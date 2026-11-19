package github

import (
	"bytes"
	"context"
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

	"gitlab.com/system4us/issue-timeline/internal/provider"
)

const (
	restTimeout    = 30 * time.Second
	graphqlTimeout = 60 * time.Second // a sync page costs GitHub several seconds
	maxAttempts    = 4                // the first try and up to three retries
	maxWait        = 15 * time.Second
	isoMillis      = "2006-01-02T15:04:05.000Z" // what JavaScript's toISOString writes: the stores keep this form
)

// api is the HTTP side: every call goes through it, with its timeout and its retries.
type api struct {
	http  *http.Client
	pause func(ctx context.Context, d time.Duration) error
}

func newAPI(o Options) *api {
	a := &api{http: sameOrigin(o.HTTP), pause: o.pause}
	if a.pause == nil {
		a.pause = realPause
	}
	return a
}

func realPause(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// hostError is an HTTP answer GitHub gave with a status the API should pass on. asProvider turns it into a
// *provider.Error; a caller that needs the status (a 404 read as "no role") reads it with asHostError.
type hostError struct {
	Status int
	Msg    string
}

func (e *hostError) Error() string { return e.Msg }

// asHostError finds a *hostError in err's chain.
func asHostError(err error, target **hostError) bool { return errors.As(err, target) }

// asProvider turns a host answer into the *provider.Error the API answers with: 401, 403 and 422 keep their status, any
// other host answer is a 502. Errors that are not the host's answer pass through.
func asProvider(err error) error {
	if err == nil {
		return nil
	}
	var he *hostError
	if errors.As(err, &he) {
		status := 502
		switch he.Status {
		case 401, 403, 422:
			status = he.Status
		}
		return &provider.Error{Status: status, Message: he.Msg}
	}
	return err
}

// pathSeg escapes one segment of a URL path (an owner or a repo name, configured by the server).
func pathSeg(s string) string { return url.PathEscape(s) }

// retryWait is how long to wait before a retry: what Retry-After says, else the fallback; never more than 15 s.
func retryWait(retryAfter string, fallback time.Duration) time.Duration {
	d := fallback
	if n, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && n > 0 {
		d = time.Duration(n) * time.Second
	}
	if d > maxWait {
		d = maxWait
	}
	return d
}

// response is an answer read whole: its status, headers and body.
type response struct {
	Status int
	Header http.Header
	Body   []byte
}

// retryableREST says whether a REST answer is retried. A rate limit (429) is retried for every method: the host did not
// process the request. A server error (5xx) is retried only for GET, PUT and DELETE, which repeat with the same effect; a
// POST or PATCH may already have created something (a comment, an issue, a sub-issue link), so it is never sent again.
func retryableREST(method string, status int) bool {
	if status == 429 {
		return true
	}
	if status >= 500 {
		return method == "GET" || method == "PUT" || method == "DELETE"
	}
	return false
}

// send makes a REST call, retrying as retryableREST says, up to three times.
func (a *api) send(ctx context.Context, method, u, token string, payload []byte) (*response, error) {
	for attempt := 1; ; attempt++ {
		res, err := a.sendOnce(ctx, method, u, token, payload)
		if err != nil {
			return nil, err
		}
		if retryableREST(method, res.Status) && attempt < maxAttempts {
			if err := a.pause(ctx, retryWait(res.Header.Get("Retry-After"), time.Duration(attempt)*3*time.Second)); err != nil {
				return nil, err
			}
			continue
		}
		return res, nil
	}
}

func (a *api) sendOnce(ctx context.Context, method, u, token string, payload []byte) (*response, error) {
	ctx, cancel := context.WithTimeout(ctx, restTimeout)
	defer cancel()
	var body *bytes.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	var req *http.Request
	var err error
	if body != nil {
		req, err = http.NewRequestWithContext(ctx, method, u, body)
	} else {
		req, err = http.NewRequestWithContext(ctx, method, u, nil)
	}
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, pathOf(u), err)
	}
	req.Header.Set("authorization", "Bearer "+token)
	req.Header.Set("accept", "application/vnd.github+json")
	req.Header.Set("content-type", "application/json")
	req.Header.Set("user-agent", "issue-timeline")
	resp, err := a.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, pathOf(u), err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%s %s: reading the answer: %w", method, pathOf(u), err)
	}
	return &response{Status: resp.StatusCode, Header: resp.Header, Body: b}, nil
}

// pathOf is the path of a URL, for messages (no query, which can hold a search).
func pathOf(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return u
	}
	return p.Path
}

// hostErrorOf is the error for a REST answer that is not 2xx, worded as the Node server worded it.
func hostErrorOf(res *response, method, u string) error {
	var why struct {
		Message *string `json:"message"`
		Error   *string `json:"error"`
	}
	_ = json.Unmarshal(res.Body, &why)
	msg := ""
	switch {
	case why.Message != nil:
		msg = *why.Message
	case why.Error != nil:
		msg = *why.Error
	}
	switch res.Status {
	case 401:
		return &hostError{Status: 401, Msg: "the host refused the token (HTTP 401): sign in again"}
	case 403:
		if msg == "" {
			msg = "this account cannot do it here"
		}
		return &hostError{Status: 403, Msg: "the host refused the change (HTTP 403): " + msg}
	case 422:
		if msg == "" {
			msg = "invalid"
		}
		return &hostError{Status: 422, Msg: "the host rejected the change: " + msg}
	}
	return &hostError{Status: res.Status, Msg: fmt.Sprintf("the host answered HTTP %d to %s %s", res.Status, method, pathOf(u))}
}

// rest calls the REST API and decodes the answer into out (when not nil). A 204 has no answer.
func (a *api) rest(ctx context.Context, method, u, token string, body any, out any) error {
	var payload []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = b
	}
	res, err := a.send(ctx, method, u, token, payload)
	if err != nil {
		return err
	}
	if res.Status < 200 || res.Status > 299 {
		return hostErrorOf(res, method, u)
	}
	if out == nil || res.Status == 204 || len(bytes.TrimSpace(res.Body)) == 0 {
		return nil
	}
	if err := json.Unmarshal(res.Body, out); err != nil {
		return fmt.Errorf("reading %s: %w", pathOf(u), err)
	}
	return nil
}

// restAll reads every page of a list. It follows the Link header's rel="next" (at most maxPages pages) and never a next
// link to another origin. A first page that fails is an error; a later page that fails ends the list with what was read.
func restAll[T any](ctx context.Context, a *api, first, token string, maxPages int) ([]T, error) {
	var out []T
	next := first
	for page := 0; next != "" && page < maxPages; page++ {
		res, err := a.send(ctx, "GET", next, token, nil)
		if err != nil {
			return nil, err
		}
		if res.Status < 200 || res.Status > 299 {
			if page == 0 {
				return nil, hostErrorOf(res, "GET", next)
			}
			return out, nil
		}
		var items []T
		if err := json.Unmarshal(res.Body, &items); err != nil {
			return nil, fmt.Errorf("reading %s: %w", pathOf(next), err)
		}
		out = append(out, items...)
		next = nextLink(res.Header.Get("Link"), first)
	}
	return out, nil
}

var linkNext = regexp.MustCompile(`<([^>]+)>\s*;\s*rel="next"`)

// nextLink is the rel="next" URL of a Link header, when it is on the same origin as first; "" otherwise.
func nextLink(link, first string) string {
	m := linkNext.FindStringSubmatch(link)
	if m == nil {
		return ""
	}
	base, err := url.Parse(first)
	if err != nil {
		return ""
	}
	ref, err := url.Parse(m[1])
	if err != nil {
		return ""
	}
	u := base.ResolveReference(ref)
	if u.Scheme != base.Scheme || u.Host != base.Host {
		return "" // never follow a link to another host
	}
	return u.String()
}

// graphql posts one read-only query and returns the `data` of the answer. Only queries come here: a mutation (a draft
// switch) goes through rest as a POST, which a 5xx never repeats. A transport error, a body cut short, a rate limit
// (429), GitHub's secondary limit (403) and a 5xx are retried. A GraphQL error fails the call, unless every error is
// NOT_FOUND: a node the token cannot see reads as empty, and the rest of the data stays.
func (a *api) graphql(ctx context.Context, endpoint, token, query string, vars map[string]any) (json.RawMessage, error) {
	payload, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return nil, err
	}
	for attempt := 1; ; attempt++ {
		status, header, body, err := a.graphqlOnce(ctx, endpoint, token, payload)
		if err != nil {
			if attempt < maxAttempts {
				if perr := a.pause(ctx, time.Duration(attempt)*2*time.Second); perr != nil {
					return nil, perr
				}
				continue
			}
			return nil, err
		}
		if (status == 429 || status == 403 || status >= 500) && attempt < maxAttempts {
			if perr := a.pause(ctx, retryWait(header.Get("Retry-After"), time.Duration(attempt)*2*time.Second)); perr != nil {
				return nil, perr
			}
			continue
		}
		if status < 200 || status > 299 {
			return nil, fmt.Errorf("GitHub %d: %s", status, truncate(string(body), 200))
		}
		var env struct {
			Data   json.RawMessage `json:"data"`
			Errors []struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"errors"`
		}
		if err := json.Unmarshal(body, &env); err != nil {
			if attempt < maxAttempts {
				if perr := a.pause(ctx, time.Duration(attempt)*2*time.Second); perr != nil {
					return nil, perr
				}
				continue
			}
			return nil, fmt.Errorf("GitHub GraphQL: unreadable answer: %w", err)
		}
		if len(env.Errors) > 0 {
			onlyMissing := hasData(env.Data)
			msgs := make([]string, 0, len(env.Errors))
			for _, e := range env.Errors {
				if e.Type != "NOT_FOUND" {
					onlyMissing = false
				}
				msgs = append(msgs, e.Message)
			}
			if !onlyMissing {
				joined := strings.Join(msgs, "; ")
				if strings.Contains(joined, "OAuth App access restrictions") {
					return nil, &hostError{Status: 403, Msg: "GitHub organization restricts third-party OAuth apps: approve this OAuth app in the organization's settings, or set a Personal Access Token (GITHUB_TOKEN) on the server"}
				}
				return nil, fmt.Errorf("GitHub GraphQL: %s", joined)
			}
		}
		return env.Data, nil
	}
}

// graphqlInto is graphql with the data decoded into out.
func (a *api) graphqlInto(ctx context.Context, endpoint, token, query string, vars map[string]any, out any) error {
	data, err := a.graphql(ctx, endpoint, token, query, vars)
	if err != nil {
		return err
	}
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("GitHub GraphQL: reading the data: %w", err)
	}
	return nil
}

// graphqlOnce is one POST of a GraphQL query: the status, headers and whole body.
func (a *api) graphqlOnce(ctx context.Context, endpoint, token string, payload []byte) (int, http.Header, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, graphqlTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("authorization", "bearer "+token)
	req.Header.Set("content-type", "application/json")
	req.Header.Set("user-agent", "issue-timeline")
	// native parent / sub-issue and blocked-by / blocking fields
	req.Header.Set("graphql-features", "sub_issues,issue_dependencies")
	resp, err := a.http.Do(req)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("GitHub GraphQL: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("GitHub GraphQL: reading the answer: %w", err)
	}
	return resp.StatusCode, resp.Header, b, nil
}

// hasData reports whether a GraphQL answer carries data (not null).
func hasData(d json.RawMessage) bool { return len(d) > 0 && string(d) != "null" }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// scalarText is a JSON value (a string or a number) as the text the stores compare with: "12" for 12 and for "12".
func scalarText(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return ""
	}
	if s[0] == '"' {
		var t string
		if err := json.Unmarshal(raw, &t); err == nil {
			return t
		}
	}
	return s
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
