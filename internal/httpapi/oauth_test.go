package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"gitlab.com/system4us/issue-timeline/internal/app"
	"gitlab.com/system4us/issue-timeline/internal/auth"
	"gitlab.com/system4us/issue-timeline/internal/credentials"
	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/provider"
)

// oauthGitLab is a GitLab instance for the OAuth tests: its token endpoint gives at1 for the code "good" (expiring in 30
// s, so the next request renews it) and at2 for the refresh token rt1. It records the grant types it was asked for.
type oauthGitLab struct {
	mu     sync.Mutex
	grants []string
}

func (g *oauthGitLab) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	f, _ := url.ParseQuery(string(b))
	g.mu.Lock()
	g.grants = append(g.grants, f.Get("grant_type"))
	g.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.URL.Path != "/oauth/token" || f.Get("client_secret") != "sec":
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"invalid_client"}`)
	case f.Get("grant_type") == "authorization_code" && f.Get("code") == "good" && f.Get("code_verifier") != "":
		_, _ = io.WriteString(w, `{"access_token":"at1","refresh_token":"rt1","expires_in":30}`)
	case f.Get("grant_type") == "refresh_token" && f.Get("refresh_token") == "rt1":
		_, _ = io.WriteString(w, `{"access_token":"at2","refresh_token":"rt2","expires_in":7200}`)
	default:
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"invalid_grant","error_description":"The provided authorization grant is invalid"}`)
	}
}

func (g *oauthGitLab) asked() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.grants...)
}

// gitlabAccounts is the repo's host: it knows the person by the two tokens the instance gives.
type gitlabAccounts struct {
	*fakeCode
	tokens map[string]bool
}

func (h *gitlabAccounts) WhoAmI(_ context.Context, token string) (provider.User, error) {
	if !h.tokens[token] {
		return provider.User{}, provider.Errorf(401, "that token is not valid on GitLab")
	}
	return provider.User{ID: 9, Login: "ann", Name: "Ann"}, nil
}

func (h *gitlabAccounts) RoleOf(context.Context, provider.Cred, provider.User) (provider.Role, error) {
	return provider.RoleViewer, nil
}

func (h *gitlabAccounts) VisibilityOf(context.Context, provider.Cred, provider.User, provider.Role, []*model.Record) (provider.Visibility, error) {
	return provider.Visibility{}, nil
}

// noRedirects is a client that shows each redirect instead of following it.
var noRedirects = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func get(t *testing.T, u string, cookies ...*http.Cookie) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.9") // a browser elsewhere: not this machine
	for _, c := range cookies {
		req.AddCookie(c)
	}
	res, err := noRedirects.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func cookieOf(res *http.Response, name string) *http.Cookie {
	for _, c := range res.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func TestSignInWithGitLabThroughItsOwnPage(t *testing.T) {
	gl := &oauthGitLab{}
	instance := httptest.NewServer(gl)
	defer instance.Close()
	dir := t.TempDir()
	host := &gitlabAccounts{
		fakeCode: &fakeCode{&fakeHost{key: "gitlab:" + instance.URL + "/g/p", prov: "gitlab", origin: instance.URL}},
		tokens:   map[string]bool{"at1": true, "at2": true},
	}
	a, err := app.New(app.Config{
		StoreDir: dir, PollInterval: time.Hour, Env: func(string) string { return "" },
		Secrets: &credentials.Store{Dir: dir, Env: func(string) string { return "" }, Keyring: memKeyring{}},
		Log:     t.Logf, Hosts: func(string) (provider.Host, error) { return host, nil },
		OwnToken: func(context.Context, string) (string, error) { return "local-token", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Stop)
	if _, _, err := a.AddRepo(context.Background(), "gitlab", instance.URL+"/g/p", "read"); err != nil {
		t.Fatal(err)
	}
	o := opts(nil)
	o.Env = func(k string) string {
		return map[string]string{"GITLAB_OAUTH_CLIENT_ID": "cid", "GITLAB_OAUTH_CLIENT_SECRET": "sec"}[k]
	}
	srv := httptest.NewServer(New(a, o))
	defer srv.Close()

	// the page learns it can send people to GitLab
	res := get(t, srv.URL+"/api/sign-in-options")
	var opts struct{ Methods []signInMethod }
	_ = json.NewDecoder(res.Body).Decode(&opts)
	res.Body.Close()
	if len(opts.Methods) != 1 || opts.Methods[0] != (signInMethod{Provider: "gitlab", Origin: instance.URL, OAuth: true, Token: true}) {
		t.Fatalf("methods: %+v", opts.Methods)
	}

	// start: to GitLab's consent page, with a state that is also a cookie
	res = get(t, srv.URL+"/api/oauth/gitlab/start?return=/analysis")
	res.Body.Close()
	loc, _ := url.Parse(res.Header.Get("Location"))
	flowCookie := cookieOf(res, oauthCookie)
	if res.StatusCode != http.StatusFound || !strings.HasPrefix(loc.String(), instance.URL+"/oauth/authorize?") || flowCookie == nil {
		t.Fatalf("start: %d %s", res.StatusCode, loc)
	}
	state := loc.Query().Get("state")
	if flowCookie.Value != state || loc.Query().Get("redirect_uri") != srv.URL+"/api/oauth/gitlab/callback" || loc.Query().Get("scope") != "api" {
		t.Fatalf("the link: %s (cookie %q)", loc, flowCookie.Value)
	}
	callback := srv.URL + "/api/oauth/gitlab/callback?code=good&state=" + url.QueryEscape(state)

	// a callback in another browser (no flow cookie) is refused, and does not spend the flow
	res = get(t, callback)
	res.Body.Close()
	if !strings.Contains(res.Header.Get("Location"), "signin_error=") || cookieOf(res, auth.SessionCookie) != nil {
		t.Fatalf("a callback without the flow's cookie: %s", res.Header.Get("Location"))
	}

	// the right browser: a session, and back to the page it started from
	res = get(t, callback, flowCookie)
	res.Body.Close()
	session := cookieOf(res, auth.SessionCookie)
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/analysis" || session == nil {
		t.Fatalf("callback: %d %s", res.StatusCode, res.Header.Get("Location"))
	}

	// the token expires in 30 s: the next request renews it, and the person is still signed in
	res = get(t, srv.URL+"/api/me", session)
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.Contains(string(body), `"login":"ann"`) {
		t.Fatalf("me: %d %s", res.StatusCode, body)
	}
	if got := strings.Join(gl.asked(), ","); got != "authorization_code,refresh_token" {
		t.Fatalf("the code was traded once, then the token renewed once: %s", got)
	}

	// the same callback again (a replay) does not sign anyone in
	res = get(t, callback, flowCookie)
	res.Body.Close()
	if !strings.Contains(res.Header.Get("Location"), "signin_error=") {
		t.Fatalf("a replayed callback: %s", res.Header.Get("Location"))
	}
}

func TestTheReturnPageStaysOnThisSite(t *testing.T) {
	for in, want := range map[string]string{"/analysis?x=1": "/analysis?x=1", "": "/", "https://evil.example": "/", "//evil.example": "/", `/\evil.example`: "/"} {
		if got := backTo(in); got != want {
			t.Fatalf("backTo(%q) = %q, want %q", in, got, want)
		}
	}
}
