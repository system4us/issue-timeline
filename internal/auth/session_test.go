package auth

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gitlab.com/system4us/issue-timeline/internal/provider"
)

// fakeClock is a clock the test moves by hand.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newClock() *fakeClock { return &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)} }

func TestTheSessionCookieIsReadFromTheRequestAndASessionIsDroppedOnSignOut(t *testing.T) {
	r := &http.Request{Header: http.Header{"Cookie": {"theme=dark; it_session=abc_DEF-1; other=1"}}}
	if id := SessionIDOf(r); id != "abc_DEF-1" {
		t.Fatalf("SessionIDOf = %q", id)
	}
	if id := SessionIDOf(&http.Request{Header: http.Header{}}); id != "" {
		t.Fatalf("no cookie: %q", id)
	}
	if c := CookieFor("x", false); !strings.Contains(c, "HttpOnly") || !strings.Contains(c, "SameSite=Strict") {
		t.Fatalf("cookie = %s", c)
	}
	ss := NewSessions(SessionOptions{})
	id := ss.Create("github", "github", "tok", provider.User{Login: "ana"})
	if s := ss.Get(id); s == nil || s.User.Login != "ana" {
		t.Fatalf("Get after Create = %+v", s)
	}
	ss.Drop(id)
	if ss.Get(id) != nil {
		t.Fatal("a dropped session is still there")
	}
	if ss.Get("") != nil {
		t.Fatal("empty id gave a session")
	}
}

func TestSessionIDsAreRandomBase64URL32Bytes(t *testing.T) {
	ss := NewSessions(SessionOptions{})
	a := ss.Create("github", "github", "t", provider.User{})
	b := ss.Create("github", "github", "t", provider.User{})
	if a == b {
		t.Fatal("two sessions with the same id")
	}
	if len(a) != 43 { // 32 bytes in base64url without padding
		t.Fatalf("id length = %d, want 43", len(a))
	}
	if got := SessionIDOf(&http.Request{Header: http.Header{"Cookie": {SessionCookie + "=" + a}}}); got != a {
		t.Fatalf("the id does not read back from a cookie: %q", got)
	}
}

func TestASessionEndsWhenIdleTooLongOrTooOldAndTheOldestGoesWhenThereAreTooMany(t *testing.T) {
	c := newClock()
	ss := NewSessions(SessionOptions{Now: c.now, Idle: 100 * time.Millisecond, MaxAge: time.Second, Max: 2})
	a := ss.Create("github", "github", "t", provider.User{Login: "a"})
	c.advance(50 * time.Millisecond)
	if ss.Get(a) == nil {
		t.Fatal("a: still in use, but gone")
	}
	c.advance(151 * time.Millisecond)
	if ss.Get(a) != nil {
		t.Fatal("a: idle for more than 100 ms, still there")
	}

	b := ss.Create("github", "github", "t", provider.User{Login: "b"})
	// used every 90 ms (inside the 100 ms idle limit) for 990 ms: still there
	for i := 0; i < 11; i++ {
		c.advance(90 * time.Millisecond)
		if ss.Get(b) == nil {
			t.Fatalf("b: in use %d ms after it began, but gone", (i+1)*90)
		}
	}
	// 20 ms more makes it older than the maximum age (1 s), however used
	c.advance(20 * time.Millisecond)
	if ss.Get(b) != nil {
		t.Fatal("b: older than the maximum age, however used, still there")
	}

	cc := ss.Create("github", "github", "t", provider.User{Login: "c"})
	d := ss.Create("github", "github", "t", provider.User{Login: "d"})
	if ss.Get(cc) == nil { // c is used: d is now the least recently used
		t.Fatal("c gone")
	}
	ss.Create("github", "github", "t", provider.User{Login: "e"})
	if ss.Len() != 2 {
		t.Fatalf("Len = %d, want 2", ss.Len())
	}
	if ss.Get(cc) == nil {
		t.Fatal("c, used recently, was evicted")
	}
	if ss.Get(d) != nil {
		t.Fatal("d, the least recently used, was kept")
	}
}

func TestDefaultsComeFromTheEnvironmentWhenTheOptionsLeaveThemOut(t *testing.T) {
	env := func(k string) string {
		switch k {
		case "SESSION_IDLE_HOURS":
			return "2"
		case "SESSION_MAX_DAYS":
			return "3"
		}
		return ""
	}
	c := newClock()
	ss := NewSessions(SessionOptions{Now: c.now, Env: env})
	if ss.idle != 2*time.Hour || ss.maxAge != 72*time.Hour || ss.max != 1000 {
		t.Fatalf("idle %v, maxAge %v, max %d", ss.idle, ss.maxAge, ss.max)
	}
	// a value that is not a positive number gives the default (the JS would end every session at once on "")
	ss = NewSessions(SessionOptions{Env: func(k string) string {
		if k == "SESSION_IDLE_HOURS" {
			return ""
		}
		return "abc"
	}})
	if ss.idle != 12*time.Hour || ss.maxAge != 7*24*time.Hour {
		t.Fatalf("defaults: idle %v, maxAge %v", ss.idle, ss.maxAge)
	}
}

func TestARoleIsKeptForAFewMinutesThenReadAgain(t *testing.T) {
	c := newClock()
	ss := NewSessions(SessionOptions{Now: c.now})
	s := ss.Get(ss.Create("gitlab", "https://gitlab.example", "t", provider.User{}))
	if _, ok := s.CachedRole("o/r"); ok {
		t.Fatal("a role is there before it is kept")
	}
	s.KeepRole("o/r", provider.RoleEditor)
	c.advance(time.Second)
	if role, ok := s.CachedRole("o/r"); !ok || role != provider.RoleEditor {
		t.Fatalf("CachedRole = %q, %v", role, ok)
	}
	c.advance(cacheTTL)
	if _, ok := s.CachedRole("o/r"); ok {
		t.Fatal("a role older than the TTL is still kept")
	}
	s.KeepRole("o/x", provider.RoleNone)
	if role, ok := s.CachedRole("o/x"); !ok || role != provider.RoleNone {
		t.Fatal("not being a member is kept as well")
	}
}

func TestAVisibilityIsKeptForFiveMinutesPerRepo(t *testing.T) {
	c := newClock()
	ss := NewSessions(SessionOptions{Now: c.now})
	s := ss.Get(ss.Create("redmine", "https://r.example", "k", provider.User{}))
	if _, ok := s.CachedVisibility("r/x"); ok {
		t.Fatal("visibility is there before it is kept")
	}
	s.KeepVisibility("r/x", provider.Visibility{HideCode: true})
	v, ok := s.CachedVisibility("r/x")
	if !ok || !v.HideCode {
		t.Fatalf("CachedVisibility = %+v, %v", v, ok)
	}
	if _, ok := s.CachedVisibility("r/y"); ok {
		t.Fatal("visibility of another repo")
	}
	c.advance(cacheTTL - time.Second)
	if _, ok := s.CachedVisibility("r/x"); !ok {
		t.Fatal("visibility gone before the TTL")
	}
	c.advance(time.Second)
	if _, ok := s.CachedVisibility("r/x"); ok {
		t.Fatal("visibility kept past the TTL")
	}
}

func TestTheCookieIsSecureOnRequestOrBehindAnHTTPSProxy(t *testing.T) {
	t.Setenv("COOKIE_SECURE", "")
	if c := CookieFor("x", true); !strings.HasSuffix(c, "; Secure") {
		t.Fatalf("secure = %s", c)
	}
	if c := CookieFor("x", false); strings.Contains(c, "Secure") {
		t.Fatalf("not secure = %s", c)
	}
	if c := ClearedCookie(true); !strings.HasSuffix(c, "; Secure") || !strings.Contains(c, "Max-Age=0") {
		t.Fatalf("cleared = %s", c)
	}
	proxied := &http.Request{Header: http.Header{"X-Forwarded-Proto": {"https"}}}
	if !WantsSecureCookie(proxied) {
		t.Fatal("an HTTPS proxy did not ask for Secure")
	}
	if WantsSecureCookie(&http.Request{Header: http.Header{}}) {
		t.Fatal("plain request asked for Secure")
	}
	t.Setenv("COOKIE_SECURE", "1")
	if !WantsSecureCookie(&http.Request{Header: http.Header{}}) {
		t.Fatal("COOKIE_SECURE=1 did not ask for Secure")
	}
}

func TestConcurrentSessionUseIsSafe(t *testing.T) {
	ss := NewSessions(SessionOptions{Max: 20})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				id := ss.Create("github", "github", fmt.Sprint(i), provider.User{ID: j})
				if s := ss.Get(id); s != nil {
					s.KeepRole("o/r", provider.RoleViewer)
					s.KeepVisibility("o/r", provider.Visibility{HideCode: j%2 == 0})
					s.CachedRole("o/r")
					s.CachedVisibility("o/r")
				}
				ss.Get(id)
				if j%3 == 0 {
					ss.Drop(id)
				}
			}
		}(i)
	}
	wg.Wait()
	if n := ss.Len(); n > 20 {
		t.Fatalf("Len = %d, over the maximum of 20", n)
	}
}

func TestSessionsPersistAcrossRestartsWithoutStoringClientSecret(t *testing.T) {
	c := newClock()
	dir := t.TempDir()
	file := filepath.Join(dir, "sessions.json")
	env := func(k string) string {
		return map[string]string{
			"GITLAB_OAUTH_CLIENT_ID":     "cid",
			"GITLAB_OAUTH_CLIENT_SECRET": "super-secret",
		}[k]
	}
	ss := NewSessions(SessionOptions{Now: c.now, Env: env, File: file})
	app, _ := OAuthGitLab(env, []string{"https://gitlab.com"})
	id := ss.CreateWith("gitlab", "https://gitlab.com", "at1", provider.User{ID: 7, Login: "ana"}, &Grant{
		App:      app,
		Redirect: "http://localhost:4310/api/oauth/gitlab/callback",
		Refresh:  "rt1",
		Expires:  c.now().Add(2 * time.Hour),
	})
	dropped := ss.Create("github", "github", "gh1", provider.User{Login: "bob"})
	ss.Drop(dropped)

	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %v, want 0600", info.Mode().Perm())
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "super-secret") {
		t.Fatal("client secret was written to sessions.json")
	}

	// Simulate restart: new Sessions reads from file and rehydrates OAuthApp from env.
	restarted := NewSessions(SessionOptions{Now: c.now, Env: env, File: file})
	if restarted.Get(dropped) != nil {
		t.Fatal("dropped session came back after restart")
	}
	got := restarted.Get(id)
	if got == nil || got.User.Login != "ana" || got.Token != "at1" {
		t.Fatalf("restored session = %+v", got)
	}
	if got.Grant == nil || got.Grant.Refresh != "rt1" || got.Grant.App.ClientSecret != "super-secret" {
		t.Fatalf("restored grant = %+v", got.Grant)
	}

	// Renew updates disk too, and expired sessions are dropped on restart.
	restarted.Renew(id, "at2", &Grant{App: app, Redirect: got.Grant.Redirect, Refresh: "rt2", Expires: c.now().Add(3 * time.Hour)})
	restarted2 := NewSessions(SessionOptions{Now: c.now, Env: env, File: file})
	if got2 := restarted2.Get(id); got2 == nil || got2.Token != "at2" || got2.Grant.Refresh != "rt2" {
		t.Fatalf("renewed session after restart = %+v", got2)
	}
	c.advance(8 * 24 * time.Hour)
	restarted3 := NewSessions(SessionOptions{Now: c.now, Env: env, File: file})
	if restarted3.Get(id) != nil {
		t.Fatal("expired session survived restart")
	}
}

func TestMultiProviderAttachRenewDropAndPersist(t *testing.T) {
	c := newClock()
	dir := t.TempDir()
	file := filepath.Join(dir, "sessions.json")
	ss := NewSessions(SessionOptions{Now: c.now, File: file})

	id := ss.Attach("", "github", "github", "gh-1", provider.User{ID: 1, Login: "ghuser"}, nil)
	id2 := ss.Attach(id, "gitlab", "https://gitlab.com", "gl-1", provider.User{ID: 2, Login: "gluser"}, &Grant{
		Redirect: "http://localhost:4310/api/oauth/gitlab/callback", Refresh: "rt1", Expires: c.now().Add(2 * time.Hour),
	})
	if id2 != id {
		t.Fatalf("Attach created new id %q, want %q", id2, id)
	}
	reloaded := NewSessions(SessionOptions{Now: c.now, File: file})
	s := reloaded.Get(id)
	if s == nil || len(s.AllAccounts()) != 2 {
		t.Fatalf("restored multi-account session = %+v", s)
	}
	if a, ok := s.AccountFor("github", "github"); !ok || a.Token != "gh-1" {
		t.Fatalf("github account = %+v, %v", a, ok)
	}
	if a, ok := s.AccountFor("gitlab", "https://gitlab.com"); !ok || a.Token != "gl-1" {
		t.Fatalf("gitlab account = %+v, %v", a, ok)
	}
	if s1 := reloaded.LatestFor("github", "github"); s1 == nil {
		t.Fatal("LatestFor(github) nil")
	}
	if s2 := reloaded.LatestFor("gitlab", "https://gitlab.com"); s2 == nil {
		t.Fatal("LatestFor(gitlab) nil")
	}

	reloaded.DropAccount(id, "github", "github")
	reloaded2 := NewSessions(SessionOptions{Now: c.now, File: file})
	sAfter := reloaded2.Get(id)
	if sAfter == nil || len(sAfter.AllAccounts()) != 1 {
		t.Fatalf("after DropAccount(github): %+v", sAfter)
	}
	if _, ok := sAfter.AccountFor("github", "github"); ok {
		t.Fatal("github account remained after DropAccount")
	}
	if _, ok := sAfter.AccountFor("gitlab", "https://gitlab.com"); !ok {
		t.Fatal("gitlab account disappeared after DropAccount(github)")
	}

	sAfter.KeepRole("gitlab:https://gitlab.com/a/b", provider.RoleEditor)
	reloaded3 := NewSessions(SessionOptions{Now: c.now, File: file})
	sRole := reloaded3.Get(id)
	if r, ok := sRole.CachedRole("gitlab:https://gitlab.com/a/b"); !ok || r != provider.RoleEditor {
		t.Fatalf("restored CachedRole = %v, %v, want Editor, true", r, ok)
	}
	c.advance(cacheTTL + time.Second)
	reloaded4 := NewSessions(SessionOptions{Now: c.now, File: file})
	sExpiredRole := reloaded4.Get(id)
	if _, ok := sExpiredRole.CachedRole("gitlab:https://gitlab.com/a/b"); ok {
		t.Fatal("expired role survived restart")
	}
}

func TestMCPTokensCreateAuthenticatePersistAndRevoke(t *testing.T) {
	c := newClock()
	dir := t.TempDir()
	file := filepath.Join(dir, "sessions.json")
	ss := NewSessions(SessionOptions{Now: c.now, File: file})

	if _, _, err := ss.CreateMCPToken("remote-no-login", nil, false); err == nil {
		t.Fatal("expected error creating remote MCP token without session")
	}

	sid := ss.Create("github", "github", "gh-tok", provider.User{ID: 1, Login: "ana"})
	s := ss.Get(sid)
	raw, info, err := ss.CreateMCPToken("Cursor", s, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(raw, "it_mcp_") || info.Label != "Cursor" || info.Local {
		t.Fatalf("unexpected token info: %+v", info)
	}

	onDisk, err := os.ReadFile(filepath.Join(dir, "mcp-tokens.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(onDisk), raw) {
		t.Fatal("raw MCP token must never be saved to disk")
	}

	reloaded := NewSessions(SessionOptions{Now: c.now, File: file})
	ms, isLocal, ok := reloaded.AuthenticateMCP(raw)
	if !ok || isLocal || ms == nil {
		t.Fatalf("AuthenticateMCP = %v, %v, %v", ms, isLocal, ok)
	}
	if a, ok := ms.AccountFor("github", "github"); !ok || a.Token != "gh-tok" {
		t.Fatalf("bound account = %+v, %v", a, ok)
	}

	other := reloaded.Get(reloaded.Create("github", "github", "gh-bob", provider.User{ID: 2, Login: "bob"}))
	if reloaded.RevokeMCPToken(info.ID, other, false) {
		t.Fatal("other user must not revoke ana's token")
	}
	if !reloaded.RevokeMCPToken(info.ID, s, false) {
		t.Fatal("owner should revoke token")
	}
	if _, _, ok := reloaded.AuthenticateMCP(raw); ok {
		t.Fatal("revoked token still authenticated")
	}
}

func TestARevokedGrantExpiresTheMCPTokenInsteadOfLeavingItRetrying(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "sessions.json")
	c := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	ss := NewSessions(SessionOptions{Now: c.now, File: file})
	s := ss.Get(ss.Create("gitlab", "https://gitlab.com", "gl-tok", provider.User{ID: 1, Login: "ana"}))
	raw, info, err := ss.CreateMCPToken("Claude", s, false)
	if err != nil {
		t.Fatal(err)
	}
	ms, _, ok := ss.AuthenticateMCP(raw)
	if !ok || ms == nil {
		t.Fatal("token should work before the grant is lost")
	}
	if got := ss.DropAccount(ms.ID, "gitlab", "https://gitlab.com"); got != nil {
		t.Fatalf("no account is left: %+v", got)
	}
	if _, _, ok := ss.AuthenticateMCP(raw); ok {
		t.Fatal("an expired token must stop authenticating")
	}
	if !ss.MCPTokenExpired(raw) {
		t.Fatal("the token should report expired, so the caller can say what to do")
	}
	if ss.MCPTokenExpired("it_mcp_unknown") {
		t.Fatal("an unknown token is invalid, not expired")
	}
	list := ss.ListMCPTokens(s, false)
	if len(list) != 1 || !list[0].Expired || list[0].ID != info.ID {
		t.Fatalf("the owner still sees it, marked expired: %+v", list)
	}
	reloaded := NewSessions(SessionOptions{Now: c.now, File: file})
	if !reloaded.MCPTokenExpired(raw) {
		t.Fatal("expiry survives a restart")
	}
	if !reloaded.RevokeMCPToken(info.ID, reloaded.Get(reloaded.Create("gitlab", "https://gitlab.com", "x", provider.User{ID: 1, Login: "ana"})), false) {
		t.Fatal("the owner can revoke an expired token")
	}
}

// signInWithGrant makes a web session whose gitlab account carries a refresh token.
func signInWithGrant(ss *Sessions, c *fakeClock, id int, login, token, refresh string, ttl time.Duration) *Session {
	sid := ss.Create("gitlab", "https://gitlab.com", token, provider.User{ID: id, Login: login})
	return ss.RenewAccount(sid, "gitlab", "https://gitlab.com", token,
		&Grant{Refresh: refresh, Redirect: "https://it/cb", Expires: c.now().Add(ttl)})
}

func TestRenewingOneCopyOfAGrantRenewsTheOthers(t *testing.T) {
	dir := t.TempDir()
	c := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	ss := NewSessions(SessionOptions{Now: c.now, File: filepath.Join(dir, "sessions.json")})
	web := signInWithGrant(ss, c, 7, "leo", "at1", "rt1", 2*time.Hour)
	raw, _, err := ss.CreateMCPToken("Claude", web, false) // copies the account, refresh token included
	if err != nil {
		t.Fatal(err)
	}
	other := signInWithGrant(ss, c, 7, "leo", "bt1", "other-rt", 2*time.Hour) // a second sign-in: its own grant

	// the web session renews first; GitLab rotates rt1, which spends the token's copy
	later := c.now().Add(2 * time.Hour)
	ss.RenewAccount(web.ID, "gitlab", "https://gitlab.com", "at2", &Grant{Refresh: "rt2", Redirect: "https://it/cb", Expires: later})

	ms, _, ok := ss.AuthenticateMCP(raw)
	if !ok {
		t.Fatal("token should authenticate")
	}
	a, _ := ms.AccountFor("gitlab", "https://gitlab.com")
	if a.Token != "at2" || a.Grant.Refresh != "rt2" {
		t.Fatalf("the MCP token kept the spent grant: %+v", a)
	}
	b, _ := ss.Get(other.ID).AccountFor("gitlab", "https://gitlab.com")
	if b.Grant.Refresh != "other-rt" {
		t.Fatalf("a separate sign-in must keep its own grant: %+v", b)
	}
}

func TestARefusedGrantAdoptsTheNewerOneOfAnotherSession(t *testing.T) {
	dir := t.TempDir()
	c := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	ss := NewSessions(SessionOptions{Now: c.now, File: filepath.Join(dir, "sessions.json")})
	old := signInWithGrant(ss, c, 7, "leo", "at1", "rt1", time.Minute) // about to be due
	fresh := signInWithGrant(ss, c, 7, "leo", "at9", "rt9", 2*time.Hour)
	stranger := signInWithGrant(ss, c, 8, "mia", "at8", "rt8", 3*time.Hour)

	next, ok := ss.AdoptSiblingGrant(old.ID, "gitlab", "https://gitlab.com")
	if !ok {
		t.Fatal("a newer grant of the same person exists")
	}
	if a, _ := next.AccountFor("gitlab", "https://gitlab.com"); a.Token != "at9" || a.Grant.Refresh != "rt9" {
		t.Fatalf("adopted %+v, want leo's newer grant and never mia's", a)
	}
	if _, ok := ss.AdoptSiblingGrant(stranger.ID, "gitlab", "https://gitlab.com"); ok {
		t.Fatal("nobody else holds a grant of mia")
	}
	_ = fresh
}

func TestDueSessionsAndPeekDoNotExtendIdleWebSessions(t *testing.T) {
	dir := t.TempDir()
	c := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	ss := NewSessions(SessionOptions{Now: c.now, Idle: 2 * time.Hour, File: filepath.Join(dir, "sessions.json")})
	web := signInWithGrant(ss, c, 7, "leo", "at1", "rt1", 3*time.Minute) // already due (<5m)
	raw, _, err := ss.CreateMCPToken("Claude", web, false)
	if err != nil {
		t.Fatal(err)
	}
	ms, _, ok := ss.AuthenticateMCP(raw)
	if !ok || ms == nil {
		t.Fatal("expected active MCP token")
	}
	origLastSeen := web.LastSeen

	c.advance(30 * time.Minute)
	due := ss.DueSessions()
	if len(due) != 2 {
		t.Fatalf("expected 2 due sessions (web + mcp), got %d", len(due))
	}
	peeked := ss.Peek(web.ID)
	if peeked == nil || !peeked.LastSeen.Equal(origLastSeen) {
		t.Fatalf("Peek must not bump LastSeen: got %v, want %v", peeked, origLastSeen)
	}

	// After Idle elapses, the web session expires even though Peek/DueSessions ran, while the MCP token remains due.
	c.advance(2 * time.Hour)
	if ss.Peek(web.ID) != nil {
		t.Fatal("idle web session should have expired")
	}
	due = ss.DueSessions()
	if len(due) != 1 || due[0].ID != ms.ID {
		t.Fatalf("only MCP token should remain due after web idle expiry: %+v", due)
	}
}

func TestAdoptSiblingGrantFallsBackToDueSiblingWithDifferentRefresh(t *testing.T) {
	dir := t.TempDir()
	c := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	ss := NewSessions(SessionOptions{Now: c.now, File: filepath.Join(dir, "sessions.json")})
	old := signInWithGrant(ss, c, 7, "leo", "at1", "rt1", time.Minute)
	dueSibling := signInWithGrant(ss, c, 7, "leo", "at2", "rt2", 2*time.Minute) // also due (<5m), but different refresh

	next, ok := ss.AdoptSiblingGrant(old.ID, "gitlab", "https://gitlab.com")
	if !ok {
		t.Fatal("should adopt due sibling with a different refresh token")
	}
	if a, _ := next.AccountFor("gitlab", "https://gitlab.com"); a.Grant == nil || a.Grant.Refresh != "rt2" {
		t.Fatalf("adopted %+v, want rt2", a)
	}
	_ = dueSibling
}
