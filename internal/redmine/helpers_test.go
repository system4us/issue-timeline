package redmine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/provider"
	"gitlab.com/system4us/issue-timeline/internal/store"
)

// closedStatuses are the statuses of the tests: New, In progress (open) and Closed.
var closedStatuses = map[int]bool{1: false, 2: false, 5: true}

const testOrigin = "https://rm.example.com"

// rawIssue is a Redmine issue as the REST API gives it, as the JSON the fake server answers with.
func rawIssue(id int, extra map[string]any) map[string]any {
	m := map[string]any{
		"id": id, "subject": fmt.Sprintf("issue %d", id), "description": "d",
		"status": map[string]any{"id": 1, "name": "New"}, "tracker": map[string]any{"id": 1, "name": "Bug"},
		"author": map[string]any{"id": 9, "name": "Ana"}, "assigned_to": nil,
		"created_on": "2026-01-01T00:00:00Z", "updated_on": "2026-01-02T00:00:00Z", "closed_on": nil,
		"journals": []any{}, "relations": []any{}, "children": []any{},
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

// parseIssue reads a JSON object the way the client reads an issue.
func parseIssue(t *testing.T, m map[string]any) rmIssue {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var raw rmIssue
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

// normalize is the record of a JSON issue, with the test's statuses and origin.
func normalize(t *testing.T, m map[string]any) *model.Record {
	t.Helper()
	return normalizeIssue(parseIssue(t, m), normCtx{closed: closedStatuses, origin: testOrigin})
}

// jsonEq checks that got is the JSON want, whatever the key order.
func jsonEq(t *testing.T, got []byte, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("body is not JSON: %s", got)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("body\n got %s\nwant %s", got, want)
	}
}

// respond writes a JSON answer.
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

// fakeRedmine is a Redmine server for one test, and the repo key of its project "backend". The handler runs
// concurrently: the test guards what it records.
func fakeRedmine(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return "redmine:" + srv.URL + "/backend"
}

// newStore is an empty store in a temporary folder.
func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.json"))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// fixedNow is a clock for the sync's times.
func fixedNow(s string) func() time.Time {
	t := mustTime(s)
	return func() time.Time { return t }
}

func mustTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// keyOf is the server's own key for the tests.
func keyOf(key string) func(context.Context) (string, error) {
	return func(context.Context) (string, error) { return key, nil }
}

// recorder keeps the requests a fake server saw; safe for concurrent handlers.
type recorder struct {
	mu    sync.Mutex
	calls []call
}

type call struct {
	Method string
	Path   string
	Query  map[string]string
	Key    string
	Body   []byte
}

func (rec *recorder) add(r *http.Request, body []byte) {
	q := map[string]string{}
	for k, v := range r.URL.Query() {
		q[k] = v[0]
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.calls = append(rec.calls, call{Method: r.Method, Path: r.URL.Path, Query: q, Key: r.Header.Get("X-Redmine-API-Key"), Body: body})
}

func (rec *recorder) all() []call {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return append([]call(nil), rec.calls...)
}

func (rec *recorder) reset() {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.calls = nil
}

// errCode is the provider status of an error, 0 when it is not a provider error.
func errCode(err error) int {
	var pe *provider.Error
	if errors.As(err, &pe) {
		return pe.Status
	}
	return 0
}
