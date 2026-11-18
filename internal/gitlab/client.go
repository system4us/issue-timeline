package gitlab

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
	"sync"
	"time"

	"gitlab.com/system4us/issue-timeline/internal/provider"
)

const (
	requestTimeout = 30 * time.Second
	maxAttempts    = 4 // the first try and three retries
	maxWait        = 15 * time.Second
	maxBody        = 64 << 20
	pageSize       = 100
)

// eventPattern is the three event endpoints, which GitLab rate-limits harder than the rest.
var eventPattern = regexp.MustCompile(`/resource_(?:state|label|milestone)_events(?:/|\?|$)`)

// client is one operation's view of the API: a token, the API base, and the cooldown of the event endpoints. Each
// operation builds its own, so the cooldown lasts for one sync, as it did in the Node server.
type client struct {
	http *http.Client
	base string // the API base, for example https://host/api/v4
	// token is the credential the requests go with.
	token string
	// invalidate is the host whose cached server token a 401 drops. It is "" for a person's token: their refusal is
	// theirs, not the server's.
	invalidate string
	log        func(string)

	mu            sync.Mutex
	cooldownUntil time.Time
	limitLogged   bool
}

// response is an answer of the host, read in full.
type response struct {
	status int
	header http.Header
	body   []byte
}

// upstreamError is an error from the host that keeps the host's own status. provider.Error carries what the API should
// answer; the host's status tells whether an optional list may be skipped (see listOptional).
type upstreamError struct {
	upstream int
	err      *provider.Error
}

func (e *upstreamError) Error() string { return e.err.Message }
func (e *upstreamError) Unwrap() error { return e.err }

// send makes one request. A 429 is retried for every method, up to three times, waiting what Retry-After says (at most
// 15 s). A 5xx and a transport error are retried only when retry5xx is set: GET, PUT and DELETE, and GraphQL queries.
// A POST that failed on the host's side may have made the change (a note, an issue, a link, a time entry), so it is
// sent once. A 429 from an event endpoint is returned at once: the caller starts the cooldown.
func (c *client) send(ctx context.Context, method, u string, body any, event, retry5xx bool) (*response, error) {
	var payload []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encoding the %s body: %w", method, err)
		}
		payload = b
	}
	for attempt := 1; ; attempt++ {
		res, err := c.once(ctx, method, u, payload)
		if err != nil {
			if retry5xx && attempt < maxAttempts && ctx.Err() == nil {
				if werr := sleepCtx(ctx, backoff(attempt)); werr != nil {
					return nil, werr
				}
				continue
			}
			return nil, provider.Errorf(502, "GitLab request to %s failed after %d tries: %v", pathOf(u), attempt, err)
		}
		if event && res.status == 429 {
			return res, nil
		}
		if res.status == 429 && attempt < maxAttempts {
			if werr := sleepCtx(ctx, retryAfter(res.header, attempt)); werr != nil {
				return nil, werr
			}
			continue
		}
		if res.status >= 500 && retry5xx && attempt < maxAttempts {
			if werr := sleepCtx(ctx, retryAfter(res.header, attempt)); werr != nil {
				return nil, werr
			}
			continue
		}
		if res.status == 401 && c.invalidate != "" {
			forgetToken(c.invalidate)
		}
		return res, nil
	}
}

// retriesServerErrors is whether a 5xx or a transport error may be sent again for this method.
func retriesServerErrors(method string) bool {
	return method == http.MethodGet || method == http.MethodPut || method == http.MethodDelete
}

// once is one request with its own deadline.
func (c *client) once(ctx context.Context, method, u string, payload []byte) (*response, error) {
	rctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	var rdr io.Reader
	if payload != nil {
		rdr = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(rctx, method, u, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("User-Agent", "issue-timeline")
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, err
	}
	return &response{status: resp.StatusCode, header: resp.Header, body: data}, nil
}

// backoff is the wait before a retry of a transport error: 2 s, 4 s, 6 s.
func backoff(attempt int) time.Duration { return time.Duration(attempt) * 2 * time.Second }

// retryAfter is the Retry-After wait (seconds), or two seconds per attempt when the host does not say, at most 15 s.
func retryAfter(h http.Header, attempt int) time.Duration {
	d := time.Duration(attempt) * 2 * time.Second
	if v, err := strconv.ParseFloat(strings.TrimSpace(h.Get("Retry-After")), 64); err == nil && v >= 0 {
		d = time.Duration(v * float64(time.Second))
	}
	return min(d, maxWait)
}

func (c *client) cooling() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().Before(c.cooldownUntil)
}

// coolDown skips the event endpoints for d, and says so once in the log.
func (c *client) coolDown(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cooldownUntil = time.Now().Add(d)
	if !c.limitLogged && c.log != nil {
		c.log("GitLab rate-limited the events endpoints; skipping those events during the cooldown and continuing with issues and merge requests.")
		c.limitLogged = true
	}
}

// get reads one URL. found is false for a 404, and for an event endpoint that is cooling down or rate-limited (its
// events are skipped, not an error). Any other failure is an error.
func (c *client) get(ctx context.Context, u string) (res *response, found bool, err error) {
	event := eventPattern.MatchString(u)
	if event && c.cooling() {
		return nil, false, nil
	}
	res, err = c.send(ctx, http.MethodGet, u, nil, event, true)
	if err != nil {
		return nil, false, err
	}
	if event && res.status == 429 {
		wait := 60 * time.Second
		if v, perr := strconv.Atoi(strings.TrimSpace(res.header.Get("Retry-After"))); perr == nil && v > 0 {
			wait = time.Duration(v) * time.Second
		}
		c.coolDown(wait)
		return nil, false, nil
	}
	if res.status == 404 {
		return nil, false, nil
	}
	if res.status < 200 || res.status > 299 {
		return nil, false, readError(u, res)
	}
	return res, true, nil
}

// readError is the error of a failed read: the host's status is kept for the optional lists, and the API answers 401
// and 403 as they are, and anything else as 502.
func readError(u string, res *response) error {
	msg := fmt.Sprintf("GitLab %d at %s: %s", res.status, pathOf(u), truncate(string(res.body), 180))
	status := 502
	if res.status == 401 || res.status == 403 {
		status = res.status
	}
	return &upstreamError{upstream: res.status, err: &provider.Error{Status: status, Message: msg}}
}

// write makes a change. A 404 is an error unless allow404, in which case found is false. The messages are the ones the
// edit form shows: the host refused the token, refused the change, or rejected it.
func (c *client) write(ctx context.Context, method, u string, body any, allow404 bool) (res *response, found bool, err error) {
	return c.writeWith(ctx, method, u, body, allow404, retriesServerErrors(method))
}

// writeWith is write, with the 5xx retry given: a GraphQL query may be retried, a mutation may not.
func (c *client) writeWith(ctx context.Context, method, u string, body any, allow404, retry5xx bool) (res *response, found bool, err error) {
	res, err = c.send(ctx, method, u, body, false, retry5xx)
	if err != nil {
		return nil, false, err
	}
	if allow404 && res.status == 404 {
		return nil, false, nil
	}
	if res.status >= 200 && res.status <= 299 {
		return res, true, nil
	}
	return nil, false, writeError(method, u, res)
}

// call is write with no 404 allowed.
func (c *client) call(ctx context.Context, method, u string, body any) (*response, error) {
	res, _, err := c.write(ctx, method, u, body, false)
	return res, err
}

func writeError(method, u string, res *response) error {
	switch res.status {
	case 401:
		return &provider.Error{Status: 401, Message: "the host refused the token (HTTP 401): sign in again"}
	case 403:
		var why struct {
			Message string `json:"message"`
			Error   string `json:"error"`
		}
		_ = json.Unmarshal(res.body, &why)
		reason := firstNonEmpty(why.Message, why.Error, "this account cannot do it here")
		return &provider.Error{Status: 403, Message: "the host refused the change (HTTP 403): " + reason}
	case 422:
		var why struct {
			Message any `json:"message"`
		}
		_ = json.Unmarshal(res.body, &why)
		reason := "invalid"
		if s, ok := why.Message.(string); ok && s != "" {
			reason = s
		} else if why.Message != nil {
			b, _ := json.Marshal(why.Message)
			reason = string(b)
		}
		return &provider.Error{Status: 422, Message: "the host rejected the change: " + reason}
	}
	return &provider.Error{Status: 502, Message: fmt.Sprintf("the host answered HTTP %d to %s %s", res.status, method, pathOf(u))}
}

// listPages reads every page of a list. The next page is the host's X-Next-Page number, never a link: the request goes
// to the API base only.
func listPages[T any](ctx context.Context, c *client, path string, q url.Values) ([]T, error) {
	out := []T{}
	for page := 1; ; {
		qq := url.Values{}
		for k, v := range q {
			qq[k] = v
		}
		qq.Set("per_page", strconv.Itoa(pageSize))
		qq.Set("page", strconv.Itoa(page))
		res, found, err := c.get(ctx, c.apiURL(path, qq))
		if err != nil {
			return nil, err
		}
		if !found {
			return out, nil
		}
		var items []T
		if err := json.Unmarshal(res.body, &items); err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
		out = append(out, items...)
		next, err := strconv.Atoi(res.header.Get("X-Next-Page"))
		if err != nil || next <= page {
			return out, nil
		}
		page = next
	}
}

// listOptional is listPages for a list the sync can do without: a host that refuses it (or has no such feature) gives
// an empty list.
func listOptional[T any](ctx context.Context, c *client, path string, q url.Values) ([]T, error) {
	out, err := listPages[T](ctx, c, path, q)
	if err != nil && isOptionalStatus(err) {
		return []T{}, nil
	}
	return out, err
}

func isOptionalStatus(err error) bool {
	var ue *upstreamError
	if !errors.As(err, &ue) {
		return false
	}
	switch ue.upstream {
	case 400, 403, 404, 405, 429:
		return true
	}
	return false
}

// getOne reads one object. A 404 is nil, with no error.
func getOne[T any](ctx context.Context, c *client, path string, q url.Values) (*T, error) {
	res, found, err := c.get(ctx, c.apiURL(path, q))
	if err != nil || !found {
		return nil, err
	}
	var v T
	if err := json.Unmarshal(res.body, &v); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return &v, nil
}

// apiURL is the URL of a path under the API base, with a query.
func (c *client) apiURL(path string, q url.Values) string {
	u := c.base + "/" + strings.TrimPrefix(path, "/")
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

// pathOf is the path of a URL, for messages (no query, no host).
func pathOf(u string) string {
	parsed, err := url.Parse(u)
	if err != nil {
		return "(bad URL)"
	}
	return parsed.Path
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// newClient is a client for the repo's API with a token.
func (r *Repo) newClient(token string, serverToken bool, log func(string)) *client {
	c := &client{http: r.http, base: r.apiBase, token: token, log: log}
	if serverToken {
		c.invalidate = r.host
	}
	return c
}

// sleepCtx waits d, or until ctx ends.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
