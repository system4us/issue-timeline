package github

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/provider"
)

// testRepo is the repo owner/name on a test server answering with h. Its token is "t", its retries do not wait, and
// the classifier and categorizer are stubs.
func testRepo(t *testing.T, h http.Handler) (*Repo, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	r := New("o", "r", Options{
		Token:     func(context.Context) (string, error) { return "t", nil },
		HTTP:      srv.Client(),
		Endpoints: Endpoints{Host: "github.test", REST: srv.URL, GraphQL: srv.URL + "/graphql"},
		ClassifyMerge: func(parents int, title, prTitle string, number int, prCommitTitles []string) string {
			return "classified"
		},
		Categorize: func(model.CIRun) string { return "ci" },
		pause:      func(context.Context, time.Duration) error { return nil },
	})
	return r, srv
}

// changes builds a provider.Changes from Go values, as the API decodes them.
func changes(t *testing.T, m map[string]any) provider.Changes {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var c provider.Changes
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

// call is one request a fake host got.
type call struct {
	Method string
	Path   string
	Query  string
	Body   string
}

// fakeHost answers REST and GraphQL requests from a table ("METHOD /path" -> status and body) and records every call.
// A request with no entry gets 200 and {}.
type fakeHost struct {
	mu     sync.Mutex
	routes map[string]route
	calls  []call
}

type route struct {
	status int
	body   string
}

func (f *fakeHost) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r.Body)
	f.mu.Lock()
	f.calls = append(f.calls, call{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: buf.String()})
	rt, ok := f.routes[r.Method+" "+r.URL.Path]
	f.mu.Unlock()
	if !ok {
		rt = route{status: 200, body: "{}"}
	}
	w.Header().Set("content-type", "application/json")
	if rt.status == 0 {
		rt.status = 200
	}
	w.WriteHeader(rt.status)
	_, _ = w.Write([]byte(rt.body))
}

// got returns the recorded calls.
func (f *fakeHost) got() []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]call(nil), f.calls...)
}

func ptrStr(s string) *string { return &s }
