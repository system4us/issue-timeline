package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/provider"
	"gitlab.com/system4us/issue-timeline/internal/store"
)

// fakeGraphQL is a GitHub that answers the sync's queries from lists of nodes, newest first, 30 issues or 20 pulls a page.
// A chain of cursors only holds the nodes of the states it asks for, as GitHub does.
type fakeGraphQL struct {
	mu            sync.Mutex
	issues        []map[string]any
	pulls         []map[string]any
	extraComments []map[string]any
	calls         []string
}

func (f *fakeGraphQL) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	defer f.mu.Unlock()
	var answer any
	switch {
	case strings.Contains(body.Query, "node(id"):
		f.calls = append(f.calls, "node")
		answer = map[string]any{"node": map[string]any{"comments": map[string]any{"pageInfo": map[string]any{"hasNextPage": false, "endCursor": nil}, "nodes": f.extraComments}}}
	case strings.Contains(body.Query, "pullRequests("):
		f.calls = append(f.calls, "pulls")
		answer = map[string]any{"repository": map[string]any{"pullRequests": page(f.pulls, 20, body.Variables)}}
	default:
		f.calls = append(f.calls, "issues")
		answer = map[string]any{"repository": map[string]any{"issues": page(f.issues, 30, body.Variables)}}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": answer})
}

func (f *fakeGraphQL) callsOf(kind string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == kind {
			n++
		}
	}
	return n
}

// page is one page of nodes in the requested states, from the cursor (an offset) in the variables.
func page(all []map[string]any, size int, vars map[string]any) map[string]any {
	var list []map[string]any
	states, _ := vars["states"].([]any)
	for _, n := range all {
		if len(states) == 0 {
			list = append(list, n)
			continue
		}
		for _, s := range states {
			if s == n["state"] {
				list = append(list, n)
			}
		}
	}
	start := 0
	if after, ok := vars["after"].(string); ok {
		fmt.Sscan(after, &start)
	}
	end := start + size
	if end > len(list) {
		end = len(list)
	}
	nodes := list[start:end]
	info := map[string]any{"hasNextPage": end < len(list), "endCursor": nil}
	if end < len(list) {
		info["endCursor"] = fmt.Sprint(end)
	}
	return map[string]any{"totalCount": len(list), "pageInfo": info, "nodes": nodes}
}

func fakeIssue(n int, updatedAt string, extra map[string]any) map[string]any {
	state := "CLOSED"
	if n%2 == 1 {
		state = "OPEN"
	}
	node := map[string]any{
		"id": fmt.Sprintf("I%d", n), "number": n, "title": fmt.Sprintf("issue %d", n), "body": "b", "url": fmt.Sprintf("u%d", n),
		"state": state, "stateReason": nil, "createdAt": "2026-01-01T00:00:00Z", "updatedAt": updatedAt, "closedAt": nil,
		"author":        map[string]any{"login": "ana"},
		"labels":        map[string]any{"nodes": []any{map[string]any{"name": "bug", "color": "d73a4a"}}},
		"assignees":     map[string]any{"nodes": []any{}},
		"milestone":     nil,
		"comments":      emptyConn(),
		"timelineItems": emptyConn(),
	}
	for k, v := range extra {
		node[k] = v
	}
	return node
}

func emptyConn() map[string]any {
	return map[string]any{"totalCount": 0, "pageInfo": map[string]any{"hasNextPage": false, "endCursor": nil}, "nodes": []any{}}
}

func fakePull(n int, updatedAt string) map[string]any {
	node := fakeIssue(n, updatedAt, nil)
	node["state"] = "MERGED"
	node["merged"] = true
	node["mergedAt"] = "2026-01-03T00:00:00Z"
	node["isDraft"] = false
	node["mergedBy"] = map[string]any{"login": "ana"}
	node["reviewDecision"] = nil
	node["baseRefName"] = "main"
	node["headRefName"] = fmt.Sprintf("b%d", n)
	node["additions"], node["deletions"], node["changedFiles"] = 3, 1, 1
	node["closingIssuesReferences"] = map[string]any{"nodes": []any{map[string]any{"number": 1}}}
	node["commits"] = map[string]any{"totalCount": 1, "pageInfo": map[string]any{"hasNextPage": false, "endCursor": nil}, "nodes": []any{
		map[string]any{"commit": map[string]any{"oid": "abc", "messageHeadline": "m", "committedDate": "2026-01-02T00:00:00Z", "authoredDate": "2026-01-02T00:00:00Z", "author": map[string]any{"user": map[string]any{"login": "ana"}}}},
	}}
	node["reviews"] = map[string]any{"totalCount": 1, "pageInfo": map[string]any{"hasNextPage": false, "endCursor": nil}, "nodes": []any{
		map[string]any{"author": map[string]any{"login": "bo"}, "state": "APPROVED", "submittedAt": "2026-01-02T12:00:00Z", "body": ""},
	}}
	return node
}

// syncRepo is the repo owner/name on a fake GitHub, with a store in a temp dir.
func syncRepo(t *testing.T, fake *fakeGraphQL) (*Repo, *store.Store, string) {
	t.Helper()
	r, _ := testRepo(t, fake)
	file := filepath.Join(t.TempDir(), "s.json")
	st, err := store.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	return r, st, file
}

func TestFirstSyncWalksEveryPageAndStoresTheRecords(t *testing.T) {
	var issues []map[string]any
	for i := 0; i < 65; i++ {
		issues = append(issues, fakeIssue(65-i, fmt.Sprintf("2026-02-%02dT00:00:00Z", 28-i%20), nil))
	}
	fake := &fakeGraphQL{issues: issues, pulls: []map[string]any{fakePull(100, "2026-02-10T00:00:00Z")}}
	r, st, file := syncRepo(t, fake)
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	entry, err := r.Sync(context.Background(), st, provider.SyncOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if entry.Issues != 65 || entry.Pulls != 1 || !entry.Full {
		t.Fatalf("entry: %+v", entry)
	}
	st.View(func(d *store.Data) {
		if len(d.Issues) != 65 {
			t.Errorf("issues stored: %d", len(d.Issues))
		}
		if d.Pulls["100"].State != "MERGED" {
			t.Errorf("pull state: %q", d.Pulls["100"].State)
		}
		if len(d.Pulls["100"].Closes) != 1 || d.Pulls["100"].Closes[0] != "1" {
			t.Errorf("closes: %v", d.Pulls["100"].Closes)
		}
		if d.Meta.FullSyncAt == nil || *d.Meta.FullSyncAt != "2026-03-01T00:00:00.000Z" {
			t.Errorf("fullSyncAt: %v", d.Meta.FullSyncAt)
		}
		if d.Meta.Repo == nil || *d.Meta.Repo != "o/r" {
			t.Errorf("meta.repo: %v", d.Meta.Repo)
		}
		if d.Meta.Totals == nil || d.Meta.Totals.Issues != 65 || d.Meta.Totals.Pulls != 1 {
			t.Errorf("totals: %+v", d.Meta.Totals)
		}
	})
	// the file on disk has the same records
	reopened, err := store.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.Data.Issues) != 65 {
		t.Fatalf("saved issues: %d", len(reopened.Data.Issues))
	}
}

func TestIncrementalSyncStopsAtTheFirstPageOlderThanTheLastSync(t *testing.T) {
	var issues []map[string]any
	for i := 0; i < 3; i++ {
		issues = append(issues, fakeIssue(100-i, "2026-03-02T00:00:00Z", nil))
	}
	for i := 0; i < 60; i++ {
		issues = append(issues, fakeIssue(60-i, "2026-01-05T00:00:00Z", nil))
	}
	fake := &fakeGraphQL{issues: issues}
	r, st, _ := syncRepo(t, fake)
	repo := "o/r"
	last := "2026-03-01T00:00:00.000Z"
	full := "2026-02-01T00:00:00.000Z"
	st.Update(func(d *store.Data) {
		d.Meta.Repo, d.Meta.LastSync, d.Meta.FullSyncAt = &repo, &last, &full
		d.Upsert("issues", storedIssue("1"))
	})
	now := time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC)
	entry, err := r.Sync(context.Background(), st, provider.SyncOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if entry.Full {
		t.Fatal("an incremental sync was reported as full")
	}
	if entry.Issues != 3 {
		t.Fatalf("only the three updated since the last sync are read, got %d", entry.Issues)
	}
	if n := fake.callsOf("issues"); n != 2 {
		t.Fatalf("one page per state (open, closed), got %d issue queries", n)
	}
	st.View(func(d *store.Data) {
		if d.Issues["100"] == nil || d.Issues["1"] == nil {
			t.Fatalf("records kept: %v", len(d.Issues))
		}
	})
}

// storedIssue is a minimal issue as a store keeps it.
func storedIssue(key string) *model.Record {
	n, _ := strconv.Atoi(key)
	return &model.Record{Kind: "issue", Key: key, Number: model.Ptr(n), Title: "stored " + key, State: "OPEN", ReadAt: "2026-01-01T00:00:00.000Z"}
}

func TestConnectionWithMoreThanOnePageIsDrained(t *testing.T) {
	first := map[string]any{"totalCount": 2, "pageInfo": map[string]any{"hasNextPage": true, "endCursor": "cursor1"}, "nodes": []any{
		map[string]any{"id": "c1", "author": map[string]any{"login": "a"}, "createdAt": "2026-02-01T00:00:00Z", "body": "x"},
	}}
	fake := &fakeGraphQL{
		issues:        []map[string]any{fakeIssue(1, "2026-02-01T00:00:00Z", map[string]any{"comments": first})},
		extraComments: []map[string]any{{"id": "c2", "author": map[string]any{"login": "b"}, "createdAt": "2026-02-02T00:00:00Z", "body": "y"}},
	}
	r, st, _ := syncRepo(t, fake)
	if _, err := r.Sync(context.Background(), st, provider.SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	st.View(func(d *store.Data) {
		var ids []string
		for _, c := range d.Issues["1"].Comments {
			ids = append(ids, c.ID)
		}
		if strings.Join(ids, ",") != "c1,c2" {
			t.Fatalf("comments: %v", ids)
		}
		if d.Issues["1"].CommentsCount != 2 {
			t.Fatalf("commentsCount: %d", d.Issues["1"].CommentsCount)
		}
	})
	if fake.callsOf("node") == 0 {
		t.Fatal("the rest of the connection was not read")
	}
}

func TestSyncOfAnotherRepoStartsAgain(t *testing.T) {
	fake := &fakeGraphQL{issues: []map[string]any{fakeIssue(1, "2026-02-01T00:00:00Z", nil)}}
	r, st, _ := syncRepo(t, fake)
	other, last := "x/y", "2026-02-02T00:00:00.000Z"
	st.Update(func(d *store.Data) {
		d.Meta.Repo, d.Meta.LastSync = &other, &last
		fullAt := "2026-02-02T00:00:00.000Z"
		d.Meta.FullSyncAt = &fullAt
		d.Upsert("issues", storedIssue("99"))
	})
	entry, err := r.Sync(context.Background(), st, provider.SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !entry.Full {
		t.Fatal("a store of another repo is read in full")
	}
	st.View(func(d *store.Data) {
		if d.Issues["99"] != nil {
			t.Fatal("the other repo's issues were kept")
		}
	})
}

func TestNormalizedEventsKeepTheirFields(t *testing.T) {
	raw := `{"id":"I1","number":1,"title":"t","body":null,"url":"u","state":"OPEN","createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-02T00:00:00Z",
	"author":null,"labels":null,"assignees":null,"milestone":null,"stateReason":null,"issueType":null,"parent":null,
	"subIssues":null,"blockedBy":null,"blocking":null,"comments":null,
	"timelineItems":{"totalCount":3,"pageInfo":{"hasNextPage":false,"endCursor":null},"nodes":[
	  {"__typename":"LabeledEvent","createdAt":"2026-01-03T00:00:00Z","actor":{"login":"ana"},"label":{"name":"bug"}},
	  {"__typename":"CrossReferencedEvent","createdAt":"2026-01-04T00:00:00Z","isCrossRepository":true,"source":{"__typename":"PullRequest","number":20}},
	  null]}}`
	var n issueNode
	if err := json.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatal(err)
	}
	rec := normalizeIssue(&n)
	if len(rec.Events) != 2 {
		t.Fatalf("null events are skipped: %d", len(rec.Events))
	}
	if rec.Events[0].Type != "Labeled" || rec.Events[0].Actor == nil || *rec.Events[0].Actor != "ana" {
		t.Fatalf("labeled: %+v", rec.Events[0])
	}
	var label string
	if !rec.Events[0].Field("label", &label) || label != "bug" {
		t.Fatalf("label field: %q", label)
	}
	var source int
	var kind string
	var external bool
	if !rec.Events[1].Field("source", &source) || source != 20 || !rec.Events[1].Field("sourceKind", &kind) || kind != "PullRequest" {
		t.Fatalf("cross reference: %+v", rec.Events[1])
	}
	if !rec.Events[1].Field("external", &external) || !external {
		t.Fatal("external")
	}
}

func TestANullConnectionReadsAsEmpty(t *testing.T) {
	raw := `{"id":"I3","number":3,"title":"t","body":null,"url":"u","state":"OPEN","createdAt":"2026-01-01T00:00:00Z","closedAt":null,
	"author":{"login":"a"},"labels":null,"assignees":null,"milestone":null,"comments":null,"timelineItems":null,
	"parent":null,"subIssues":null,"blockedBy":null,"blocking":null,"stateReason":null}`
	var n issueNode
	if err := json.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatal(err)
	}
	rec := normalizeIssue(&n)
	if len(rec.Labels) != 0 || rec.Labels == nil || len(rec.Events) != 0 || len(rec.BlockedBy) != 0 {
		t.Fatalf("null connections: labels=%v events=%v blockedBy=%v", rec.Labels, rec.Events, rec.BlockedBy)
	}
	if rec.Body != "" || rec.Author == nil || *rec.Author != "a" {
		t.Fatalf("body or author: %q %v", rec.Body, rec.Author)
	}
}
