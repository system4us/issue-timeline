package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Sign-in with GitHub or GitLab through OAuth: the person is sent to their host, says yes there, and comes back with a
// code that this server trades for a token of theirs. On GitHub the app can be a GitHub App (recommended: its token
// is limited to the app's permissions and to the repos it is installed on) or an OAuth App; both use the same flow.
// On GitLab it is an application (user, group or instance). The token then works as a pasted one does.

// OAuthApp is one host's registered app, as the server is configured with it.
type OAuthApp struct {
	Provider     string // "github" or "gitlab"
	Origin       string // the session's origin: "github" (one GitHub host per run) or the GitLab instance's URL
	AuthorizeURL string
	TokenURL     string
	ClientID     string
	ClientSecret string
	Scopes       string // space separated; a GitHub App ignores them (its permissions decide)
}

// OAuthGitHub is the GitHub app of the environment: GITHUB_OAUTH_CLIENT_ID and GITHUB_OAUTH_CLIENT_SECRET, on
// GITHUB_HOST (github.com by default). GITHUB_OAUTH_SCOPES are an OAuth App's scopes ("repo": private repos and edits).
func OAuthGitHub(env func(string) string) (OAuthApp, bool) {
	id, secret := env("GITHUB_OAUTH_CLIENT_ID"), env("GITHUB_OAUTH_CLIENT_SECRET")
	if id == "" || secret == "" {
		return OAuthApp{}, false
	}
	host := strings.TrimRight(strings.TrimPrefix(strings.TrimPrefix(env("GITHUB_HOST"), "https://"), "http://"), "/")
	if host == "" {
		host = "github.com"
	}
	scopes := env("GITHUB_OAUTH_SCOPES")
	if scopes == "" {
		scopes = "repo"
	}
	base := "https://" + host
	return OAuthApp{Provider: "github", Origin: "github", AuthorizeURL: base + "/login/oauth/authorize",
		TokenURL: base + "/login/oauth/access_token", ClientID: id, ClientSecret: secret, Scopes: scopes}, true
}

// OAuthGitLab is the GitLab application of the environment: GITLAB_OAUTH_CLIENT_ID and GITLAB_OAUTH_CLIENT_SECRET, on
// GITLAB_OAUTH_URL, or else on the one GitLab instance the configured repos are on (gitlab.com when there is none).
// GITLAB_OAUTH_SCOPES default to "api" (reading and editing).
func OAuthGitLab(env func(string) string, origins []string) (OAuthApp, bool) {
	id, secret := env("GITLAB_OAUTH_CLIENT_ID"), env("GITLAB_OAUTH_CLIENT_SECRET")
	if id == "" || secret == "" {
		return OAuthApp{}, false
	}
	origin := strings.TrimRight(env("GITLAB_OAUTH_URL"), "/")
	switch {
	case origin != "":
	case len(origins) == 1:
		origin = origins[0]
	default:
		origin = "https://gitlab.com"
	}
	scopes := env("GITLAB_OAUTH_SCOPES")
	if scopes == "" {
		scopes = "api"
	}
	return OAuthApp{Provider: "gitlab", Origin: origin, AuthorizeURL: origin + "/oauth/authorize",
		TokenURL: origin + "/oauth/token", ClientID: id, ClientSecret: secret, Scopes: scopes}, true
}

// Randomness is a URL-safe random text of n bytes (state, PKCE verifier). It panics without a random source, as
// Create does.
func Randomness(n int) string {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		panic(fmt.Sprintf("no random source: %v", err))
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// Challenge is the PKCE S256 challenge of a verifier.
func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// AuthorizeLink is where the person is sent: the host's consent page, which comes back to redirect with a code.
func (a OAuthApp) AuthorizeLink(state, verifier, redirect string) string {
	q := url.Values{
		"client_id":             {a.ClientID},
		"redirect_uri":          {redirect},
		"response_type":         {"code"},
		"state":                 {state},
		"code_challenge":        {Challenge(verifier)},
		"code_challenge_method": {"S256"},
	}
	if a.Scopes != "" {
		q.Set("scope", a.Scopes)
	}
	return a.AuthorizeURL + "?" + q.Encode()
}

// Grant is what an OAuth sign-in keeps besides the token: how to renew it before it expires. A GitLab token lasts two
// hours, a GitHub App's eight (when the app's tokens expire); an OAuth App's does not expire (Expires is zero).
type Grant struct {
	App      OAuthApp
	Redirect string // the redirect the code came to (GitLab asks for it again on a renewal)
	Refresh  string
	Expires  time.Time
}

// Due reports whether the token should be renewed now: it expires within a minute.
func (g *Grant) Due(now time.Time) bool {
	return g != nil && !g.Expires.IsZero() && now.After(g.Expires.Add(-time.Minute))
}

// OAuthToken is a token the host gave.
type OAuthToken struct {
	Access  string
	Refresh string
	Expires time.Time // zero: it does not expire
}

// Exchange trades the code the host sent back for the person's token.
func (a OAuthApp) Exchange(ctx context.Context, client *http.Client, code, verifier, redirect string) (OAuthToken, error) {
	return a.token(ctx, client, url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {verifier}, "redirect_uri": {redirect},
	})
}

// Renew trades a refresh token for a new token (GitLab rotates the refresh token too: the old one stops working).
func (a OAuthApp) Renew(ctx context.Context, client *http.Client, refresh, redirect string) (OAuthToken, error) {
	return a.token(ctx, client, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "redirect_uri": {redirect}})
}

// token posts to the host's token endpoint. GitHub answers an error with 200 and an "error" field; GitLab with a 4xx.
func (a OAuthApp) token(ctx context.Context, client *http.Client, form url.Values) (OAuthToken, error) {
	form.Set("client_id", a.ClientID)
	form.Set("client_secret", a.ClientSecret)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return OAuthToken{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if client == nil {
		client = http.DefaultClient
	}
	// the secret goes to the token endpoint only, never through a redirect to another site
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := c.Do(req)
	if err != nil {
		return OAuthToken{}, fmt.Errorf("%s did not answer the sign-in: %w", a.Provider, err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Error        string `json:"error"`
		Description  string `json:"error_description"`
	}
	_ = json.Unmarshal(body, &out)
	if out.Error != "" || res.StatusCode >= 400 || out.AccessToken == "" {
		msg := out.Description
		if msg == "" {
			msg = out.Error
		}
		if msg == "" {
			msg = fmt.Sprintf("status %d", res.StatusCode)
		}
		return OAuthToken{}, fmt.Errorf("%s refused the sign-in: %s", a.Provider, msg)
	}
	t := OAuthToken{Access: out.AccessToken, Refresh: out.RefreshToken}
	if out.ExpiresIn > 0 {
		t.Expires = time.Now().Add(time.Duration(out.ExpiresIn) * time.Second)
	}
	return t, nil
}
