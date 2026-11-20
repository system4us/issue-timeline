package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/provider"
	"gitlab.com/system4us/issue-timeline/internal/store"
)

// fakeCall is one request the fake host received.
type fakeCall struct {
	method string
	path   string // escaped path, as the host sees it
	query  string
	body   string
}

// fakeHost is an httptest server standing for GitLab. Routes are keyed "METHOD /escaped/path"; a request with no route
// gets a 404, which is what the host answers for a feature it does not have.
type fakeHost struct {
	t      *testing.T
	srv    *httptest.Server
	mu     sync.Mutex
	routes map[string]http.HandlerFunc
	calls  []fakeCall
}

func newFakeHost(t *testing.T) *fakeHost {
	t.Helper()
	f := &fakeHost{t: t, routes: map[string]http.HandlerFunc{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeHost) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.calls = append(f.calls, fakeCall{method: r.Method, path: r.URL.EscapedPath(), query: r.URL.RawQuery, body: string(body)})
	h, ok := f.routes[r.Method+" "+r.URL.EscapedPath()]
	f.mu.Unlock()
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"404 Not Found"}`)
		return
	}
	// the body is read again by the handler: put it back
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	h(w, r)
}

// route sets the answer of one method and path.
func (f *fakeHost) route(method, path string, h http.HandlerFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes[method+" "+path] = h
}

// json answers with a status and a JSON body, and the headers given in pairs.
func jsonReply(status int, body any, headers ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, _ := json.Marshal(body)
		w.Header().Set("Content-Type", "application/json")
		for i := 0; i+1 < len(headers); i += 2 {
			w.Header().Set(headers[i], headers[i+1])
		}
		w.WriteHeader(status)
		_, _ = w.Write(b)
	}
}

// calls returns the requests the host received with the method and path given.
func (f *fakeHost) callsTo(method, path string) []fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []fakeCall
	for _, c := range f.calls {
		if c.method == method && c.path == path {
			out = append(out, c)
		}
	}
	return out
}

// allCalls is every request in the order the host received them.
func (f *fakeHost) allCalls() []fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeCall(nil), f.calls...)
}

const projPath = "/api/v4/projects/g%2Fp"

// testRepo is the project g/p on the fake host, with a fixed server token.
func testRepo(t *testing.T, f *fakeHost) *Repo {
	t.Helper()
	r, err := New("gitlab:"+f.srv.URL+"/g/p", Options{
		Token:      func(ctx context.Context, host string) (string, error) { return "server-token", nil },
		HTTP:       f.srv.Client(),
		Categorize: fakeCategorize,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// fakeCategorize is a stand-in for ci.Categorize: CI events are "ci", workflows named for pages are "pages", the rest
// "other". The real one is in internal/ci.
func fakeCategorize(run model.CIRun) string {
	if strings.Contains(strings.ToLower(run.Path+" "+run.Name), "pages") {
		return "pages"
	}
	switch run.Event {
	case "pull_request", "push", "workflow_dispatch", "merge_request_event":
		return "ci"
	}
	return "other"
}

// staticToken is an Options.Token that always gives the same token.
func staticToken(tok string) func(ctx context.Context, host string) (string, error) {
	return func(ctx context.Context, host string) (string, error) { return tok, nil }
}

// openStore is an empty store in a temporary folder.
func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "store.json"))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// mustOK fails the test on an error.
func mustOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// errorStatus is the provider status of an error, 0 when it is not a provider error.
func errorStatus(err error) int {
	var pe *provider.Error
	if errors.As(err, &pe) {
		return pe.Status
	}
	return 0
}
