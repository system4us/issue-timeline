package httpapi

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"gitlab.com/system4us/issue-timeline/internal/auth"
	"gitlab.com/system4us/issue-timeline/internal/provider"
)

// Sign-in with GitHub or GitLab (OAuth), and the sign-in methods the page offers. A flow: the page links to
// /api/oauth/{provider}/start, which sends the person to their host with a state and a PKCE challenge; the host sends
// them back to /api/oauth/{provider}/callback with a code, which is traded for their token. The state is also kept in a
// cookie, so a callback only completes in the browser that started it (no one can sign a victim into their account).

const (
	oauthPrefix = "/api/oauth/"
	oauthCookie = "it_oauth"
	// flowTTL is how long a started sign-in may take on the host's page.
	flowTTL  = 10 * time.Minute
	maxFlows = 1000
)

// flow is a sign-in started and not yet back.
type flow struct {
	app      auth.OAuthApp
	verifier string
	redirect string
	back     string // the page to return to
	at       time.Time
}

// flows are the sign-ins under way, by state.
type flows struct {
	mu sync.Mutex
	m  map[string]flow
}

func (f *flows) put(state string, fl flow) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.m == nil {
		f.m = map[string]flow{}
	}
	for k, v := range f.m { // the expired ones go; and a flood does not grow it without end
		if time.Since(v.at) > flowTTL || len(f.m) >= maxFlows {
			delete(f.m, k)
		}
	}
	f.m[state] = fl
}

// take returns the flow of state once (a code is traded once).
func (f *flows) take(state string) (flow, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fl, ok := f.m[state]
	delete(f.m, state)
	if !ok || time.Since(fl.at) > flowTTL {
		return flow{}, false
	}
	return fl, true
}

// oauthApp is the registered app of a provider, when the server has one and either a configured repo is on its host or
// no repos are configured for that provider yet.
func (s *server) oauthApp(prov string) (auth.OAuthApp, bool) {
	origins := s.app.SignInOrigins(prov)
	var a auth.OAuthApp
	var ok bool
	switch prov {
	case "github":
		a, ok = auth.OAuthGitHub(s.env)
	case "gitlab":
		a, ok = auth.OAuthGitLab(s.env, origins)
	}
	if !ok {
		return auth.OAuthApp{}, false
	}
	if len(origins) > 0 && !slices.Contains(origins, a.Origin) {
		return auth.OAuthApp{}, false
	}
	return a, true
}

// publicBase is the address the hosts send people back to: PUBLIC_URL, or the scheme and Host of the request (https
// when it came over TLS or a proxy says so).
func (s *server) publicBase(r *http.Request) string {
	if p := strings.TrimRight(s.env("PUBLIC_URL"), "/"); p != "" {
		return p
	}
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// backTo is the page to return to after the sign-in: a path of this site, never another site ("//x", "/\x").
func backTo(v string) string {
	if !strings.HasPrefix(v, "/") || strings.HasPrefix(v, "//") || strings.HasPrefix(v, "/\\") {
		return "/"
	}
	return v
}

// oauthStart sends the person to their host's consent page.
func (s *server) oauthStart(w http.ResponseWriter, r *http.Request) {
	prov := r.PathValue("provider")
	a, ok := s.oauthApp(prov)
	if !ok {
		writeError(w, http.StatusNotFound, "sign-in with "+prov+" is not set up on this server")
		return
	}
	state, verifier := auth.Randomness(32), auth.Randomness(48)
	redirect := s.publicBase(r) + oauthPrefix + prov + "/callback"
	s.flows.put(state, flow{app: a, verifier: verifier, redirect: redirect, back: backTo(r.URL.Query().Get("return")), at: time.Now()})
	http.SetCookie(w, &http.Cookie{Name: oauthCookie, Value: state, Path: oauthPrefix, MaxAge: int(flowTTL.Seconds()),
		HttpOnly: true, Secure: auth.WantsSecureCookie(r), SameSite: http.SameSiteLaxMode})
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, a.AuthorizeLink(state, verifier, redirect), http.StatusFound)
}

// oauthCallback takes the person back from their host: the code becomes their token and a session. Whatever goes wrong
// sends them to the page with ?signin_error=..., where the sign-in dialog says it.
func (s *server) oauthCallback(w http.ResponseWriter, r *http.Request) {
	prov := r.PathValue("provider")
	q := r.URL.Query()
	clear := &http.Cookie{Name: oauthCookie, Value: "", Path: oauthPrefix, MaxAge: -1, HttpOnly: true, Secure: auth.WantsSecureCookie(r), SameSite: http.SameSiteLaxMode}
	http.SetCookie(w, clear)
	w.Header().Set("Cache-Control", "no-store")
	fail := func(back, msg string) {
		http.Redirect(w, r, back+sep(back)+"signin_error="+url.QueryEscape(msg), http.StatusFound)
	}
	state := q.Get("state")
	c, err := r.Cookie(oauthCookie)
	if state == "" || err != nil || c.Value != state {
		fail("/", "the sign-in was not started in this browser, or took too long: try again")
		return
	}
	fl, ok := s.flows.take(state)
	if !ok || fl.app.Provider != prov {
		fail("/", "the sign-in took too long: try again")
		return
	}
	if e := q.Get("error"); e != "" {
		msg := q.Get("error_description")
		if msg == "" {
			msg = e
		}
		fail(fl.back, prov+" did not sign you in: "+msg)
		return
	}
	tok, err := fl.app.Exchange(r.Context(), nil, q.Get("code"), fl.verifier, fl.redirect)
	if err != nil {
		fail(fl.back, err.Error())
		return
	}
	var user provider.User
	if acc, ok := s.app.Accounts(prov, fl.app.Origin); ok {
		user, err = acc.WhoAmI(r.Context(), tok.Access)
	} else if len(s.app.SignInOrigins(prov)) == 0 {
		user, err = s.app.WhoAmI(r.Context(), prov, fl.app.Origin, tok.Access)
	} else {
		fail(fl.back, "no repo of this server is on that host any more")
		return
	}
	if err != nil {
		fail(fl.back, err.Error())
		return
	}
	var grant *auth.Grant
	if !tok.Expires.IsZero() {
		grant = &auth.Grant{App: fl.app, Redirect: fl.redirect, Refresh: tok.Refresh, Expires: tok.Expires}
	}
	// a new sign-in replaces the old session, never adds to it
	s.sessions.Drop(auth.SessionIDOf(r))
	id := s.sessions.CreateWith(prov, fl.app.Origin, tok.Access, user, grant)
	w.Header().Add("Set-Cookie", auth.CookieFor(id, auth.WantsSecureCookie(r)))
	http.Redirect(w, r, fl.back, http.StatusFound)
}

// sep is the separator before a new query parameter of a path.
func sep(path string) string {
	if strings.Contains(path, "?") {
		return "&"
	}
	return "?"
}

// signInMethod is one way to sign in that this server offers.
type signInMethod struct {
	Provider string `json:"provider"` // github, gitlab or redmine
	Origin   string `json:"origin"`   // "github", or the instance's URL
	OAuth    bool   `json:"oauth"`    // the host's own sign-in page (GET /api/oauth/{provider}/start)
	Token    bool   `json:"token"`    // a pasted personal access token (GitHub, GitLab) or API key (Redmine)
}

// signInOptions lists the ways to sign in: a host of each configured repo, or an OAuth app when no repos are configured
// for that provider yet.
func (s *server) signInOptions(w http.ResponseWriter, r *http.Request) {
	out := []signInMethod{}
	for _, prov := range []string{"github", "gitlab", "redmine"} {
		app, hasApp := s.oauthApp(prov)
		origins := s.app.SignInOrigins(prov)
		if len(origins) == 0 && hasApp {
			out = append(out, signInMethod{Provider: prov, Origin: app.Origin, OAuth: true, Token: false})
			continue
		}
		for _, origin := range origins {
			if _, ok := s.app.Accounts(prov, origin); !ok {
				continue
			}
			out = append(out, signInMethod{Provider: prov, Origin: origin, OAuth: hasApp && app.Origin == origin, Token: true})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"methods": out})
}

// renewed is the session with its OAuth token renewed when it is about to expire. One renewal at a time (GitLab rotates
// the refresh token, so two at once would spend it twice); a renewal that fails ends the session (nil): the person signs
// in again.
func (s *server) renewed(ctx context.Context, sess *auth.Session) *auth.Session {
	if sess == nil || !sess.Grant.Due(time.Now()) {
		return sess
	}
	s.renewMu.Lock()
	defer s.renewMu.Unlock()
	cur := s.sessions.Get(sess.ID)
	if cur == nil || !cur.Grant.Due(time.Now()) {
		return cur // gone, or renewed by the request before this one
	}
	g := cur.Grant
	if g.Refresh == "" {
		s.sessions.Drop(cur.ID)
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	tok, err := g.App.Renew(ctx, nil, g.Refresh, g.Redirect)
	if err != nil {
		s.log("httpapi: renewing the %s sign-in of %s: %v", cur.Provider, cur.User.Login, err)
		s.sessions.Drop(cur.ID)
		return nil
	}
	next := &auth.Grant{App: g.App, Redirect: g.Redirect, Refresh: tok.Refresh, Expires: tok.Expires}
	if next.Refresh == "" {
		next.Refresh = g.Refresh // a host that does not rotate it
	}
	if tok.Expires.IsZero() {
		next = nil
	}
	return s.sessions.Renew(cur.ID, tok.Access, next)
}
