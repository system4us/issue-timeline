// Package jira is the Jira Cloud tracker: a project's issues, their changelog (history), comments, links and parents,
// read through the REST API v3 with an email and an API token. Jira has no pull requests, commits or CI here, and no
// sign-in yet: a session never sees a Jira project. Keys are text ("PROJ-12"); the store keeps them as they are.
//
// Repo key: "jira:<site origin>/<PROJECT KEY>", e.g. "jira:https://acme.atlassian.net/PROJ".
package jira

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/provider"
)

const (
	keyPrefix = "jira:"
	// maxAttempts is how many times a request is tried when Jira is busy (429 or 5xx): the first try and 3 retries.
	maxAttempts  = 4
	maxRetryWait = 15 * time.Second
	// requestTimeout bounds one HTTP call.
	requestTimeout = 30 * time.Second
)

var projectRE = regexp.MustCompile(`^[A-Z][A-Z0-9_]+$`)

// ParseRepo reads a repo key ("jira:https://site/PROJ") into the site's origin and the project key. It fails on anything
// else.
func ParseRepo(key string) (origin, project string, err error) {
	if !strings.HasPrefix(key, keyPrefix) {
		return "", "", fmt.Errorf("not a Jira repo key: %s", key)
	}
	u, err := parseSiteURL(strings.TrimPrefix(key, keyPrefix))
	if err != nil {
		return "", "", err
	}
	project = lastSegment(u.Path)
	if !projectRE.MatchString(project) {
		return "", "", errors.New("Jira project key missing or invalid (for example PROJ)")
	}
	return originOf(u), project, nil
}

// KeyFromURL is the repo key of a Jira project, from a project URL ("https://site/jira/software/projects/PROJ/...") or
// from a site and a key ("https://site/PROJ").
func KeyFromURL(input string) (string, error) {
	u, err := parseSiteURL(input)
	if err != nil {
		return "", err
	}
	parts := nonEmpty(u.Path)
	var project string
	at := -1
	for i, p := range parts {
		if p == "projects" {
			at = i
			break
		}
	}
	switch {
	case at >= 0 && at+1 < len(parts):
		project = parts[at+1]
	case at < 0 && len(parts) > 0:
		project = parts[len(parts)-1]
	}
	project = strings.ToUpper(project)
	if project == "" {
		return "", errors.New("use the project URL: https://<site>.atlassian.net/jira/software/projects/<KEY>")
	}
	key := keyPrefix + originOf(u) + "/" + project
	if _, _, err := ParseRepo(key); err != nil {
		return "", err
	}
	return key, nil
}

func parseSiteURL(s string) (*url.URL, error) {
	u, err := url.Parse(s)
	if err != nil {
		return nil, fmt.Errorf("not a URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, errors.New("Jira site must be http or https")
	}
	if u.Host == "" {
		return nil, errors.New("Jira site has no host")
	}
	return u, nil
}

func originOf(u *url.URL) string {
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)
}

func nonEmpty(path string) []string {
	var out []string
	for _, p := range strings.Split(path, "/") {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func lastSegment(path string) string {
	parts := nonEmpty(path)
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}

// Options is what the server gives a Repo: Creds returns the email and the API token of this site (Basic auth), and HTTP
// is the client (default: 30 s timeout).
type Options struct {
	Creds func(ctx context.Context) (email, token string, err error)
	HTTP  *http.Client
}

// Repo is one Jira project. It implements provider.Host only.
type Repo struct {
	key        string
	origin     string
	projectKey string
	opt        Options
	http       *http.Client
	sleep      func(context.Context, time.Duration) error
}

var _ provider.Host = (*Repo)(nil)

// New is the Repo for a repo key.
func New(key string, o Options) (*Repo, error) {
	origin, projectKey, err := ParseRepo(key)
	if err != nil {
		return nil, err
	}
	h := sameOrigin(o.HTTP)
	if o.HTTP == nil {
		h.Timeout = requestTimeout
	}
	return &Repo{key: key, origin: origin, projectKey: projectKey, opt: o, http: h, sleep: sleep}, nil
}

// Key is the repo key.
func (r *Repo) Key() string { return r.key }

// Provider is "jira".
func (r *Repo) Provider() string { return "jira" }

// Origin is the site's origin.
func (r *Repo) Origin() string { return r.origin }

// Refresh is not supported: a Jira issue is read again with the sync.
func (r *Repo) Refresh(context.Context, string, *model.Record) (*model.Record, error) {
	return nil, provider.ErrNotSupported
}

// client is one site with one credential (Basic auth).
type client struct {
	origin string
	email  string
	token  string
	http   *http.Client
	// sleep waits between tries; the tests replace it so they run fast.
	sleep func(context.Context, time.Duration) error
}

// httpError is a Jira answer that is not a success.
type httpError struct {
	Status int
	Msg    string
}

func (e *httpError) Error() string { return e.Msg }

// get reads a Jira JSON resource with Basic auth into out, retrying rate limits and server errors.
func (c client) get(ctx context.Context, path string, query url.Values, out any) error {
	target := c.origin + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	auth := base64.StdEncoding.EncodeToString([]byte(c.email + ":" + c.token))
	for attempt := 1; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Basic "+auth)
		req.Header.Set("Accept", "application/json")
		resp, err := c.http.Do(req)
		if err != nil {
			return fmt.Errorf("Jira GET %s: %w", path, err)
		}
		data, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return fmt.Errorf("reading %s: %w", path, readErr)
		}
		// Jira is only read here (GET): a rate limit or a server error is safe to ask again.
		if (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500) && attempt < maxAttempts {
			if err := c.wait(ctx, retryWait(resp, attempt)); err != nil {
				return err
			}
			continue
		}
		switch {
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			return &httpError{Status: resp.StatusCode, Msg: fmt.Sprintf("Jira refused the credentials (HTTP %d) on %s: check the email and the API token, and the project's access", resp.StatusCode, path)}
		case resp.StatusCode == http.StatusNotFound:
			return &httpError{Status: resp.StatusCode, Msg: fmt.Sprintf("Jira: not found (%s)", path)}
		case resp.StatusCode < 200 || resp.StatusCode > 299:
			return &httpError{Status: resp.StatusCode, Msg: fmt.Sprintf("Jira HTTP %d on %s", resp.StatusCode, path)}
		}
		if out == nil || len(bytes.TrimSpace(data)) == 0 {
			return nil
		}
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("reading %s: %w", path, err)
		}
		return nil
	}
}

// wait sleeps between tries with the client's sleep.
func (c client) wait(ctx context.Context, d time.Duration) error {
	if c.sleep == nil {
		return sleep(ctx, d)
	}
	return c.sleep(ctx, d)
}

// retryWait is how long to wait before the next try: what Retry-After says (at most maxRetryWait). A missing or zero
// Retry-After waits the backoff of 2 s per attempt, as the Node server did.
func retryWait(resp *http.Response, attempt int) time.Duration {
	if v := resp.Header.Get("Retry-After"); v != "" {
		if secs, err := parseSeconds(v); err == nil && secs > 0 {
			return min(secs, maxRetryWait)
		}
	}
	return min(time.Duration(attempt*2)*time.Second, maxRetryWait)
}

func parseSeconds(v string) (time.Duration, error) {
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil || f < 0 {
		return 0, errors.New("not seconds")
	}
	return time.Duration(f * float64(time.Second)), nil
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func isNotFound(err error) bool {
	var he *httpError
	return errors.As(err, &he) && he.Status == http.StatusNotFound
}

// client returns the site's credentials from the server's options. Missing ones are an error.
func (r *Repo) client(ctx context.Context) (client, error) {
	if r.opt.Creds == nil {
		return client{}, errors.New("no Jira credentials")
	}
	email, token, err := r.opt.Creds(ctx)
	if err != nil {
		return client{}, err
	}
	if email == "" || token == "" {
		return client{}, errors.New("no Jira credentials: set the email and the API token for this site")
	}
	return client{origin: r.origin, email: email, token: token, http: r.http, sleep: r.sleep}, nil
}

// jiraProject is the project: its id, key and name.
type jiraProject struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`
}

func (r *Repo) project(ctx context.Context, c client) (jiraProject, error) {
	var p jiraProject
	err := c.get(ctx, "/rest/api/3/project/"+url.PathEscape(r.projectKey), nil, &p)
	return p, err
}

// Exists reports whether the project is there and the credentials can read it.
func (r *Repo) Exists(ctx context.Context) error {
	c, err := r.client(ctx)
	if err != nil {
		return err
	}
	if _, err := r.project(ctx, c); err != nil {
		if isNotFound(err) {
			return fmt.Errorf("Jira project not found, or the credentials cannot see it: %s: %w", r.key, provider.ErrNotFound)
		}
		return err
	}
	return nil
}

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
