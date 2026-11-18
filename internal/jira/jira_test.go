package jira

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/provider"
	"gitlab.com/system4us/issue-timeline/internal/store"
)

const (
	testSite = "https://acme.atlassian.net"
	repoKey  = "jira:" + testSite + "/PROJ"
)

var done = map[string]bool{"Done": true, "Closed": true}

func adf(text string) map[string]any {
	return map[string]any{"type": "doc", "version": 1, "content": []any{
		map[string]any{"type": "paragraph", "content": []any{map[string]any{"type": "text", "text": text}}},
	}}
}

// issueJSON is a Jira issue as the REST API gives it: the fields the sync asks for, with defaults the tests change.
func issueJSON(key string, fields map[string]any, extra map[string]any) map[string]any {
	n := 0
	fmt.Sscanf(strings.TrimPrefix(key, "PROJ-"), "%d", &n)
	f := map[string]any{
		"summary": "task " + key, "description": adf("body"),
		"status": map[string]any{"name": "To Do", "statusCategory": map[string]any{"key": "new"}},
		"labels": []any{}, "created": "2026-01-01T10:00:00.000+0000", "updated": "2026-01-02T10:00:00.000+0000",
		"resolutiondate": nil, "issuelinks": []any{}, "subtasks": []any{},
		"comment": map[string]any{"total": 0, "comments": []any{}}, "fixVersions": []any{},
	}
	for k, v := range fields {
		f[k] = v
	}
	m := map[string]any{"id": fmt.Sprint(10000 + n), "key": key, "fields": f}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

// parseIssue reads a JSON issue the way the client reads one.
func parseIssue(t *testing.T, m map[string]any) jiraIssue {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var raw jiraIssue
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

func normalize(t *testing.T, m map[string]any) *model.Record {
	t.Helper()
	r, err := normalizeIssue(parseIssue(t, m), jiraNorm{done: done, origin: testSite})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

// recorder keeps what a fake Jira saw; safe for concurrent handlers.
type recorder struct {
	mu    sync.Mutex
	calls []call
}

type call struct {
	Path  string
	Query map[string]string
	Auth  string
}

func (rec *recorder) add(r *http.Request) {
	q := map[string]string{}
	for k, v := range r.URL.Query() {
		q[k] = v[0]
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.calls = append(rec.calls, call{Path: r.URL.Path, Query: q, Auth: r.Header.Get("Authorization")})
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

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.json"))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func fixedNow(s string) func() time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return func() time.Time { return t }
}

var creds = func(context.Context) (string, string, error) { return "ana@example.com", "tok", nil }

func newRepo(t *testing.T, h http.HandlerFunc) *Repo {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	// the repo key names the test server's origin
	r, err := New("jira:"+srv.URL+"/PROJ", Options{Creds: creds})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func errCode(err error) int {
	var pe *provider.Error
	if errors.As(err, &pe) {
		return pe.Status
	}
	return 0
}

func TestRepoKeyCarriesSiteAndProjectKey(t *testing.T) {
	origin, project, err := ParseRepo(repoKey)
	if err != nil || origin != testSite || project != "PROJ" {
		t.Fatalf("ParseRepo: %q %q %v", origin, project, err)
	}
	if got, err := KeyFromURL(testSite + "/jira/software/projects/proj/board"); err != nil || got != repoKey {
		t.Fatalf("KeyFromURL: %q %v", got, err)
	}
	if _, _, err := ParseRepo("jira:https://acme.atlassian.net/proj"); err == nil || !strings.Contains(err.Error(), "project key") {
		t.Fatalf("a lowercase key is refused: %v", err)
	}
	if _, _, err := ParseRepo("redmine:https://x/y"); err == nil {
		t.Fatal("a Redmine key is not a Jira key")
	}
}

func TestDescriptionsAndCommentsAreReadOutOfADF(t *testing.T) {
	if got := adfOf(json.RawMessage(`{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"hello"}]}]}`)); got != "hello" {
		t.Fatalf("hello: %q", got)
	}
	two := map[string]any{"type": "doc", "content": []any{
		map[string]any{"type": "paragraph", "content": []any{map[string]any{"type": "text", "text": "a"}}},
		map[string]any{"type": "paragraph", "content": []any{map[string]any{"type": "text", "text": "b"}}},
	}}
	b, _ := json.Marshal(two)
	if got := adfOf(b); got != "a\nb" {
		t.Fatalf("two paragraphs: %q", got)
	}
}

func TestTheKeyIsTextAndDoneClosesAndChangelogGivesEvents(t *testing.T) {
	r := normalize(t, issueJSON("PROJ-12",
		map[string]any{"status": map[string]any{"name": "Closed", "statusCategory": map[string]any{"key": "done"}}, "resolutiondate": "2026-01-05T10:00:00.000+0000"},
		map[string]any{"changelog": map[string]any{"histories": []any{
			map[string]any{"id": "1", "author": map[string]any{"displayName": "Bea"}, "created": "2026-01-03T10:00:00.000+0000",
				"items": []any{map[string]any{"field": "status", "fromString": "In Progress", "toString": "Done"}}},
			map[string]any{"id": "2", "author": map[string]any{"displayName": "Bea"}, "created": "2026-01-04T10:00:00.000+0000",
				"items": []any{map[string]any{"field": "status", "fromString": "Done", "toString": "Reopened by QA"}}},
		}}}))
	if r.Key != "PROJ-12" || r.Number != nil {
		t.Fatalf("key %q number %v", r.Key, r.Number)
	}
	if r.URL != testSite+"/browse/PROJ-12" {
		t.Fatalf("url %q", r.URL)
	}
	if r.CreatedAt != "2026-01-01T10:00:00.000+00:00" {
		t.Fatalf("Jira's offset is made ISO: %q", r.CreatedAt)
	}
	if r.State != "CLOSED" {
		t.Fatalf("state %s", r.State)
	}
	var types []string
	for _, e := range r.Events {
		types = append(types, e.Type)
	}
	if strings.Join(types, ",") != "Closed,Reopened" {
		t.Fatalf("events: %v", types)
	}
}

func TestBlocksLinksReadBothWaysAndTheTreeAndVersion(t *testing.T) {
	r := normalize(t, issueJSON("PROJ-3", map[string]any{
		"parent": map[string]any{"key": "PROJ-1"}, "subtasks": []any{map[string]any{"key": "PROJ-4"}},
		"issuelinks": []any{
			map[string]any{"type": map[string]any{"name": "Blocks"}, "outwardIssue": map[string]any{"key": "PROJ-5"}},  // PROJ-3 blocks PROJ-5
			map[string]any{"type": map[string]any{"name": "Blocks"}, "inwardIssue": map[string]any{"key": "PROJ-6"}},   // PROJ-6 blocks PROJ-3
			map[string]any{"type": map[string]any{"name": "Relates"}, "outwardIssue": map[string]any{"key": "PROJ-7"}}, // not a dependency
		},
		"fixVersions": []any{map[string]any{"name": "2.0", "releaseDate": "2026-03-01"}},
		"labels":      []any{"backend"},
		"assignee":    map[string]any{"displayName": "Caro"},
	}, nil))
	if len(r.Blocking) != 1 || r.Blocking[0] != "PROJ-5" {
		t.Fatalf("blocking: %v", r.Blocking)
	}
	if len(r.BlockedBy) != 1 || r.BlockedBy[0] != "PROJ-6" {
		t.Fatalf("blocked by: %v", r.BlockedBy)
	}
	if r.Parent == nil || *r.Parent != "PROJ-1" {
		t.Fatalf("parent: %v", r.Parent)
	}
	if len(r.SubIssues) != 1 || r.SubIssues[0] != "PROJ-4" {
		t.Fatalf("subissues: %v", r.SubIssues)
	}
	if r.Milestone == nil || r.Milestone.Title != "2.0" || r.Milestone.DueOn == nil || *r.Milestone.DueOn != "2026-03-01" {
		t.Fatalf("milestone: %+v", r.Milestone)
	}
	if len(r.Labels) != 1 || r.Labels[0].Name != "backend" || r.Labels[0].Color != "b6c5d2" {
		t.Fatalf("labels: %+v", r.Labels)
	}
	if len(r.Assignees) != 1 || r.Assignees[0] != "Caro" {
		t.Fatalf("assignees: %v", r.Assignees)
	}
}

func TestPrivateCommentsAndHistoryKeepTheirAuthors(t *testing.T) {
	r := normalize(t, issueJSON("PROJ-40", map[string]any{
		"comment": map[string]any{"total": 3, "comments": []any{
			map[string]any{"id": "500", "author": map[string]any{"displayName": "Bea"}, "created": "2026-01-02T10:00:00.000+0000", "body": adf("first")},
		}},
	}, nil))
	if r.CommentsCount != 3 || len(r.Comments) != 1 || r.Comments[0].ID != "500" || r.Comments[0].Body != "first" {
		t.Fatalf("comments: %d %+v", r.CommentsCount, r.Comments)
	}
	if r.Comments[0].Author == nil || *r.Comments[0].Author != "Bea" {
		t.Fatalf("author: %v", r.Comments[0].Author)
	}
}

func TestSyncPagesTheSearchReadsChangelogsAndSendsBasicAuth(t *testing.T) {
	var rec recorder
	r := newRepo(t, func(w http.ResponseWriter, req *http.Request) {
		rec.add(req)
		switch {
		case req.URL.Path == "/rest/api/3/project/PROJ":
			respond(w, 200, map[string]any{"id": "10001", "key": "PROJ", "name": "Project"})
		case req.URL.Path == "/rest/api/3/status":
			respond(w, 200, []any{map[string]any{"name": "Done", "statusCategory": map[string]any{"key": "done"}}, map[string]any{"name": "To Do", "statusCategory": map[string]any{"key": "new"}}})
		case req.URL.Path == "/rest/api/3/search/jql":
			if req.URL.Query().Get("nextPageToken") == "" {
				respond(w, 200, map[string]any{"issues": []any{map[string]any{"key": "PROJ-1"}, map[string]any{"key": "PROJ-2"}}, "nextPageToken": "T2", "isLast": false})
				return
			}
			respond(w, 200, map[string]any{"issues": []any{map[string]any{"key": "PROJ-3"}}, "isLast": true})
		case strings.HasPrefix(req.URL.Path, "/rest/api/3/issue/"):
			key := strings.TrimPrefix(req.URL.Path, "/rest/api/3/issue/")
			respond(w, 200, issueJSON(key, nil, map[string]any{"changelog": map[string]any{"histories": []any{}}}))
		default:
			respond(w, 404, nil)
		}
	})
	st := newStore(t)
	entry, err := r.Sync(context.Background(), st, provider.SyncOptions{Now: fixedNow("2026-01-03T00:00:00Z")})
	if err != nil {
		t.Fatal(err)
	}
	if !entry.Full || entry.Issues != 3 {
		t.Fatalf("all three pages' issues are read: %+v", entry)
	}
	if st.Data.Issues["PROJ-1"] == nil || st.Data.Issues["PROJ-3"] == nil {
		t.Fatal("issues by key")
	}
	if len(st.Data.Pulls) != 0 {
		t.Fatal("a tracker has no pull requests")
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("ana@example.com:tok"))
	var search []call
	for _, c := range rec.all() {
		if c.Auth != want {
			t.Fatalf("call %s without the auth", c.Path)
		}
		if c.Path == "/rest/api/3/search/jql" {
			search = append(search, c)
		}
	}
	if search[1].Query["nextPageToken"] != "T2" {
		t.Fatal("the second page asks with the token, not a start index")
	}
	if search[0].Query["jql"] != `project = "PROJ" ORDER BY updated DESC` {
		t.Fatalf("jql: %q", search[0].Query["jql"])
	}

	rec.reset()
	if _, err := r.Sync(context.Background(), st, provider.SyncOptions{Now: fixedNow("2026-01-04T00:00:00Z")}); err != nil {
		t.Fatal(err)
	}
	for _, c := range rec.all() {
		if c.Path == "/rest/api/3/search/jql" {
			if !strings.HasPrefix(c.Query["jql"], `project = "PROJ" AND updated >= "2026-01-02 `) {
				t.Fatalf("the incremental JQL starts at the last sync, with a margin: %q", c.Query["jql"])
			}
			return
		}
	}
	t.Fatal("no search in the incremental sync")
}

func TestWithoutCredentialsTheSyncSaysSoAndAnUnknownProjectIsMissing(t *testing.T) {
	noCreds, _ := New(repoKey, Options{Creds: func(context.Context) (string, string, error) { return "", "", nil }})
	if _, err := noCreds.Sync(context.Background(), newStore(t), provider.SyncOptions{}); err == nil || !strings.Contains(err.Error(), "no Jira credentials") {
		t.Fatalf("missing credentials: %v", err)
	}
	notFound := newRepo(t, func(w http.ResponseWriter, r *http.Request) { respond(w, 404, nil) })
	if err := notFound.Exists(context.Background()); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("an unknown project is ErrNotFound: %v", err)
	}
	refused := newRepo(t, func(w http.ResponseWriter, r *http.Request) { respond(w, 401, nil) })
	err := refused.Exists(context.Background())
	if err == nil || errors.Is(err, provider.ErrNotFound) || !strings.Contains(err.Error(), "refused the credentials") {
		t.Fatalf("refused credentials are an error that says so: %v", err)
	}
}

func TestDueDateIsJiraDuedateAndThereIsNoStartDateWithoutAField(t *testing.T) {
	r := normalize(t, issueJSON("PROJ-20", map[string]any{"duedate": "2027-04-15"}, nil))
	if r.DueDate == nil || *r.DueDate != "2027-04-15" {
		t.Fatalf("due: %v", r.DueDate)
	}
	if r.StartDate != nil {
		t.Fatal("no start date without the site's field")
	}
	if normalize(t, issueJSON("PROJ-21", nil, nil)).DueDate != nil {
		t.Fatal("no due date when Jira has none")
	}
}

func TestIncrementalSyncReadsOnlyIssuesUpdatedSinceTheLastOne(t *testing.T) {
	var mu sync.Mutex
	var detailed []string
	r := newRepo(t, func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.URL.Path == "/rest/api/3/project/PROJ":
			respond(w, 200, map[string]any{"id": "1", "key": "PROJ", "name": "P"})
		case req.URL.Path == "/rest/api/3/status":
			respond(w, 200, []any{map[string]any{"name": "Done", "statusCategory": map[string]any{"key": "done"}}})
		case req.URL.Path == "/rest/api/3/search/jql":
			// Jira's own times, in the account's zone (-03:00): one recent, one from long before the last sync.
			respond(w, 200, map[string]any{"issues": []any{
				map[string]any{"key": "PROJ-1", "fields": map[string]any{"updated": "2026-01-04T10:00:00.000-0300"}},
				map[string]any{"key": "PROJ-2", "fields": map[string]any{"updated": "2025-12-01T10:00:00.000-0300"}},
			}, "isLast": true})
		case strings.HasPrefix(req.URL.Path, "/rest/api/3/issue/"):
			key := strings.TrimPrefix(req.URL.Path, "/rest/api/3/issue/")
			mu.Lock()
			detailed = append(detailed, key)
			mu.Unlock()
			respond(w, 200, issueJSON(key, nil, map[string]any{"changelog": map[string]any{"histories": []any{}}}))
		default:
			respond(w, 404, nil)
		}
	})
	st := newStore(t)
	ctx := context.Background()
	if _, err := r.Sync(ctx, st, provider.SyncOptions{Now: fixedNow("2026-01-03T00:00:00Z")}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	detailed = nil
	mu.Unlock()
	if _, err := r.Sync(ctx, st, provider.SyncOptions{Now: fixedNow("2026-01-04T12:00:00Z")}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(detailed) != 1 || detailed[0] != "PROJ-1" {
		t.Fatalf("the issue updated long before the last sync is not read again: %v", detailed)
	}
}

func TestStartDateIsTheSitesStartDateFieldFoundByName(t *testing.T) {
	withField := normalizeOf(t, issueJSON("PROJ-30", map[string]any{"customfield_10015": "2026-10-01"}, nil), "customfield_10015")
	if withField.StartDate == nil || *withField.StartDate != "2026-10-01" {
		t.Fatalf("start: %v", withField.StartDate)
	}
	if normalizeOf(t, issueJSON("PROJ-31", map[string]any{"customfield_10015": "2026-10-01"}, nil), "").StartDate != nil {
		t.Fatal("without the field the start date stays empty")
	}
	fieldsOf := func(list []any) *Repo {
		return newRepo(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/rest/api/3/field" {
				respond(w, 200, list)
				return
			}
			respond(w, 404, nil)
		})
	}
	id := startFieldID(context.Background(), mustClient(t, fieldsOf([]any{
		map[string]any{"id": "customfield_1", "name": "Project start", "schema": map[string]any{"type": "string"}},
		map[string]any{"id": "customfield_10015", "name": "Start date", "schema": map[string]any{"type": "date"}},
	})))
	if id != "customfield_10015" {
		t.Fatalf("the date field named 'Start date' is picked, not a text field with a similar name: %q", id)
	}
	none := startFieldID(context.Background(), mustClient(t, fieldsOf([]any{
		map[string]any{"id": "duedate", "name": "Due date", "schema": map[string]any{"type": "date"}},
	})))
	if none != "" {
		t.Fatalf("no start field: %q", none)
	}
}

func normalizeOf(t *testing.T, m map[string]any, startField string) *model.Record {
	t.Helper()
	r, err := normalizeIssue(parseIssue(t, m), jiraNorm{done: done, origin: testSite, startField: startField})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// mustClient is the client of a test repo, for the calls the tests make directly.
func mustClient(t *testing.T, r *Repo) client {
	t.Helper()
	c, err := r.client(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRefreshIsNotSupportedAndTheKeyIsTheOneItWasMadeWith(t *testing.T) {
	r, err := New(repoKey, Options{Creds: creds})
	if err != nil || r.Key() != repoKey || r.Provider() != "jira" || r.Origin() != testSite {
		t.Fatalf("repo: %v %v %v %v", r.Key(), r.Provider(), r.Origin(), err)
	}
	if _, err := r.Refresh(context.Background(), "issue", &model.Record{}); !errors.Is(err, provider.ErrNotSupported) {
		t.Fatalf("Refresh: %v", err)
	}
}

func TestIsoAndTimesReadJiraZones(t *testing.T) {
	if got := isoJira("2026-01-01T10:00:00.000+0000"); got != "2026-01-01T10:00:00.000+00:00" {
		t.Fatalf("iso: %q", got)
	}
	if got := isoJira("2026-01-01T10:00:00.000Z"); got != "2026-01-01T10:00:00.000Z" {
		t.Fatalf("a Z stays: %q", got)
	}
	if parseJiraTime("2026-01-04T10:00:00.000-0300").IsZero() {
		t.Fatal("a Jira zone is read")
	}
	if got := jqlTime(time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)); got != "2026-01-02 00:00" {
		t.Fatalf("jql time: %q", got)
	}
}
