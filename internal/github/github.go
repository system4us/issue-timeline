// Package github is the GitHub host: the sync of a repo's issues and pull requests (GraphQL), the reads and edits of
// one record (REST), commits, CI runs, merge methods, the metadata refresh and the roles people have in a repo.
package github

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/provider"
)

// Endpoints are the URLs of one GitHub host. github.com by default; GitHub Enterprise Server through GITHUB_HOST.
type Endpoints struct {
	Host    string
	REST    string
	GraphQL string
}

// EndpointsFromEnv reads the endpoints from GITHUB_HOST, GITHUB_API_URL and GITHUB_GRAPHQL_URL.
func EndpointsFromEnv() Endpoints { return endpointsFrom(os.Getenv) }

// EndpointsFrom reads the endpoints from getenv (GITHUB_HOST, GITHUB_API_URL and GITHUB_GRAPHQL_URL), falling back to
// os.Getenv when nil.
func EndpointsFrom(getenv func(string) string) Endpoints {
	if getenv == nil {
		getenv = os.Getenv
	}
	return endpointsFrom(getenv)
}

// endpointsFrom is EndpointsFromEnv with the environment passed in, so tests need not set it.
func endpointsFrom(getenv func(string) string) Endpoints {
	host := getenv("GITHUB_HOST")
	host = strings.TrimPrefix(host, "https://")
	host = strings.TrimPrefix(host, "http://")
	host = strings.TrimRight(host, "/")
	enterprise := host != "" && host != "github.com"
	rest := getenv("GITHUB_API_URL")
	if rest == "" {
		rest = "https://api.github.com"
		if enterprise {
			rest = "https://" + host + "/api/v3"
		}
	}
	graphql := getenv("GITHUB_GRAPHQL_URL")
	if graphql == "" {
		graphql = "https://api.github.com/graphql"
		if enterprise {
			graphql = "https://" + host + "/api/graphql"
		}
	}
	if host == "" {
		host = "github.com"
	}
	return Endpoints{Host: host, REST: strings.TrimRight(rest, "/"), GraphQL: graphql}
}

// Options is what a GitHub repo needs from the server.
type Options struct {
	// Token is the server's own token (GITHUB_TOKEN, or the gh CLI's; see TokenSource). Reads and syncs use it.
	Token func(ctx context.Context) (string, error)
	// HTTP is the client the calls go through; nil means a default one. Each call has its own timeout.
	HTTP *http.Client
	// Endpoints are the host's URLs; empty fields are read from the environment.
	Endpoints Endpoints
	// ClassifyMerge says how a merged pull landed (commits.ClassifyGitHubMerge).
	ClassifyMerge func(parents int, title, prTitle string, number int, prCommitTitles []string) string
	// Categorize says what kind of CI run a run is (ci.Categorize).
	Categorize func(model.CIRun) string

	// pause waits before a retry; tests replace it so they do not wait.
	pause func(ctx context.Context, d time.Duration) error
}

// resolved fills the endpoints the caller left empty from the environment.
func (o Options) resolved() Options {
	if o.Endpoints.Host == "" || o.Endpoints.REST == "" || o.Endpoints.GraphQL == "" {
		env := EndpointsFromEnv()
		if o.Endpoints.Host == "" {
			o.Endpoints.Host = env.Host
		}
		if o.Endpoints.REST == "" {
			o.Endpoints.REST = env.REST
		}
		if o.Endpoints.GraphQL == "" {
			o.Endpoints.GraphQL = env.GraphQL
		}
	}
	return o
}

// Repo is one GitHub repo. It implements provider.Host, MetaRefresher, CodeHost, Editor, PullEditor and Accounts.
type Repo struct {
	owner, name string
	o           Options
	api         *api
}

var (
	_ provider.Host          = (*Repo)(nil)
	_ provider.MetaRefresher = (*Repo)(nil)
	_ provider.CodeHost      = (*Repo)(nil)
	_ provider.Editor        = (*Repo)(nil)
	_ provider.PullEditor    = (*Repo)(nil)
	_ provider.Accounts      = (*Repo)(nil)
)

// New is the repo owner/name on GitHub.
func New(owner, name string, o Options) *Repo {
	o = o.resolved()
	return &Repo{owner: owner, name: name, o: o, api: newAPI(o)}
}

// Key is "owner/name".
func (r *Repo) Key() string { return r.owner + "/" + r.name }

// Provider is "github".
func (r *Repo) Provider() string { return "github" }

// Origin is "github": one GitHub host per run (GITHUB_HOST).
func (r *Repo) Origin() string { return "github" }

// serverToken is the server's own token, from Options.Token.
func (r *Repo) serverToken(ctx context.Context) (string, error) {
	if r.o.Token == nil {
		return "", fmt.Errorf("no token source for %s", r.Key())
	}
	tok, err := r.o.Token(ctx)
	if err != nil {
		return "", err
	}
	if tok == "" {
		return "", fmt.Errorf("no token for %s", r.Key())
	}
	return tok, nil
}

// Identity is the login of the account the server's token belongs to: what the token really is, read at startup.
func Identity(ctx context.Context, o Options) (string, error) {
	o = o.resolved()
	if o.Token == nil {
		return "", fmt.Errorf("no token source")
	}
	tok, err := o.Token(ctx)
	if err != nil {
		return "", err
	}
	var data struct {
		Viewer struct {
			Login string `json:"login"`
		} `json:"viewer"`
	}
	if err := newAPI(o).graphqlInto(ctx, o.Endpoints.GraphQL, tok, "query { viewer { login } }", nil, &data); err != nil {
		return "", asProvider(err)
	}
	return data.Viewer.Login, nil
}

// Exists reports whether the repo is there and the server's token can read it.
func (r *Repo) Exists(ctx context.Context) error {
	return asProvider(r.exists(ctx))
}

func (r *Repo) exists(ctx context.Context) error {
	tok, err := r.serverToken(ctx)
	if err != nil {
		return err
	}
	var data struct {
		Repository *struct {
			ID string `json:"id"`
		} `json:"repository"`
	}
	q := "query($owner: String!, $name: String!) { repository(owner: $owner, name: $name) { id } }"
	if err := r.api.graphqlInto(ctx, r.o.Endpoints.GraphQL, tok, q, r.vars(), &data); err != nil {
		return fmt.Errorf("could not check %s on GitHub: %w", r.Key(), err)
	}
	if data.Repository == nil {
		return fmt.Errorf("%w: GitHub repository %s, or no access to it", provider.ErrNotFound, r.Key())
	}
	return nil
}

// vars are the owner and name, the variables of most queries.
func (r *Repo) vars() map[string]any {
	return map[string]any{"owner": r.owner, "name": r.name}
}

// Permissions are what the repo says the caller may do (GET /repos/{owner}/{name}, permissions).
type Permissions struct {
	Admin    bool `json:"admin"`
	Maintain bool `json:"maintain"`
	Push     bool `json:"push"`
	Triage   bool `json:"triage"`
	Pull     bool `json:"pull"`
}

// RoleFromPermissions maps GitHub's permissions to a role: admin is admin; maintain, push or triage is editor (triage can
// change issues); pull alone is viewer; nothing (or no permissions at all) is no role.
func RoleFromPermissions(p *Permissions) provider.Role {
	if p == nil {
		return provider.RoleNone
	}
	switch {
	case p.Admin:
		return provider.RoleAdmin
	case p.Maintain || p.Push || p.Triage:
		return provider.RoleEditor
	case p.Pull:
		return provider.RoleViewer
	}
	return provider.RoleNone
}

// WhoAmI is the account a token belongs to; a 401 when the host refuses it.
func (r *Repo) WhoAmI(ctx context.Context, token string) (provider.User, error) {
	u, err := whoAmI(ctx, r.api, r.o.Endpoints.REST, token)
	return u, asProvider(err)
}

func whoAmI(ctx context.Context, a *api, rest, token string) (provider.User, error) {
	var u restUser
	if err := a.rest(ctx, "GET", rest+"/user", token, nil, &u); err != nil {
		return provider.User{}, err
	}
	name := u.Name
	if name == "" {
		name = u.Login
	}
	return provider.User{ID: int(u.ID), Login: u.Login, Name: name, Admin: false}, nil
}

// RoleOf is the person's role in this repo, read with their own token: a repo they cannot see (404, 403, 401) is no role.
func (r *Repo) RoleOf(ctx context.Context, cred provider.Cred, user provider.User) (provider.Role, error) {
	var repo struct {
		Permissions *Permissions `json:"permissions"`
	}
	err := r.api.rest(ctx, "GET", r.repoURL(), cred.Token, nil, &repo)
	if err != nil {
		var he *hostError
		if asHostError(err, &he) && (he.Status == 401 || he.Status == 403 || he.Status == 404) {
			return provider.RoleNone, nil
		}
		return provider.RoleNone, asProvider(err)
	}
	return RoleFromPermissions(repo.Permissions), nil
}

// VisibilityOf is the zero Visibility: GitHub has no per-issue privacy, so a role in the repo reads all of it.
func (r *Repo) VisibilityOf(ctx context.Context, cred provider.Cred, user provider.User, role provider.Role, issues []*model.Record) (provider.Visibility, error) {
	return provider.Visibility{}, nil
}

// repoURL is the REST URL of the repo.
func (r *Repo) repoURL() string {
	return fmt.Sprintf("%s/repos/%s/%s", r.o.Endpoints.REST, pathSeg(r.owner), pathSeg(r.name))
}
