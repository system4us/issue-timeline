package httpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"

	"gitlab.com/system4us/issue-timeline/api"
)

// loadSpec loads and validates the API contract (the copy compiled into the binary).
func loadSpec(t *testing.T) (*openapi3.T, routers.Router) {
	t.Helper()
	doc, err := openapi3.NewLoader().LoadFromData(api.OpenAPI)
	if err != nil {
		t.Fatalf("load spec: %v", err)
	}
	if err := doc.Validate(context.Background()); err != nil {
		t.Fatalf("spec is not valid OpenAPI: %v", err)
	}
	rt, err := legacy.NewRouter(doc)
	if err != nil {
		t.Fatalf("router: %v", err)
	}
	return doc, rt
}

func TestSpecIsValid(t *testing.T) { loadSpec(t) }

// conformance runs requests through the handler and checks every answer against the spec.
type conformance struct {
	t    *testing.T
	h    http.Handler
	rt   routers.Router
	seen map[string]bool // the operationIds exercised
}

// do sends one request as this machine (so it needs no session), checks the status, and validates the request and the
// answer against the spec. It returns the answer's body.
func (c *conformance) do(op, method, target, body string, want int) []byte {
	c.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, rd)
	req.Host = "127.0.0.1:4310"
	req.RemoteAddr = "127.0.0.1:40000"
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet {
		req.Header.Set("x-issue-timeline", "1")
	}
	return c.check(op, req, body, want)
}

// check sends a request built by the caller (its own headers, its own context) and validates it and its answer.
func (c *conformance) check(op string, req *http.Request, body string, want int) []byte {
	c.t.Helper()
	method, target := req.Method, req.URL.String()
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	if rec.Code != want {
		c.t.Fatalf("%s %s %s: status %d, want %d: %s", op, method, target, rec.Code, want, rec.Body.String())
	}
	c.seen[op] = true

	route, pathParams, err := c.rt.FindRoute(req)
	if err != nil {
		c.t.Fatalf("%s %s: not in the spec: %v", method, target, err)
	}
	reqIn := &openapi3filter.RequestValidationInput{Request: req, PathParams: pathParams, Route: route,
		Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc}}
	if body != "" {
		req.Body = io.NopCloser(strings.NewReader(body))
	}
	// A refused request is off-spec on purpose: only the successful ones must match the request schema.
	if err := openapi3filter.ValidateRequest(context.Background(), reqIn); err != nil && want < 400 {
		c.t.Errorf("%s %s: request does not match the spec: %v", method, target, err)
	}
	res := rec.Result()
	// The JSON answers are checked in full. The spec and the page are text (YAML, HTML): kin would decode them as
	// structured data, so for those only the status, the headers and a non-empty body are checked.
	ropts := &openapi3filter.Options{IncludeResponseStatus: true}
	if !strings.HasPrefix(res.Header.Get("Content-Type"), "application/json") {
		ropts.ExcludeResponseBody = true
		if rec.Body.Len() == 0 {
			c.t.Errorf("%s %s: empty body", method, target)
		}
	}
	resIn := &openapi3filter.ResponseValidationInput{RequestValidationInput: reqIn, Status: res.StatusCode, Header: res.Header,
		Body: io.NopCloser(bytes.NewReader(rec.Body.Bytes())), Options: ropts}
	if err := openapi3filter.ValidateResponse(context.Background(), resIn); err != nil {
		c.t.Errorf("%s %s: response does not match the spec: %v\nbody: %s", method, target, err, rec.Body.String())
	}
	return rec.Body.Bytes()
}

// waitForIssues polls a repo's data until its sync has stored records, or fails after a while.
func (c *conformance) waitForIssues(repo string) {
	c.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var data struct {
			Issues []json.RawMessage `json:"issues"`
		}
		body := c.do("getData", http.MethodGet, "/api/data?repo="+url.QueryEscape(repo), "", http.StatusOK)
		if err := json.Unmarshal(body, &data); err == nil && len(data.Issues) > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	c.t.Fatalf("the sync of %s stored no issue within 10 s", repo)
}

// TestSpecConformance drives every operation of the spec through the handler, with fake hosts, and checks each answer.
// Every operationId must be exercised at least once.
func TestSpecConformance(t *testing.T) {
	doc, rt := loadSpec(t)
	o := opts(nil)
	o.Env = func(k string) string {
		switch k {
		case "WEBHOOK_SECRET":
			return hookSecretForTests
		case "GITHUB_OAUTH_CLIENT_ID", "GITHUB_OAUTH_CLIENT_SECRET":
			return "oauth-" + k
		}
		return ""
	}
	c := &conformance{t: t, h: New(buildApp(t), o), rt: rt, seen: map[string]bool{}}

	c.do("getOpenApiDocument", http.MethodGet, "/api/openapi.yaml", "", http.StatusOK)
	c.do("getApiDocs", http.MethodGet, "/api/docs", "", http.StatusOK)
	c.do("getMe", http.MethodGet, "/api/me", "", http.StatusUnauthorized)
	c.do("signIn", http.MethodPost, "/api/session", `{"provider":"redmine","key":"k"}`, http.StatusBadRequest)
	c.do("signOut", http.MethodDelete, "/api/session", "", http.StatusOK)

	gh := jsonField(c.do("addRepo", http.MethodPost, "/api/repos", `{"provider":"github","repo":"owner/name","mode":"read"}`, http.StatusCreated), "repo")
	c.do("addRepo", http.MethodPost, "/api/repos", `{"provider":"github","repo":"owner/name"}`, http.StatusOK)
	c.do("addRepo", http.MethodPost, "/api/repos", `{"provider":"github","repo":"not a repo!"}`, http.StatusBadRequest)
	c.do("listRepos", http.MethodGet, "/api/repos", "", http.StatusOK)
	c.do("setRepoMode", http.MethodPut, "/api/repo-mode?repo="+url.QueryEscape(gh), `{"mode":"write"}`, http.StatusOK)

	tracker := jsonField(c.do("addRepo", http.MethodPost, "/api/repos", `{"provider":"redmine","repo":"https://redmine.example.com/projects/ident"}`, http.StatusCreated), "repo")
	c.do("saveLinks", http.MethodPost, "/api/links", fmt.Sprintf(`{"tracker":%q,"code":[%q]}`, tracker, gh), http.StatusOK)

	q := "repo=" + url.QueryEscape(gh)
	c.do("startSync", http.MethodPost, "/api/sync?"+q+"&full=1", "", http.StatusAccepted)
	c.waitForIssues(gh)
	c.do("getData", http.MethodGet, "/api/data?"+q, "", http.StatusOK)
	c.do("searchRecords", http.MethodGet, "/api/search?"+q+"&q=Fake", "", http.StatusOK)
	c.do("getIssue", http.MethodGet, "/api/issues/1?"+q, "", http.StatusOK)
	c.do("getIssue", http.MethodGet, "/api/issues/999?"+q, "", http.StatusNotFound)
	c.do("getPull", http.MethodGet, "/api/pulls/2?"+q, "", http.StatusOK)
	c.do("editIssue", http.MethodPatch, "/api/issues/1?"+q, `{"title":"Renamed"}`, http.StatusOK)
	c.do("editIssue", http.MethodPatch, "/api/issues/1?"+q, `{"state":"SOMETHING"}`, http.StatusBadRequest)
	c.do("editPull", http.MethodPatch, "/api/pulls/2?"+q, `{"title":"Renamed"}`, http.StatusOK)
	c.do("createIssue", http.MethodPost, "/api/issues?"+q, `{"title":"New issue"}`, http.StatusCreated)
	c.do("getEditOptions", http.MethodGet, "/api/edit-options?"+q, "", http.StatusOK)
	c.do("refreshRecord", http.MethodPost, "/api/refresh-record?"+q+"&kind=issue&key=1", "", http.StatusOK)
	c.do("listCommits", http.MethodGet, "/api/commits?"+q, "", http.StatusOK)
	c.do("getCommit", http.MethodGet, "/api/commit?"+q+"&sha="+strings.Repeat("a", 40), "", http.StatusOK)
	c.do("getCommit", http.MethodGet, "/api/commit?"+q+"&sha=zz", "", http.StatusNotFound)
	c.do("getMergeMethod", http.MethodGet, "/api/merge?"+q+"&number=2", "", http.StatusOK)

	// sign-in: the methods, the start of a GitHub sign-in (an app is set up), and a callback that is not of this browser
	c.do("getSignInOptions", http.MethodGet, "/api/sign-in-options", "", http.StatusOK)
	c.do("startOAuth", http.MethodGet, "/api/oauth/github/start?return=/x", "", http.StatusFound)
	c.do("startOAuth", http.MethodGet, "/api/oauth/gitlab/start", "", http.StatusNotFound)
	c.do("oauthCallback", http.MethodGet, "/api/oauth/github/callback?code=c&state=s", "", http.StatusFound)

	// the event stream: its first lines arrive, then the page goes away (the request's context ends)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	stream := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/events?"+q, nil)
	stream.Host, stream.RemoteAddr = "127.0.0.1:4310", "127.0.0.1:40000"
	if got := string(c.check("streamEvents", stream, "", http.StatusOK)); !strings.HasPrefix(got, "retry: 3000\n") {
		t.Errorf("the stream starts with its retry time: %q", got)
	}

	// the webhook: from a host (not this machine, no session, no write header), signed with the secret; a repo not
	// configured here is accepted and synced nowhere
	hook := `{"action":"opened","repository":{"full_name":"someone/else"}}`
	signed := hookRequest(hook, "X-GitHub-Event", "issues", "X-Hub-Signature-256", signHook(hook))
	if got := string(c.check("receiveWebhook", signed, hook, http.StatusAccepted)); !strings.Contains(got, `"repos":[]`) {
		t.Errorf("no configured repo matched: %s", got)
	}
	c.check("receiveWebhook", hookRequest(hook, "X-GitHub-Event", "issues"), hook, http.StatusUnauthorized)

	var missing []string
	for _, item := range doc.Paths.Map() {
		for _, op := range item.Operations() {
			if !c.seen[op.OperationID] {
				missing = append(missing, op.OperationID)
			}
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("operations not exercised by the conformance run: %s", strings.Join(missing, ", "))
	}
}

// jsonField is one string field of a JSON object answer.
func jsonField(body []byte, field string) string {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return ""
	}
	s, _ := m[field].(string)
	return s
}

const hookSecretForTests = "hook-secret"

// signHook is GitHub's signature of a body under the tests' webhook secret.
func signHook(body string) string {
	mac := hmac.New(sha256.New, []byte(hookSecretForTests))
	mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// hookRequest is a webhook delivery as a host sends it: from another address, with only its own headers.
func hookRequest(body string, kv ...string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/webhook", strings.NewReader(body))
	req.Host = "127.0.0.1:4310"
	req.RemoteAddr = "203.0.113.9:5000"
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(kv); i += 2 {
		req.Header.Set(kv[i], kv[i+1])
	}
	return req
}
