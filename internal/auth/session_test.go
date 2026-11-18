package auth

import (
	"fmt"
	"net/http"
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
