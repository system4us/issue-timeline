// Package redmine is the Redmine tracker: its issues, their journals (history and comments), relations and parents,
// read and written through the REST API with an API key. Redmine has no pull requests, commits or CI here: the store
// holds issues only.
//
// Repo key: "redmine:<origin>/<project identifier>", e.g. "redmine:https://redmine.example.com/backend". A person's
// API key sees only what that person can see.
package redmine

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
	keyPrefix = "redmine:"
	// pageSize is Redmine's page for lists (100 is its maximum).
	pageSize = 100
	// syncMargin re-reads a little before the last sync: clocks and eventual consistency.
	syncMargin = 2 * time.Minute
	// detailConcurrency is how many single-issue reads run at once in a sync.
	detailConcurrency = 3
	// visibilityConcurrency is how many per-issue reads run at once when a person's visibility is computed.
	visibilityConcurrency = 4
	// maxAttempts is how many times a request is tried when Redmine is busy (429 or 5xx): the first try and 3 retries.
	maxAttempts  = 4
	maxRetryWait = 15 * time.Second
	// requestTimeout bounds one HTTP call.
	requestTimeout = 30 * time.Second
	// maxMembershipOffset stops reading a project's memberships after this many, as the Node server did.
	maxMembershipOffset = 10000
)

var identifierRE = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// ParseRepo reads a repo key ("redmine:https://host/ident") into the site's origin and the project identifier. It fails
// on anything else.
func ParseRepo(key string) (origin, identifier string, err error) {
	if !strings.HasPrefix(key, keyPrefix) {
		return "", "", fmt.Errorf("not a Redmine repo key: %s", key)
	}
	u, err := parseSiteURL(strings.TrimPrefix(key, keyPrefix))
	if err != nil {
		return "", "", err
	}
	identifier = lastSegment(u.Path)
	if !identifierRE.MatchString(identifier) {
		return "", "", errors.New("Redmine project identifier missing or invalid")
	}
	return originOf(u), identifier, nil
}

// KeyFromURL is the repo key of a Redmine project, from its URL ("https://host/projects/<identifier>" or
// "https://host/<identifier>").
func KeyFromURL(input string) (string, error) {
	u, err := parseSiteURL(input)
	if err != nil {
		return "", err
	}
	identifier := lastSegment(u.Path)
	if identifier == "" || identifier == "projects" {
		return "", errors.New("use the project URL: https://host/projects/<identifier>")
	}
	key := keyPrefix + originOf(u) + "/" + identifier
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
		return nil, errors.New("Redmine URL must be http or https")
	}
	if u.Host == "" {
		return nil, errors.New("Redmine URL has no host")
	}
	return u, nil
}

func originOf(u *url.URL) string {
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)
}

func lastSegment(path string) string {
	parts := strings.Split(path, "/")
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] != "" {
			return parts[i]
		}
	}
	return ""
}

// client is one site and one credential: every request carries the key in X-Redmine-API-Key.
type client struct {
	origin string
	key    string
	http   *http.Client
	// sleep waits between tries; the tests replace it so they run fast.
	sleep func(context.Context, time.Duration) error
}

// repeatsOn5xx reports whether a request with this method may be sent again after a server error (5xx). A POST is not:
// Redmine may have done it already (a second POST would create a second issue). Rate limits (429) are retried for every
// method, since Redmine did not act on those.
func repeatsOn5xx(method string) bool {
	return method == http.MethodGet || method == http.MethodPut || method == http.MethodDelete
}

// httpError is a Redmine answer that is not a success. Status is the HTTP status; Msg says what happened.
type httpError struct {
	Status int
	Msg    string
}

func (e *httpError) Error() string { return e.Msg }

// do sends one request and decodes the answer into out (nil: the answer is not read). Rate limits and server errors are
// retried, waiting what Retry-After says (at most maxRetryWait).
func (c client) do(ctx context.Context, method, path string, query url.Values, contentType string, body []byte, out any) error {
	target := c.origin + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	for attempt := 1; ; attempt++ {
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, target, rd)
		if err != nil {
			return err
		}
		req.Header.Set("X-Redmine-API-Key", c.key)
		req.Header.Set("Accept", "application/json")
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			// A refused redirect (see provider.SameOriginRedirects) is a 502 that names the cause.
			var pe *provider.Error
			if errors.As(err, &pe) {
				return provider.Errorf(pe.Status, "Redmine %s", pe.Message)
			}
			return fmt.Errorf("Redmine %s %s: %w", method, path, err)
		}
		data, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return fmt.Errorf("reading %s: %w", path, readErr)
		}
		busy := resp.StatusCode == http.StatusTooManyRequests || (resp.StatusCode >= 500 && repeatsOn5xx(method))
		if busy && attempt < maxAttempts {
			if err := c.wait(ctx, retryWait(resp, attempt)); err != nil {
				return err
			}
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return statusError(resp.StatusCode, path, data)
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

// statusError builds the error for a Redmine answer that is not a success. A 422 carries Redmine's own messages.
func statusError(status int, path string, data []byte) error {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return &httpError{Status: status, Msg: fmt.Sprintf("Redmine refused the API key (HTTP %d) on %s: check the key and the project's access", status, path)}
	case http.StatusNotFound:
		return &httpError{Status: status, Msg: fmt.Sprintf("Redmine: not found (%s)", path)}
	case http.StatusUnprocessableEntity:
		var d struct {
			Errors []string `json:"errors"`
		}
		_ = json.Unmarshal(data, &d)
		msg := "invalid"
		if len(d.Errors) > 0 {
			msg = strings.Join(d.Errors, "; ")
		}
		return &httpError{Status: status, Msg: "Redmine rejected the change: " + msg}
	}
	return &httpError{Status: status, Msg: fmt.Sprintf("Redmine HTTP %d on %s", status, path)}
}

// wait sleeps between tries with the client's sleep.
func (c client) wait(ctx context.Context, d time.Duration) error {
	if c.sleep == nil {
		return sleep(ctx, d)
	}
	return c.sleep(ctx, d)
}

// retryWait is how long to wait before the next try: what Retry-After says (at most maxRetryWait). A missing or zero
// Retry-After, or one in the past, waits the backoff of 2 s per attempt, as the Node server did.
func retryWait(resp *http.Response, attempt int) time.Duration {
	if v := resp.Header.Get("Retry-After"); v != "" {
		if secs, err := strconv.ParseFloat(v, 64); err == nil && secs > 0 {
			return min(time.Duration(secs*float64(time.Second)), maxRetryWait)
		}
		if at, err := http.ParseTime(v); err == nil && time.Until(at) > 0 {
			return min(time.Until(at), maxRetryWait)
		}
	}
	return min(time.Duration(attempt*2)*time.Second, maxRetryWait)
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

// write sends a JSON body and reads nothing back (Redmine answers a change with no body or with the new object).
func (c client) write(ctx context.Context, method, path string, v any, out any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return writeError(c.do(ctx, method, path, nil, "application/json", b, out))
}

// del sends a DELETE.
func (c client) del(ctx context.Context, path string) error {
	return writeError(c.do(ctx, http.MethodDelete, path, nil, "", nil, nil))
}

// writeError maps a failed change to the error the API answers with: a refused key or change is 401 or 403, a
// rejected change 422 with Redmine's own messages, a missing issue is provider.ErrNotFound.
func writeError(err error) error {
	var he *httpError
	if !errors.As(err, &he) {
		return err
	}
	switch he.Status {
	case http.StatusUnauthorized:
		return provider.Errorf(401, "Redmine refused the key (HTTP 401): sign in again")
	case http.StatusForbidden:
		return provider.Errorf(403, "Redmine refused the change (HTTP 403): the key's user cannot edit this issue")
	case http.StatusNotFound:
		return fmt.Errorf("Redmine: the issue is not there: %w", provider.ErrNotFound)
	case http.StatusUnprocessableEntity:
		return provider.Errorf(422, "%s", he.Msg)
	}
	return err
}

// isNotFound reports whether a Redmine answer was 404.
func isNotFound(err error) bool {
	var he *httpError
	return errors.As(err, &he) && he.Status == http.StatusNotFound
}

// isRefused reports whether Redmine refused the key (401 or 403).
func isRefused(err error) bool {
	var he *httpError
	return errors.As(err, &he) && (he.Status == http.StatusUnauthorized || he.Status == http.StatusForbidden)
}

// pathIssueID checks an issue id taken from a request or a record before it goes in a URL path.
func pathIssueID(key string) (int, error) {
	s, err := provider.PathID(key, "a Redmine issue id")
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(s)
}
