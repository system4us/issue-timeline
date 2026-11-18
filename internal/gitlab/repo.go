// Package gitlab is the GitLab host: the sync of issues and merge requests, edits, commits, CI, the work-item hierarchy,
// the person's role and what they may see, and the server's own token (environment or the glab session).
package gitlab

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/provider"
)

// Options are the parts of a Repo that come from the server: where the token comes from, the HTTP client, and how a CI
// run is categorized (ci.Categorize, which the server passes in).
type Options struct {
	// Token is the server's own token for a host. Nil means TokenSource().
	Token func(ctx context.Context, host string) (string, error)
	// HTTP is the client every request goes through. Nil means a plain client; each request has its own 30 s deadline.
	HTTP *http.Client
	// Categorize gives the category of a CI run ("ci", "dependabot", "pages", "other"). It is required.
	Categorize func(model.CIRun) string
}

// Repo is one GitLab project. It holds only what does not change: the key, the origin, the API base and the options.
// Every operation builds its own client, so there is no state to share between requests.
type Repo struct {
	key        string
	origin     string
	host       string
	project    string // decoded: group/subgroup/project
	projectEnc string // escaped as one path segment, as the API wants it
	apiBase    string
	token      func(ctx context.Context, host string) (string, error)
	http       *http.Client
	categorize func(model.CIRun) string
}

var (
	_ provider.Host       = (*Repo)(nil)
	_ provider.CodeHost   = (*Repo)(nil)
	_ provider.Editor     = (*Repo)(nil)
	_ provider.PullEditor = (*Repo)(nil)
	_ provider.Accounts   = (*Repo)(nil)
)

var hostPattern = regexp.MustCompile(`^[\w.-]+(?::\d+)?$`)

const invalidRepo = "Invalid GitLab repository. Use group/project or the full URL."

// ParseRepo reads a repo key: "gitlab:https://host/group/project", "https://host/group/project", or "group/project" (on
// gitlab.com). It returns the origin (scheme and host), the host (with its port), the decoded project path, and the API base.
func ParseRepo(key string) (origin, host, project, apiBase string, err error) {
	value := strings.TrimPrefix(key, "gitlab:")
	raw := value
	if !strings.Contains(value, "://") {
		raw = "https://gitlab.com/" + value
	}
	u, perr := url.Parse(raw)
	if perr != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", "", "", "", errors.New(invalidRepo)
	}
	host = strings.ToLower(u.Host)
	project = strings.Trim(u.Path, "/")
	if host == "" || project == "" || strings.HasPrefix(host, "-") || !hostPattern.MatchString(host) {
		return "", "", "", "", errors.New(invalidRepo)
	}
	for _, seg := range strings.Split(project, "/") {
		if seg == "" {
			return "", "", "", "", errors.New(invalidRepo)
		}
	}
	origin = u.Scheme + "://" + host
	return origin, host, project, origin + "/api/v4", nil
}

// New returns the Repo of a key. Categorize is required.
func New(key string, o Options) (*Repo, error) {
	origin, host, project, apiBase, err := ParseRepo(key)
	if err != nil {
		return nil, err
	}
	if o.Categorize == nil {
		return nil, errors.New("gitlab: Options.Categorize is required")
	}
	r := &Repo{
		key: "gitlab:" + origin + "/" + project, origin: origin, host: host, project: project,
		projectEnc: url.PathEscape(project), apiBase: apiBase, token: o.Token, http: o.HTTP, categorize: o.Categorize,
	}
	if r.token == nil {
		r.token = TokenSource()
	}
	r.http = sameOrigin(r.http)
	return r, nil
}

// Key is the repo key the stores keep: "gitlab:<origin>/<project>".
func (r *Repo) Key() string { return r.key }

// Provider is "gitlab".
func (r *Repo) Provider() string { return "gitlab" }

// Origin is the site's origin, which sessions are matched against.
func (r *Repo) Origin() string { return r.origin }

// serverToken is the server's own token for the host.
func (r *Repo) serverToken(ctx context.Context) (string, error) {
	tok, err := r.token(ctx, r.host)
	if err != nil {
		return "", fmt.Errorf("reading the token for %s: %w", r.host, err)
	}
	return tok, nil
}

// credToken is the token an operation acts with: the person's when they signed in, the server's otherwise.
func (r *Repo) credToken(ctx context.Context, cred provider.Cred) (string, error) {
	if cred.Token != "" {
		return cred.Token, nil
	}
	return r.serverToken(ctx)
}

// api is the API URL of a path (without a leading slash) and an optional query.
func (r *Repo) api(path string, q url.Values) string {
	u := r.apiBase + "/" + strings.TrimPrefix(path, "/")
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

// projectRoot is the path of the project itself.
func (r *Repo) projectRoot() string { return "projects/" + r.projectEnc }

// projectPath is the path of something in the project: "projects/<escaped project>/<rest>".
func (r *Repo) projectPath(rest string) string { return r.projectRoot() + "/" + rest }

// sameOrigin is the client a request goes through: a copy of c (or a plain one) that refuses a redirect to another site,
// since the request carries a credential. The copy keeps c's transport and timeout.
func sameOrigin(c *http.Client) *http.Client {
	if c == nil {
		c = &http.Client{}
	}
	cp := *c
	cp.CheckRedirect = provider.SameOriginRedirects
	return &cp
}
