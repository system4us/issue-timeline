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

// Account is one signed-in host identity inside a browser session.
type Account struct {
	Provider string
	Origin   string
	Token    string
	User     provider.User
	Grant    *Grant
}

// Session is one signed-in browser session, holding one or more host Accounts (e.g. GitHub and GitLab at once).
// ID, Provider, Origin, Token, User, Grant and Accounts never change after Create/Attach/Renew. CreatedAt and LastSeen
// are written under the lock of the Sessions that holds the session: read them only through Sessions.
type Session struct {
	ID       string
	Provider string
	Origin   string
	Token    string
	User     provider.User
	// Grant is how an OAuth sign-in renews its token (nil for a pasted key or token). A renewal replaces the whole
	// session with a copy that has the new token and grant (Renew), so these fields still never change.
	Grant    *Grant
	Accounts []Account
	// CreatedAt and LastSeen decide when the session ends (see Sessions.Get).
	CreatedAt time.Time
	LastSeen  time.Time

	// mu guards the per-repo caches below.
	mu     sync.Mutex
	now    func() time.Time
	onSave func()
	roles  map[string]roleEntry
	vis    map[string]visEntry
}

// AccountFor returns the account for (prov, origin) in this session, or false if none is signed in.
func (s *Session) AccountFor(prov, origin string) (Account, bool) {
	if s == nil {
		return Account{}, false
	}
	for _, a := range s.Accounts {
		if a.Provider == prov && a.Origin == origin {
			return a, true
		}
	}
	if len(s.Accounts) == 0 && s.Provider == prov && s.Origin == origin {
		return Account{Provider: s.Provider, Origin: s.Origin, Token: s.Token, User: s.User, Grant: s.Grant}, true
	}
	return Account{}, false
}

// AllAccounts returns a copy of every host account signed into this session.
func (s *Session) AllAccounts() []Account {
	if s == nil {
		return nil
	}
	if len(s.Accounts) == 0 && s.Provider != "" {
		return []Account{{Provider: s.Provider, Origin: s.Origin, Token: s.Token, User: s.User, Grant: s.Grant}}
	}
	out := make([]Account, len(s.Accounts))
	copy(out, s.Accounts)
	return out
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
	if s.roles == nil {
		s.roles = map[string]roleEntry{}
	}
	s.roles[repo] = roleEntry{role: role, at: s.clock()}
	save := s.onSave
	s.mu.Unlock()
	if save != nil {
		save()
	}
}

func (s *Session) snapshotRoles(now time.Time) map[string]storedRole {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.roles) == 0 {
		return nil
	}
	var out map[string]storedRole
	for repo, entry := range s.roles {
		if now.Sub(entry.at) >= cacheTTL {
			continue
		}
		if out == nil {
			out = make(map[string]storedRole, len(s.roles))
		}
		out[repo] = storedRole{Role: entry.role, At: entry.at}
	}
	return out
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
	order   *list.List
	byID    map[string]*list.Element
	mcpFile string
	mcpByID map[string]*mcpToken
	mcpHash map[string]*mcpToken
	mcpSave time.Time
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
	s.mcpByID = map[string]*mcpToken{}
	s.mcpHash = map[string]*mcpToken{}
	if opts.File != "" {
		s.mcpFile = filepath.Join(filepath.Dir(opts.File), "mcp-tokens.json")
	}
	s.load()
	s.loadMCPTokens()
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
	accts := []Account{{Provider: prov, Origin: origin, Token: token, User: user, Grant: grant}}
	s := &Session{ID: id, Provider: prov, Origin: origin, Token: token, User: user, Grant: grant, Accounts: accts, CreatedAt: at, LastSeen: at, now: ss.now, onSave: ss.persist}
	ss.byID[id] = ss.order.PushBack(s)
	ss.save()
	return id
}

// Attach adds or replaces the (prov, origin) account on an existing active session id, or creates a new session when id
// is empty or expired. Returns the session id.
func (ss *Sessions) Attach(id, prov, origin, token string, user provider.User, grant *Grant) string {
	if id == "" {
		return ss.CreateWith(prov, origin, token, user, grant)
	}
	ss.mu.Lock()
	el, ok := ss.byID[id]
	if !ok || ss.expired(el.Value.(*Session), ss.now()) {
		if ok {
			ss.remove(el)
			ss.save()
		}
		ss.mu.Unlock()
		return ss.CreateWith(prov, origin, token, user, grant)
	}
	defer ss.mu.Unlock()
	old := el.Value.(*Session)
	now := ss.now()
	accts := upsertAccount(old.AllAccounts(), Account{Provider: prov, Origin: origin, Token: token, User: user, Grant: grant})
	s := &Session{
		ID: old.ID, Provider: prov, Origin: origin, Token: token, User: user, Grant: grant,
		Accounts: accts, CreatedAt: old.CreatedAt, LastSeen: now, now: old.now, onSave: ss.persist,
	}
	el.Value = s
	ss.order.MoveToBack(el)
	ss.save()
	return s.ID
}

func upsertAccount(list []Account, next Account) []Account {
	out := make([]Account, 0, len(list)+1)
	replaced := false
	for _, a := range list {
		if a.Provider == next.Provider && a.Origin == next.Origin {
			if !replaced {
				out = append(out, next)
				replaced = true
			}
			continue
		}
		out = append(out, a)
	}
	if !replaced {
		out = append(out, next)
	}
	return out
}

// Get returns the session with id, or nil when there is none, it is idle or too old (it is then dropped), or id is
// empty. A session that is returned counts as used now.
func (ss *Sessions) Get(id string) *Session {
	if id == "" {
		return nil
	}
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if strings.HasPrefix(id, "mcp:") {
		if t := ss.mcpByID[strings.TrimPrefix(id, "mcp:")]; t != nil && !t.expired {
			return t.sess
		}
		return nil
	}
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

// Peek returns the active session with id without updating LastSeen or LRU order (used by background OAuth renewal so
// renewing a token does not extend an idle web session past Idle).
func (ss *Sessions) Peek(id string) *Session {
	if id == "" {
		return nil
	}
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if strings.HasPrefix(id, "mcp:") {
		if t := ss.mcpByID[strings.TrimPrefix(id, "mcp:")]; t != nil && !t.expired {
			return t.sess
		}
		return nil
	}
	el, ok := ss.byID[id]
	if !ok {
		return nil
	}
	s := el.Value.(*Session)
	if ss.expired(s, ss.now()) {
		return nil
	}
	return s
}

// DueSessions returns every active web session and non-expired MCP token session that has at least one OAuth grant due
// for renewal. It does not update LastSeen.
func (ss *Sessions) DueSessions() []*Session {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	now := ss.now()
	var out []*Session
	hasDue := func(s *Session) bool {
		if s == nil {
			return false
		}
		for _, a := range s.AllAccounts() {
			if a.Grant.Due(now) {
				return true
			}
		}
		return false
	}
	for el := ss.order.Front(); el != nil; el = el.Next() {
		s := el.Value.(*Session)
		if !ss.expired(s, now) && hasDue(s) {
			out = append(out, s)
		}
	}
	for _, t := range ss.mcpByID {
		if !t.expired && hasDue(t.sess) {
			out = append(out, t.sess)
		}
	}
	return out
}

// Renew gives the session with id a new token and grant (its OAuth token was renewed): the session is replaced by a copy
// with them, the same id, person and times, and caches read again with the new token. Requests already holding the old
// one finish with the old token. It returns the new session, or nil when the id is gone.
func (ss *Sessions) Renew(id, token string, grant *Grant) *Session {
	ss.mu.Lock()
	el, ok := ss.byID[id]
	if !ok {
		ss.mu.Unlock()
		return nil
	}
	old := el.Value.(*Session)
	ss.mu.Unlock()
	return ss.RenewAccount(id, old.Provider, old.Origin, token, grant)
}

func updateSessionAccount(old *Session, prov, origin, token string, grant *Grant, onSave func()) (*Session, bool) {
	accts := old.AllAccounts()
	found := false
	for i, a := range accts {
		if a.Provider == prov && a.Origin == origin {
			accts[i].Token = token
			accts[i].Grant = grant
			found = true
		}
	}
	if !found {
		return old, false
	}
	p, o, t, u, g := old.Provider, old.Origin, old.Token, old.User, old.Grant
	if p == prov && o == origin {
		t, g = token, grant
	}
	return &Session{ID: old.ID, Provider: p, Origin: o, Token: t, User: u, Grant: g, Accounts: accts,
		CreatedAt: old.CreatedAt, LastSeen: old.LastSeen, now: old.now, onSave: onSave}, true
}

// holder is the session id points at: a web session, or the one an MCP token hangs from. Callers hold ss.mu.
func (ss *Sessions) holder(id string) *Session {
	if strings.HasPrefix(id, "mcp:") {
		if t := ss.mcpByID[strings.TrimPrefix(id, "mcp:")]; t != nil && !t.expired {
			return t.sess
		}
		return nil
	}
	if el, ok := ss.byID[id]; ok {
		return el.Value.(*Session)
	}
	return nil
}

// holderIDs lists every session id and MCP token id that holds an account. Callers hold ss.mu.
func (ss *Sessions) holderIDs() []string {
	ids := make([]string, 0, len(ss.byID)+len(ss.mcpByID))
	for id := range ss.byID {
		ids = append(ids, id)
	}
	for id := range ss.mcpByID {
		ids = append(ids, "mcp:"+id)
	}
	return ids
}

// setAccountGrant stores token and grant into one (prov, origin) account of holder id. Callers hold ss.mu.
func (ss *Sessions) setAccountGrant(id, prov, origin, token string, grant *Grant) *Session {
	if strings.HasPrefix(id, "mcp:") {
		t := ss.mcpByID[strings.TrimPrefix(id, "mcp:")]
		if t == nil || t.sess == nil {
			return nil
		}
		next, changed := updateSessionAccount(t.sess, prov, origin, token, grant, ss.persistMCP)
		if !changed {
			return next
		}
		t.sess = next
		ss.saveMCPTokens()
		return next
	}
	el, ok := ss.byID[id]
	if !ok {
		return nil
	}
	s, changed := updateSessionAccount(el.Value.(*Session), prov, origin, token, grant, ss.persist)
	if !changed {
		return s
	}
	el.Value = s
	ss.save()
	return s
}

func copyGrant(g *Grant) *Grant {
	if g == nil {
		return nil
	}
	c := *g
	return &c
}

// RenewAccount updates the token and grant of one (prov, origin) account inside session id.
//
// An MCP token starts from a copy of its creator's account, so the web session and the token hold the same refresh
// token. GitLab rotates it on every renewal, which spends the other copy: renewing in one place used to leave the
// other holders with a refresh token the host refuses. Every holder of the spent refresh token gets the new grant too.
func (ss *Sessions) RenewAccount(id, prov, origin, token string, grant *Grant) *Session {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	spent := ""
	if cur := ss.holder(id); cur != nil {
		if a, ok := cur.AccountFor(prov, origin); ok && a.Grant != nil {
			spent = a.Grant.Refresh
		}
	}
	next := ss.setAccountGrant(id, prov, origin, token, grant)
	if spent == "" || next == nil {
		return next
	}
	for _, other := range ss.holderIDs() {
		if other == id {
			continue
		}
		cur := ss.holder(other)
		if cur == nil {
			continue
		}
		if a, ok := cur.AccountFor(prov, origin); ok && a.Grant != nil && a.Grant.Refresh == spent {
			ss.setAccountGrant(other, prov, origin, token, copyGrant(grant))
		}
	}
	return next
}

// AdoptSiblingGrant is the way out when the host refused a refresh token: another session or MCP token of the same
// person on the same host may hold a newer grant (it renewed first). It copies the freshest one that is not due into
// account (prov, origin) of holder id and returns the updated session, or false when there is none.
func (ss *Sessions) AdoptSiblingGrant(id, prov, origin string) (*Session, bool) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	cur := ss.holder(id)
	if cur == nil {
		return nil, false
	}
	mine, ok := cur.AccountFor(prov, origin)
	if !ok || mine.User.ID == 0 {
		return nil, false
	}
	mineRefresh := ""
	if mine.Grant != nil {
		mineRefresh = mine.Grant.Refresh
	}
	now := ss.now()
	var bestFresh, bestDue *Account
	for _, other := range ss.holderIDs() {
		if other == id {
			continue
		}
		h := ss.holder(other)
		if h == nil {
			continue
		}
		a, ok := h.AccountFor(prov, origin)
		if !ok || a.User.ID != mine.User.ID || a.Grant == nil || a.Grant.Refresh == "" || a.Grant.Refresh == mineRefresh {
			continue
		}
		if !a.Grant.Due(now) {
			if bestFresh == nil || a.Grant.Expires.After(bestFresh.Grant.Expires) {
				a := a
				bestFresh = &a
			}
		} else if bestDue == nil || a.Grant.Expires.After(bestDue.Grant.Expires) {
			a := a
			bestDue = &a
		}
	}
	best := bestFresh
	if best == nil {
		best = bestDue
	}
	if best == nil {
		return nil, false
	}
	next := ss.setAccountGrant(id, prov, origin, best.Token, copyGrant(best.Grant))
	return next, next != nil
}

// DropAccount removes one (prov, origin) account from session id, dropping the whole session if none remain.
// Returns the updated session, or nil if the whole session was dropped.
func (ss *Sessions) DropAccount(id, prov, origin string) *Session {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if strings.HasPrefix(id, "mcp:") {
		return ss.dropMCPAccount(id, prov, origin)
	}
	el, ok := ss.byID[id]
	if !ok {
		return nil
	}
	old := el.Value.(*Session)
	var kept []Account
	for _, a := range old.AllAccounts() {
		if a.Provider == prov && a.Origin == origin {
			continue
		}
		kept = append(kept, a)
	}
	if len(kept) == 0 {
		ss.remove(el)
		ss.save()
		return nil
	}
	last := kept[len(kept)-1]
	s := &Session{ID: old.ID, Provider: last.Provider, Origin: last.Origin, Token: last.Token, User: last.User, Grant: last.Grant,
		Accounts: kept, CreatedAt: old.CreatedAt, LastSeen: old.LastSeen, now: old.now, onSave: ss.persist}
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

// LatestFor returns the most recently used active session holding an account for provider and origin, or nil when none exists.
func (ss *Sessions) LatestFor(provider, origin string) *Session {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	now := ss.now()
	for el := ss.order.Back(); el != nil; el = el.Prev() {
		s := el.Value.(*Session)
		if ss.expired(s, now) {
			continue
		}
		if _, ok := s.AccountFor(provider, origin); ok {
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

type storedAccount struct {
	Provider string        `json:"provider"`
	Origin   string        `json:"origin"`
	Token    string        `json:"token"`
	User     provider.User `json:"user"`
	Grant    *storedGrant  `json:"grant,omitempty"`
}

type storedRole struct {
	Role provider.Role `json:"role"`
	At   time.Time     `json:"at"`
}

type storedSession struct {
	ID        string                `json:"id"`
	Provider  string                `json:"provider"`
	Origin    string                `json:"origin"`
	Token     string                `json:"token"`
	User      provider.User         `json:"user"`
	Grant     *storedGrant          `json:"grant,omitempty"`
	Accounts  []storedAccount       `json:"accounts,omitempty"`
	Roles     map[string]storedRole `json:"roles,omitempty"`
	CreatedAt time.Time             `json:"createdAt"`
	LastSeen  time.Time             `json:"lastSeen"`
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

func (ss *Sessions) rehydrateAccount(raw storedAccount, now time.Time) (Account, bool) {
	if raw.Provider == "" || raw.Token == "" {
		return Account{}, false
	}
	var grant *Grant
	if raw.Grant != nil {
		app, ok := ss.appFor(raw.Provider, raw.Origin)
		if !ok {
			if !raw.Grant.Expires.IsZero() && now.After(raw.Grant.Expires) {
				return Account{}, false
			}
		} else {
			grant = &Grant{App: app, Redirect: raw.Grant.Redirect, Refresh: raw.Grant.Refresh, Expires: raw.Grant.Expires}
		}
	}
	return Account{Provider: raw.Provider, Origin: raw.Origin, Token: raw.Token, User: raw.User, Grant: grant}, true
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
		if it.ID == "" {
			continue
		}
		rawAccts := it.Accounts
		if len(rawAccts) == 0 && it.Token != "" {
			rawAccts = []storedAccount{{Provider: it.Provider, Origin: it.Origin, Token: it.Token, User: it.User, Grant: it.Grant}}
		}
		var accts []Account
		for _, ra := range rawAccts {
			if a, ok := ss.rehydrateAccount(ra, now); ok {
				accts = append(accts, a)
			}
		}
		if len(accts) == 0 {
			continue
		}
		primary := accts[len(accts)-1]
		var roles map[string]roleEntry
		for repo, sr := range it.Roles {
			if now.Sub(sr.At) >= cacheTTL {
				continue
			}
			if roles == nil {
				roles = make(map[string]roleEntry, len(it.Roles))
			}
			roles[repo] = roleEntry{role: sr.Role, at: sr.At}
		}
		s := &Session{
			ID: it.ID, Provider: primary.Provider, Origin: primary.Origin, Token: primary.Token, User: primary.User,
			Grant: primary.Grant, Accounts: accts, roles: roles, onSave: ss.persist, CreatedAt: it.CreatedAt, LastSeen: it.LastSeen, now: ss.now,
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

func (ss *Sessions) persist() {
	if ss.file == "" {
		return
	}
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.save()
}

// save writes active sessions to ss.file (0600) atomically. Caller holds ss.mu.
func (ss *Sessions) save() {
	if ss.file == "" {
		return
	}
	now := ss.now()
	items := make([]storedSession, 0, ss.order.Len())
	for el := ss.order.Front(); el != nil; el = el.Next() {
		s := el.Value.(*Session)
		accts := s.AllAccounts()
		storedAccts := make([]storedAccount, 0, len(accts))
		for _, a := range accts {
			sa := storedAccount{Provider: a.Provider, Origin: a.Origin, Token: a.Token, User: a.User}
			if a.Grant != nil {
				sa.Grant = &storedGrant{Redirect: a.Grant.Redirect, Refresh: a.Grant.Refresh, Expires: a.Grant.Expires}
			}
			storedAccts = append(storedAccts, sa)
		}
		it := storedSession{
			ID: s.ID, Provider: s.Provider, Origin: s.Origin, Token: s.Token, User: s.User,
			Accounts: storedAccts, Roles: s.snapshotRoles(now), CreatedAt: s.CreatedAt, LastSeen: s.LastSeen,
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
