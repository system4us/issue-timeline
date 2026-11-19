// Package auth is who is using the app. A person signs in with their own key or token, checked against a host this
// server is configured for. The session keeps it in memory (never in a file): a restart signs everyone out, on purpose.
// Their role in a repo comes from what that host says about them there (see roles.go).
package auth

import (
	"container/list"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"gitlab.com/system4us/issue-timeline/internal/provider"
)

// SessionCookie is the name of the cookie that carries the session id.
const SessionCookie = "it_session"

// cacheTTL is how long a role or visibility read from a host is kept: a role taken away there stops counting here
// within this time.
const cacheTTL = 5 * time.Minute

// defaultMax is how many sessions are kept at once.
const defaultMax = 1000

// Session is one signed-in person. ID, Provider, Origin, Token and User never change after Create. CreatedAt and
// LastSeen are written under the lock of the Sessions that holds the session: read them only through Sessions.
type Session struct {
	ID       string
	Provider string
	Origin   string
	Token    string
	User     provider.User
	// Grant is how an OAuth sign-in renews its token (nil for a pasted key or token). A renewal replaces the whole
	// session with a copy that has the new token and grant (Renew), so these fields still never change.
	Grant *Grant
	// CreatedAt and LastSeen decide when the session ends (see Sessions.Get).
	CreatedAt time.Time
	LastSeen  time.Time

	// mu guards the per-repo caches below.
	mu    sync.Mutex
	now   func() time.Time
	roles map[string]roleEntry
	vis   map[string]visEntry
}

type roleEntry struct {
	role provider.Role
	at   time.Time
}

type visEntry struct {
	vis provider.Visibility
	at  time.Time
}

func (s *Session) clock() time.Time {
	if s.now == nil {
		return time.Now()
	}
	return s.now()
}

// CachedRole is the role kept for repo while it is fresh (ok is false when it must be read again). A kept RoleNone is
// a real answer: the person is not a member.
func (s *Session) CachedRole(repo string) (provider.Role, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	hit, ok := s.roles[repo]
	if !ok || s.clock().Sub(hit.at) >= cacheTTL {
		return provider.RoleNone, false
	}
	return hit.role, true
}

// KeepRole stores the role read for repo.
func (s *Session) KeepRole(repo string, role provider.Role) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.roles == nil {
		s.roles = map[string]roleEntry{}
	}
	s.roles[repo] = roleEntry{role: role, at: s.clock()}
}

// CachedVisibility is the visibility kept for repo while it is fresh. The maps in it are shared with the cache: callers
// must not change them.
func (s *Session) CachedVisibility(repo string) (provider.Visibility, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	hit, ok := s.vis[repo]
	if !ok || s.clock().Sub(hit.at) >= cacheTTL {
		return provider.Visibility{}, false
	}
	return hit.vis, true
}

// KeepVisibility stores the visibility read for repo.
func (s *Session) KeepVisibility(repo string, v provider.Visibility) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.vis == nil {
		s.vis = map[string]visEntry{}
	}
	s.vis[repo] = visEntry{vis: v, at: s.clock()}
}

// SessionOptions sets the clock and the limits. A zero field takes its default: Now is time.Now, Idle is
// SESSION_IDLE_HOURS (12) hours, MaxAge is SESSION_MAX_DAYS (7) days, Max is 1000, Env (default os.Getenv) is where
// the two SESSION_ variables are read from, and File optionally persists sessions across restarts (owner-only).
type SessionOptions struct {
	Now    func() time.Time
	Idle   time.Duration
	MaxAge time.Duration
	Max    int
	Env    func(string) string
	File   string
}

const saveInterval = time.Minute

// Sessions keeps the signed-in people in memory (and optionally in File across restarts). A session ends after Idle
// without a request, or MaxAge after it began, whichever is first. When there are more than Max, the least recently
// used one goes.
type Sessions struct {
	mu        sync.Mutex
	now       func() time.Time
	idle      time.Duration
	maxAge    time.Duration
	max       int
	env       func(string) string
	file      string
	lastSaved time.Time
	// order is least recently used first; byID finds an element of it.
	order *list.List
	byID  map[string]*list.Element
}

// NewSessions returns a set of sessions (loaded from opts.File when set) with the limits in opts (defaults where opts
// leaves them zero).
func NewSessions(opts SessionOptions) *Sessions {
	env := opts.Env
	if env == nil {
		env = os.Getenv
	}
	s := &Sessions{now: opts.Now, idle: opts.Idle, maxAge: opts.MaxAge, max: opts.Max, env: env, file: opts.File}
	if s.now == nil {
		s.now = time.Now
	}
	if s.idle <= 0 {
		s.idle = time.Duration(hoursFrom(env("SESSION_IDLE_HOURS"), 12) * float64(time.Hour))
	}
	if s.maxAge <= 0 {
		s.maxAge = time.Duration(hoursFrom(env("SESSION_MAX_DAYS"), 7) * 24 * float64(time.Hour))
	}
	if s.max <= 0 {
		s.max = defaultMax
	}
	s.order = list.New()
	s.byID = map[string]*list.Element{}
	s.load()
	return s
}

// hoursFrom reads a positive number from an environment value; anything else gives the default. (The JS reads it with
// Number(), where an empty value is 0: that would end every session at once.)
func hoursFrom(v string, def float64) float64 {
	if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil && f > 0 {
		return f
	}
	return def
}

// Create starts a session for a signed-in person and returns its id: 32 random bytes, base64url. It panics when the
// system has no random source, as there is then no safe id to give.
func (ss *Sessions) Create(prov, origin, token string, user provider.User) string {
	return ss.CreateWith(prov, origin, token, user, nil)
}

// CreateWith is Create for an OAuth sign-in: the session keeps the grant that renews its token.
func (ss *Sessions) CreateWith(prov, origin, token string, user provider.User, grant *Grant) string {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		panic(fmt.Sprintf("no random source for a session id: %v", err))
	}
	id := base64.RawURLEncoding.EncodeToString(raw)

	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.sweep()
	// too many: the one used least recently goes
	for ss.order.Len() >= ss.max {
		ss.remove(ss.order.Front())
	}
	at := ss.now()
	s := &Session{ID: id, Provider: prov, Origin: origin, Token: token, User: user, Grant: grant, CreatedAt: at, LastSeen: at, now: ss.now}
	ss.byID[id] = ss.order.PushBack(s)
	ss.save()
	return id
}

// Get returns the session with id, or nil when there is none, it is idle or too old (it is then dropped), or id is
// empty. A session that is returned counts as used now.
func (ss *Sessions) Get(id string) *Session {
	if id == "" {
		return nil
	}
	ss.mu.Lock()
	defer ss.mu.Unlock()
	el, ok := ss.byID[id]
	if !ok {
		return nil
	}
	s := el.Value.(*Session)
	now := ss.now()
	if ss.expired(s, now) {
		ss.remove(el)
		ss.save()
		return nil
	}
	s.LastSeen = now
	ss.order.MoveToBack(el)
	if now.Sub(ss.lastSaved) >= saveInterval {
		ss.save()
	}
	return s
}

// Renew gives the session with id a new token and grant (its OAuth token was renewed): the session is replaced by a copy
// with them, the same id, person and times, and caches read again with the new token. Requests already holding the old
// one finish with the old token. It returns the new session, or nil when the id is gone.
func (ss *Sessions) Renew(id, token string, grant *Grant) *Session {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	el, ok := ss.byID[id]
	if !ok {
		return nil
	}
	old := el.Value.(*Session)
	s := &Session{ID: old.ID, Provider: old.Provider, Origin: old.Origin, Token: token, User: old.User, Grant: grant,
		CreatedAt: old.CreatedAt, LastSeen: old.LastSeen, now: old.now}
	el.Value = s
	ss.save()
	return s
}

// Drop ends the session with id (sign-out). Unknown ids are ignored.
func (ss *Sessions) Drop(id string) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if el, ok := ss.byID[id]; ok {
		ss.remove(el)
		ss.save()
	}
}

// LatestFor returns the most recently used active session for provider and origin, or nil when none exists.
func (ss *Sessions) LatestFor(provider, origin string) *Session {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	now := ss.now()
	for el := ss.order.Back(); el != nil; el = el.Prev() {
		s := el.Value.(*Session)
		if ss.expired(s, now) {
			continue
		}
		if s.Provider == provider && s.Origin == origin {
			return s
		}
	}
	return nil
}

// Len is how many sessions are kept, expired ones included until they are swept or asked for.
func (ss *Sessions) Len() int {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return ss.order.Len()
}

// sweep drops every expired session. The caller holds ss.mu.
func (ss *Sessions) sweep() {
	now := ss.now()
	for el := ss.order.Front(); el != nil; {
		next := el.Next()
		if ss.expired(el.Value.(*Session), now) {
			ss.remove(el)
		}
		el = next
	}
}

func (ss *Sessions) expired(s *Session, now time.Time) bool {
	return now.Sub(s.LastSeen) > ss.idle || now.Sub(s.CreatedAt) > ss.maxAge
}

func (ss *Sessions) remove(el *list.Element) {
	s := el.Value.(*Session)
	delete(ss.byID, s.ID)
	ss.order.Remove(el)
}

var sessionCookieRE = regexp.MustCompile(`(?:^|;\s*)` + SessionCookie + `=([A-Za-z0-9_-]+)`)

// SessionIDOf is the session id a request carries in its cookie header, or "" when it has none.
func SessionIDOf(r *http.Request) string {
	cookie := strings.Join(r.Header.Values("Cookie"), "; ")
	m := sessionCookieRE.FindStringSubmatch(cookie)
	if m == nil {
		return ""
	}
	return m[1]
}

// CookieFor is the Set-Cookie value for a session: HttpOnly and SameSite=Strict, so no page from another site carries
// it. secure adds Secure, for a server reached over HTTPS (behind a proxy).
func CookieFor(id string, secure bool) string {
	return SessionCookie + "=" + id + "; Path=/; HttpOnly; SameSite=Strict" + secureAttr(secure)
}

// ClearedCookie is the Set-Cookie value that removes the session cookie.
func ClearedCookie(secure bool) string {
	return SessionCookie + "=; Path=/; Max-Age=0; HttpOnly; SameSite=Strict" + secureAttr(secure)
}

func secureAttr(secure bool) string {
	if secure {
		return "; Secure"
	}
	return ""
}

// WantsSecureCookie is true when the session cookie should carry Secure: COOKIE_SECURE=1, or a proxy that says the
// request came over HTTPS.
func WantsSecureCookie(r *http.Request) bool {
	return os.Getenv("COOKIE_SECURE") == "1" || r.Header.Get("X-Forwarded-Proto") == "https"
}

type storedGrant struct {
	Redirect string    `json:"redirect"`
	Refresh  string    `json:"refresh"`
	Expires  time.Time `json:"expires"`
}

type storedSession struct {
	ID        string        `json:"id"`
	Provider  string        `json:"provider"`
	Origin    string        `json:"origin"`
	Token     string        `json:"token"`
	User      provider.User `json:"user"`
	Grant     *storedGrant  `json:"grant,omitempty"`
	CreatedAt time.Time     `json:"createdAt"`
	LastSeen  time.Time     `json:"lastSeen"`
}

// appFor rehydrates the configured OAuthApp for (prov, origin) from ss.env without storing ClientSecret on disk.
func (ss *Sessions) appFor(prov, origin string) (OAuthApp, bool) {
	var a OAuthApp
	var ok bool
	switch prov {
	case "github":
		a, ok = OAuthGitHub(ss.env)
	case "gitlab":
		a, ok = OAuthGitLab(ss.env, []string{origin})
	}
	if !ok || a.Origin != origin {
		return OAuthApp{}, false
	}
	return a, true
}

func (ss *Sessions) load() {
	if ss.file == "" {
		return
	}
	b, err := os.ReadFile(ss.file)
	if err != nil {
		return
	}
	var items []storedSession
	if err := json.Unmarshal(b, &items); err != nil {
		return
	}
	now := ss.now()
	for _, it := range items {
		if it.ID == "" || it.Token == "" {
			continue
		}
		var grant *Grant
		if it.Grant != nil {
			app, ok := ss.appFor(it.Provider, it.Origin)
			if !ok {
				// Without the OAuth app secret in env, an expired OAuth session cannot renew; drop if already expired.
				if !it.Grant.Expires.IsZero() && now.After(it.Grant.Expires) {
					continue
				}
			} else {
				grant = &Grant{App: app, Redirect: it.Grant.Redirect, Refresh: it.Grant.Refresh, Expires: it.Grant.Expires}
			}
		}
		s := &Session{
			ID: it.ID, Provider: it.Provider, Origin: it.Origin, Token: it.Token, User: it.User,
			Grant: grant, CreatedAt: it.CreatedAt, LastSeen: it.LastSeen, now: ss.now,
		}
		if ss.expired(s, now) {
			continue
		}
		for ss.order.Len() >= ss.max {
			ss.remove(ss.order.Front())
		}
		ss.byID[s.ID] = ss.order.PushBack(s)
	}
	ss.lastSaved = now
}

// save writes active sessions to ss.file (0600) atomically. Caller holds ss.mu.
func (ss *Sessions) save() {
	if ss.file == "" {
		return
	}
	items := make([]storedSession, 0, ss.order.Len())
	for el := ss.order.Front(); el != nil; el = el.Next() {
		s := el.Value.(*Session)
		it := storedSession{
			ID: s.ID, Provider: s.Provider, Origin: s.Origin, Token: s.Token, User: s.User,
			CreatedAt: s.CreatedAt, LastSeen: s.LastSeen,
		}
		if s.Grant != nil {
			it.Grant = &storedGrant{Redirect: s.Grant.Redirect, Refresh: s.Grant.Refresh, Expires: s.Grant.Expires}
		}
		items = append(items, it)
	}
	b, err := json.Marshal(items)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(ss.file), 0o700); err != nil {
		return
	}
	tmp := ss.file + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return
	}
	_ = os.Chmod(tmp, 0o600)
	if os.Rename(tmp, ss.file) == nil {
		ss.lastSaved = ss.now()
	}
}
