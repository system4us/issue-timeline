package auth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"gitlab.com/system4us/issue-timeline/internal/provider"
)

func envOf(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }

func TestTheAppsComeFromTheEnvironment(t *testing.T) {
	if _, ok := OAuthGitHub(envOf(nil)); ok {
		t.Fatal("no client id: no GitHub sign-in")
	}
	gh, ok := OAuthGitHub(envOf(map[string]string{"GITHUB_OAUTH_CLIENT_ID": "id", "GITHUB_OAUTH_CLIENT_SECRET": "s"}))
	if !ok || gh.AuthorizeURL != "https://github.com/login/oauth/authorize" || gh.TokenURL != "https://github.com/login/oauth/access_token" || gh.Scopes != "repo" || gh.Origin != "github" {
		t.Fatalf("github.com: %+v", gh)
	}
	ghe, _ := OAuthGitHub(envOf(map[string]string{"GITHUB_OAUTH_CLIENT_ID": "id", "GITHUB_OAUTH_CLIENT_SECRET": "s", "GITHUB_HOST": "https://ghe.example.com/"}))
	if ghe.AuthorizeURL != "https://ghe.example.com/login/oauth/authorize" {
		t.Fatalf("Enterprise Server: %+v", ghe)
	}
	env := map[string]string{"GITLAB_OAUTH_CLIENT_ID": "id", "GITLAB_OAUTH_CLIENT_SECRET": "s"}
	if gl, _ := OAuthGitLab(envOf(env), nil); gl.Origin != "https://gitlab.com" || gl.TokenURL != "https://gitlab.com/oauth/token" || gl.Scopes != "api" {
		t.Fatalf("gitlab.com by default: %+v", gl)
	}
	if gl, _ := OAuthGitLab(envOf(env), []string{"https://git.example.com"}); gl.Origin != "https://git.example.com" {
		t.Fatalf("the one instance the repos are on: %+v", gl)
	}
	env["GITLAB_OAUTH_URL"] = "https://other.example.com/"
	if gl, _ := OAuthGitLab(envOf(env), []string{"https://git.example.com", "https://gitlab.com"}); gl.Origin != "https://other.example.com" {
		t.Fatalf("GITLAB_OAUTH_URL wins: %+v", gl)
	}
}

func TestTheAuthorizeLinkCarriesStateAndAPKCEChallenge(t *testing.T) {
	a := OAuthApp{AuthorizeURL: "https://gitlab.com/oauth/authorize", ClientID: "cid", Scopes: "api"}
	u, _ := url.Parse(a.AuthorizeLink("st", "verifier", "https://it.example.com/api/oauth/gitlab/callback"))
	q := u.Query()
	if q.Get("client_id") != "cid" || q.Get("state") != "st" || q.Get("response_type") != "code" || q.Get("scope") != "api" ||
		q.Get("redirect_uri") != "https://it.example.com/api/oauth/gitlab/callback" || q.Get("code_challenge_method") != "S256" ||
		q.Get("code_challenge") != Challenge("verifier") || q.Get("code_challenge") == "verifier" {
		t.Fatalf("link: %s", u)
	}
	if len(Randomness(32)) < 40 || Randomness(32) == Randomness(32) {
		t.Fatal("random texts are long and differ")
	}
}

func TestTheCodeIsTradedForATokenAndRenewed(t *testing.T) {
	var forms []url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f, _ := url.ParseQuery(string(b))
		forms = append(forms, f)
		if r.Header.Get("Accept") != "application/json" {
			t.Errorf("asks for JSON")
		}
		w.Header().Set("Content-Type", "application/json")
		switch f.Get("grant_type") {
		case "authorization_code":
			if f.Get("code") == "bad" {
				// GitHub's way: 200 with an error
				_, _ = io.WriteString(w, `{"error":"bad_verification_code","error_description":"The code passed is incorrect or expired."}`)
				return
			}
			_, _ = io.WriteString(w, `{"access_token":"at1","refresh_token":"rt1","expires_in":7200,"token_type":"Bearer"}`)
		case "refresh_token":
			if f.Get("refresh_token") != "rt1" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":"invalid_grant"}`)
				return
			}
			_, _ = io.WriteString(w, `{"access_token":"at2","refresh_token":"rt2","expires_in":7200}`)
		}
	}))
	defer srv.Close()
	a := OAuthApp{Provider: "gitlab", TokenURL: srv.URL, ClientID: "cid", ClientSecret: "sec"}
	ctx := context.Background()
	tok, err := a.Exchange(ctx, nil, "good", "ver", "https://it/cb")
	if err != nil || tok.Access != "at1" || tok.Refresh != "rt1" || time.Until(tok.Expires) < time.Hour {
		t.Fatalf("exchange: %+v %v", tok, err)
	}
	f := forms[0]
	if f.Get("client_id") != "cid" || f.Get("client_secret") != "sec" || f.Get("code_verifier") != "ver" || f.Get("redirect_uri") != "https://it/cb" {
		t.Fatalf("form: %v", f)
	}
	if _, err := a.Exchange(ctx, nil, "bad", "ver", "https://it/cb"); err == nil || !strings.Contains(err.Error(), "incorrect or expired") {
		t.Fatalf("an error in a 200 is an error: %v", err)
	}
	renewed, err := a.Renew(ctx, nil, "rt1", "https://it/cb")
	if err != nil || renewed.Access != "at2" || renewed.Refresh != "rt2" {
		t.Fatalf("renew: %+v %v", renewed, err)
	}
	if _, err := a.Renew(ctx, nil, "rt1-used", "https://it/cb"); err == nil {
		t.Fatal("a used refresh token is refused")
	}
}

func TestARenewedSessionKeepsItsIdAndPerson(t *testing.T) {
	ss := NewSessions(SessionOptions{Env: envOf(nil)})
	g := &Grant{Refresh: "rt1", Expires: time.Now().Add(time.Hour)}
	id := ss.CreateWith("gitlab", "https://gitlab.com", "at1", provider.User{ID: 7, Login: "ann"}, g)
	before := ss.Get(id)
	if before.Grant != g || before.Token != "at1" {
		t.Fatal("the grant is kept")
	}
	if g.Due(time.Now()) || !g.Due(time.Now().Add(time.Hour)) || (&Grant{}).Due(time.Now()) {
		t.Fatal("due a minute before it expires; never for a token that does not expire")
	}
	after := ss.Renew(id, "at2", &Grant{Refresh: "rt2"})
	if after == nil || after.ID != id || after.Token != "at2" || after.User.Login != "ann" || after.CreatedAt != before.CreatedAt {
		t.Fatalf("renewed: %+v", after)
	}
	if ss.Get(id).Token != "at2" || before.Token != "at1" {
		t.Fatal("the set holds the new one; the old value is untouched")
	}
	if ss.Renew("gone", "x", nil) != nil {
		t.Fatal("an unknown id renews nothing")
	}
}

func TestOnlyARevokedGrantIsFinal(t *testing.T) {
	status, body := http.StatusBadRequest, `{"error":"invalid_grant","error_description":"revoked"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()
	a := OAuthApp{Provider: "gitlab", TokenURL: srv.URL, ClientID: "cid", ClientSecret: "sec"}
	_, err := a.Renew(context.Background(), nil, "rt", "https://it/cb")
	if !GrantRevoked(err) {
		t.Fatalf("invalid_grant is final: %v", err)
	}
	status, body = http.StatusServiceUnavailable, `upstream down`
	_, err = a.Renew(context.Background(), nil, "rt", "https://it/cb")
	if err == nil || GrantRevoked(err) {
		t.Fatalf("a 503 is not final: %v", err)
	}
	status, body = http.StatusTooManyRequests, `{"error":"rate_limited"}`
	if _, err = a.Renew(context.Background(), nil, "rt", "https://it/cb"); err == nil || GrantRevoked(err) {
		t.Fatalf("a rate limit is not final: %v", err)
	}
	if GrantRevoked(errors.New("boom")) {
		t.Fatal("a plain error is not final")
	}
}
