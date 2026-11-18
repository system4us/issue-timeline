package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/provider"
)

// ---- the repo key

func TestParseRepoReadsTheKeyForms(t *testing.T) {
	origin, host, project, apiBase, err := ParseRepo("gitlab:https://gitlab.example.com/grp/sub/proj")
	mustOK(t, err)
	if origin != "https://gitlab.example.com" || host != "gitlab.example.com" || project != "grp/sub/proj" || apiBase != "https://gitlab.example.com/api/v4" {
		t.Fatalf("got %q %q %q %q", origin, host, project, apiBase)
	}
	origin, host, project, _, err = ParseRepo("group/project")
	mustOK(t, err)
	if origin != "https://gitlab.com" || host != "gitlab.com" || project != "group/project" {
		t.Fatalf("a bare key is on gitlab.com: got %q %q %q", origin, host, project)
	}
	for _, bad := range []string{"", "gitlab:", "https://gitlab.com/", "gitlab:https://gitlab.com/a//b", "ftp://gitlab.com/a/b", "gitlab:https://-host/a/b", "gitlab:https://h x/a/b"} {
		if _, _, _, _, err := ParseRepo(bad); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
	r, err := New("gitlab:https://gitlab.example.com/grp/proj", Options{Categorize: fakeCategorize, Token: staticToken("t")})
	mustOK(t, err)
	if r.Key() != "gitlab:https://gitlab.example.com/grp/proj" || r.Provider() != "gitlab" || r.Origin() != "https://gitlab.example.com" {
		t.Fatalf("key %q provider %q origin %q", r.Key(), r.Provider(), r.Origin())
	}
	if _, err := New("group/project", Options{}); err == nil {
		t.Fatal("New without Categorize must fail")
	}
}

// ---- the diff line counts

func TestDiffLineCountsMatchTheJavaScriptRegexes(t *testing.T) {
	// the same diff, counted with /^\+(?!\+\+)/gm and /^-(?!--)/gm: 4 additions and 3 deletions
	diff := "+a\n+b\n-c\n+++ x\n--- y\n++z\n--w\n+\n-"
	if got := countLines(diff, "+", "+++"); got != 4 {
		t.Errorf("additions: %d", got)
	}
	if got := countLines(diff, "-", "---"); got != 3 {
		t.Errorf("deletions: %d", got)
	}
	if countLines("", "+", "+++") != 0 {
		t.Error("an empty diff has no lines")
	}
}

// ---- the token

// scriptedRun is a runner that answers from a function and records the commands. gate, when set, holds the calls for
// the host named in gateHost until it is closed.
type scriptedRun struct {
	mu       sync.Mutex
	calls    []string
	gate     chan struct{}
	gateHost string
	answer   func(name string, args []string) (string, string, error)
}

func (s *scriptedRun) run(ctx context.Context, name string, args ...string) (string, string, error) {
	s.mu.Lock()
	s.calls = append(s.calls, strings.Join(append([]string{name}, args...), " "))
	s.mu.Unlock()
	if s.gate != nil && len(args) > 0 && strings.Contains(strings.Join(args, " "), s.gateHost) {
		select {
		case <-s.gate:
		case <-ctx.Done():
			return "", "", ctx.Err()
		}
	}
	return s.answer(name, args)
}

func (s *scriptedRun) runs() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

const quickToken = "abcdefghijklmnopqrstuvwx"

func TestTokenFromTheEnvironmentNeedsNoGlab(t *testing.T) {
	t.Setenv("GITLAB_TOKEN_GITLAB_EXAMPLE_COM", "env-token")
	run := &scriptedRun{answer: func(string, []string) (string, string, error) { return "", "", errors.New("glab must not run") }}
	src := tokenSourceWith(newTokenCache(), run.run)
	tok, err := src(context.Background(), "gitlab.example.com")
	mustOK(t, err)
	if tok != "env-token" || run.runs() != 0 {
		t.Fatalf("token %q after %d glab runs", tok, run.runs())
	}
	if name := identitySource("gitlab.example.com"); name != "GITLAB_TOKEN_GITLAB_EXAMPLE_COM" {
		t.Fatalf("the source is the variable: %q", name)
	}
}

func TestGitlabComTakesTheGenericVariable(t *testing.T) {
	t.Setenv("GITLAB_TOKEN", "generic")
	if tok := envToken("gitlab.com"); tok != "generic" {
		t.Fatalf("got %q", tok)
	}
	if tok := envToken("other.example"); tok != "" {
		t.Fatalf("the generic token is for gitlab.com only, got %q", tok)
	}
}

func TestGlabConfigTokenIsTheQuickPath(t *testing.T) {
	run := &scriptedRun{answer: func(name string, args []string) (string, string, error) {
		if strings.Join(args, " ") == "config get token --host h.example" {
			return quickToken + "\n", "", nil
		}
		return "", "", errors.New("auth status must not run when config has the token")
	}}
	src := tokenSourceWith(newTokenCache(), run.run)
	tok, err := src(context.Background(), "h.example")
	mustOK(t, err)
	if tok != quickToken || run.runs() != 1 {
		t.Fatalf("token %q after %d runs", tok, run.runs())
	}
}

func TestGlabAuthStatusIsTheFallbackAndIsRetriedOnce(t *testing.T) {
	statusCalls := 0
	run := &scriptedRun{answer: func(name string, args []string) (string, string, error) {
		if args[0] == "config" {
			return "", "", errors.New("exit 1")
		}
		statusCalls++
		if statusCalls == 1 {
			return "", "", errors.New("keyring busy") // no "Token found": retried
		}
		return "", "Token found: glpat-0123456789abcdefghij (xyz)\n", nil
	}}
	src := tokenSourceWith(newTokenCache(), run.run)
	tok, err := src(context.Background(), "h.example")
	mustOK(t, err)
	if tok != "glpat-0123456789abcdefghij" || statusCalls != 2 {
		t.Fatalf("token %q after %d status calls", tok, statusCalls)
	}
}

func TestGlabWithNoSessionIsAnError(t *testing.T) {
	run := &scriptedRun{answer: func(string, []string) (string, string, error) { return "", "", errors.New("no glab") }}
	src := tokenSourceWith(newTokenCache(), run.run)
	if _, err := src(context.Background(), "h.example"); err == nil || !strings.Contains(err.Error(), "glab auth login") {
		t.Fatalf("want the login hint, got %v", err)
	}
}

func TestConcurrentCallersShareOneGlabRun(t *testing.T) {
	gate := make(chan struct{})
	run := &scriptedRun{gate: gate, gateHost: "slow.example", answer: func(name string, args []string) (string, string, error) {
		return quickToken, "", nil
	}}
	cache := newTokenCache()
	src := tokenSourceWith(cache, run.run)
	var wg sync.WaitGroup
	results := make([]string, 10)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tok, err := src(context.Background(), "slow.example")
			if err != nil {
				t.Error(err)
			}
			results[i] = tok
		}(i)
	}
	// while the one run for slow.example is held, another host is not blocked by it (no lock is held during a command)
	fast, err := src(context.Background(), "fast.example")
	mustOK(t, err)
	if fast != quickToken {
		t.Fatalf("fast host token %q", fast)
	}
	time.Sleep(20 * time.Millisecond)
	close(gate)
	wg.Wait()
	for _, tok := range results {
		if tok != quickToken {
			t.Fatalf("every caller gets the token, got %q", tok)
		}
	}
	if n := run.runs(); n != 2 { // one run for each host: the slow one and the fast one
		t.Fatalf("want 2 runs (one per host), got %d: %v", n, run.calls)
	}
}

func TestACallerThatLeavesStopsItsLoadAndTheNextCallerMakesAnother(t *testing.T) {
	gate := make(chan struct{})
	run := &scriptedRun{gate: gate, gateHost: "slow.example", answer: func(string, []string) (string, string, error) { return quickToken, "", nil }}
	src := tokenSourceWith(newTokenCache(), run.run)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := src(ctx, "slow.example"); done <- err }()
	time.Sleep(10 * time.Millisecond)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("the caller that left gets its context error, got %v", err)
	}
	before := run.runs()
	close(gate)
	tok, err := src(context.Background(), "slow.example")
	mustOK(t, err)
	if tok != quickToken || run.runs() <= before {
		t.Fatalf("the next caller makes a load of its own: %q, runs %d after %d", tok, run.runs(), before)
	}
}

func TestATokenIsKeptFor20MinutesAndForgotten(t *testing.T) {
	run := &scriptedRun{answer: func(string, []string) (string, string, error) { return quickToken, "", nil }}
	cache := newTokenCache()
	src := tokenSourceWith(cache, run.run)
	for i := 0; i < 3; i++ {
		_, err := src(context.Background(), "h.example")
		mustOK(t, err)
	}
	if run.runs() != 1 {
		t.Fatalf("the cached token is used: %d runs", run.runs())
	}
	cache.forget("h.example")
	_, err := src(context.Background(), "h.example")
	mustOK(t, err)
	if run.runs() != 2 {
		t.Fatalf("a forgotten token is read again: %d runs", run.runs())
	}
}

// ---- the REST client: retries, paging, the event cooldown

func TestRetryAfterIsWaitedAndA5xxIsRetried(t *testing.T) {
	f := newFakeHost(t)
	hits := 0
	f.route("GET", projPath+"/labels", func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		jsonReply(200, []glLabel{{Name: "bug"}})(w, r)
	})
	r := testRepo(t, f)
	c := r.newClient("t", false, nil)
	labels, err := listPages[glLabel](context.Background(), c, r.projectPath("labels"), nil)
	mustOK(t, err)
	if len(labels) != 1 || hits != 2 {
		t.Fatalf("labels %v after %d hits", labels, hits)
	}
}

func TestPagesFollowTheHostsNextPageNumberOnly(t *testing.T) {
	f := newFakeHost(t)
	f.route("GET", projPath+"/issues", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("page") {
		case "1":
			// a link to another host is not followed; the next page is the X-Next-Page number
			w.Header().Set("X-Next-Page", "2")
			w.Header().Set("Link", `<https://evil.example/steal?page=2>; rel="next"`)
			jsonReply(200, []glRef{{IID: 1}})(w, r)
		case "2":
			jsonReply(200, []glRef{{IID: 2}})(w, r)
		}
	})
	r := testRepo(t, f)
	c := r.newClient("t", false, nil)
	rows, err := listPages[glRef](context.Background(), c, r.projectPath("issues"), nil)
	mustOK(t, err)
	if len(rows) != 2 || rows[1].IID != 2 {
		t.Fatalf("rows %v", rows)
	}
	for _, call := range f.allCalls() {
		if !strings.HasPrefix(call.path, "/api/v4/") {
			t.Fatalf("a request left the API: %s", call.path)
		}
	}
}

func TestAnEventEndpointRateLimitStartsACooldownAndIsLoggedOnce(t *testing.T) {
	f := newFakeHost(t)
	f.route("GET", projPath+"/issues/3/resource_state_events", jsonReply(429, map[string]string{"message": "slow down"}, "Retry-After", "60"))
	f.route("GET", projPath+"/issues/4/resource_state_events", jsonReply(429, map[string]string{"message": "slow down"}, "Retry-After", "60"))
	r := testRepo(t, f)
	var logged []string
	c := r.newClient("t", false, func(s string) { logged = append(logged, s) })
	for _, iid := range []string{"3", "4", "3"} {
		events, err := listOptional[glStateEvent](context.Background(), c, r.projectPath("issues/"+iid+"/resource_state_events"), nil)
		mustOK(t, err)
		if len(events) != 0 {
			t.Fatalf("a rate-limited event list is empty")
		}
	}
	if n := len(f.callsTo("GET", projPath+"/issues/3/resource_state_events")); n != 1 {
		t.Fatalf("the cooldown skips the endpoint: %d requests for issue 3", n)
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "rate-limited") {
		t.Fatalf("the cooldown is logged once: %v", logged)
	}
}

func TestOptionalListsSkipRefusedFeaturesButNotAuthErrors(t *testing.T) {
	f := newFakeHost(t)
	f.route("GET", projPath+"/issues/3/links", jsonReply(403, map[string]string{"message": "no"}))
	f.route("GET", projPath+"/issues/3/notes", jsonReply(401, map[string]string{"message": "bad token"}))
	r := testRepo(t, f)
	c := r.newClient("t", false, nil)
	links, err := listOptional[glLink](context.Background(), c, r.projectPath("issues/3/links"), nil)
	mustOK(t, err)
	if len(links) != 0 {
		t.Fatal("a refused optional list is empty")
	}
	_, err = listOptional[glNote](context.Background(), c, r.projectPath("issues/3/notes"), nil)
	if errorStatus(err) != 401 {
		t.Fatalf("an refused token is an error, got %v", err)
	}
}

// ---- hierarchy and parents

const hierItem = `{"iid":"%s","workItemType":{"name":"Issue"},"widgets":%s}`

func hierWidget(parent map[string]any, children ...string) string {
	kids := []map[string]string{}
	for _, c := range children {
		kids = append(kids, map[string]string{"iid": c})
	}
	w := map[string]any{"__typename": "WorkItemWidgetHierarchy", "children": map[string]any{"nodes": kids}}
	if parent != nil {
		w["parent"] = parent
	}
	b, _ := json.Marshal([]any{w})
	return string(b)
}

func TestTheHierarchyReadsTwoPagesWithEpicParentsAndChildren(t *testing.T) {
	f := newFakeHost(t)
	epic := map[string]any{"iid": "486", "title": "Feature parity", "webUrl": "https://gitlab.com/groups/g/-/work_items/486", "workItemType": map[string]any{"name": "Epic"}}
	issue5 := map[string]any{"iid": "5", "title": "t", "webUrl": "u", "workItemType": map[string]any{"name": "Issue"}}
	page := func(hasNext bool, cursor *string, nodes ...string) map[string]any {
		var ns []json.RawMessage
		for _, n := range nodes {
			ns = append(ns, json.RawMessage(n))
		}
		pi := map[string]any{"hasNextPage": hasNext, "endCursor": cursor}
		return map[string]any{"data": map[string]any{"project": map[string]any{"workItems": map[string]any{"pageInfo": pi, "nodes": ns}}}}
	}
	c1 := "c1"
	var seen []any
	f.route("POST", "/api/graphql", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Variables struct {
				After any `json:"after"`
			} `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		seen = append(seen, body.Variables.After)
		if body.Variables.After == nil {
			jsonReply(200, page(true, &c1,
				fmt.Sprintf(hierItem, "180", hierWidget(epic)),
				fmt.Sprintf(hierItem, "5", hierWidget(nil, "6")),
			))(w, r)
			return
		}
		jsonReply(200, page(false, nil,
			fmt.Sprintf(hierItem, "6", hierWidget(issue5)),
		))(w, r)
	})
	r := testRepo(t, f)
	h, err := r.hierarchy(context.Background(), r.newClient("t", true, nil))
	mustOK(t, err)
	if len(seen) != 2 || seen[0] != nil || seen[1] != "c1" {
		t.Fatalf("the second page is asked with the first page's cursor: %v", seen)
	}
	if h[180].parent.iid != 486 || deref(h[180].parent.typ) != "Epic" {
		t.Fatalf("epic parent: %+v", h[180].parent)
	}
	if len(h[5].children) != 1 || h[5].children[0] != 6 {
		t.Fatalf("children of 5: %v", h[5].children)
	}
	if h[6].parent.iid != 5 {
		t.Fatalf("parent of 6: %+v", h[6].parent)
	}
}

func TestApplyHierarchyPutsIssueParentsAndEpicsWhereTheyBelong(t *testing.T) {
	issues := map[string]*model.Record{
		"5":   {Key: "5", Number: model.Ptr(5)},
		"6":   {Key: "6", Number: model.Ptr(6)},
		"180": {Key: "180", Number: model.Ptr(180)},
	}
	typ := func(s string) *string { return &s }
	h := map[int]hierEntry{
		180: {typ: typ("Issue"), parent: &hierParent{iid: 486, title: "Feature parity", url: "u", typ: typ("Epic")}},
		5:   {typ: typ("Issue"), children: []int{6, 999}},
		6:   {typ: typ("Issue"), parent: &hierParent{iid: 5, title: "t", url: "u", typ: typ("Issue")}},
	}
	applyHierarchy(issues, h)
	if issues["180"].Parent != nil || issues["180"].ParentEpic == nil || issues["180"].ParentEpic.IID != 486 {
		t.Fatalf("an epic goes to parentEpic: %+v %+v", issues["180"].Parent, issues["180"].ParentEpic)
	}
	if issues["6"].Parent == nil || *issues["6"].Parent != "5" || issues["6"].ParentEpic != nil {
		t.Fatalf("an issue parent goes to parent")
	}
	if len(issues["5"].SubIssues) != 1 || issues["5"].SubIssues[0] != "6" {
		t.Fatalf("a child that is not a stored issue is left out: %v", issues["5"].SubIssues)
	}
}

func TestApplyHierarchyCopiesAndNeverChangesAStoredRecord(t *testing.T) {
	old := &model.Record{Key: "5", Number: model.Ptr(5)}
	issues := map[string]*model.Record{"5": old}
	applyHierarchy(issues, map[int]hierEntry{5: {children: []int{}}})
	if issues["5"] != old {
		t.Fatal("an unchanged record is kept as it is")
	}
	issues["6"] = &model.Record{Key: "6", Number: model.Ptr(6)}
	applyHierarchy(issues, map[int]hierEntry{5: {children: []int{6}}})
	if old.SubIssues != nil || issues["5"] == old || len(issues["5"].SubIssues) != 1 {
		t.Fatal("a changed record is replaced by a copy, the stored one is not changed")
	}
}

// ---- the sync

// syncHost serves a small project: two issues (one confidential, with an internal note), one merged merge request.
func syncHost(t *testing.T) *fakeHost {
	t.Helper()
	f := newFakeHost(t)
	issues := []map[string]any{
		{"id": 103, "iid": 3, "title": "Secret plan", "description": "d", "web_url": "u3", "author": map[string]any{"username": "ana"},
			"created_at": "2026-01-01T10:00:00Z", "updated_at": "2026-01-03T10:00:00Z", "state": "opened", "confidential": true,
			"labels": []string{"bug"}, "assignees": []map[string]any{{"username": "ana"}}, "user_notes_count": 3,
			"milestone": map[string]any{"title": "v1", "due_date": "2026-02-01"}, "due_date": "2026-02-10"},
		{"id": 104, "iid": 4, "title": "Public", "web_url": "u4", "created_at": "2026-01-01T10:00:00Z", "updated_at": "2026-01-02T10:00:00Z",
			"state": "closed", "closed_at": "2026-01-02T10:00:00Z", "confidential": false},
	}
	f.route("GET", projPath+"/issues", jsonReply(200, issues))
	mrs := []map[string]any{{
		"id": 207, "iid": 7, "title": "Add feature", "web_url": "um7", "author": map[string]any{"username": "bea"},
		"created_at": "2026-01-01T10:00:00Z", "updated_at": "2026-01-04T10:00:00Z", "state": "merged", "merged_at": "2026-01-04T10:00:00Z",
		"merge_user": map[string]any{"username": "bea"}, "target_branch": "main", "source_branch": "feat",
		"merge_commit_sha": "abc123", "draft": false, "labels": []string{}, "assignees": []map[string]any{},
	}}
	f.route("GET", projPath+"/merge_requests", jsonReply(200, mrs))
	f.route("GET", projPath+"/labels", jsonReply(200, []map[string]any{{"name": "bug", "color": "#d73a4a"}}))
	f.route("GET", projPath+"/issues/3/notes", jsonReply(200, []map[string]any{
		{"id": 10, "body": "public", "author": map[string]any{"username": "ana"}, "created_at": "2026-01-01T11:00:00Z"},
		{"id": 11, "body": "secret", "internal": true, "author": map[string]any{"username": "ana"}, "created_at": "2026-01-01T12:00:00Z"},
		{"id": 12, "body": "added bug", "system": true, "created_at": "2026-01-01T12:30:00Z"},
		{"id": 13, "body": "older confidential", "confidential": true, "created_at": "2026-01-01T13:00:00Z"},
	}))
	f.route("GET", projPath+"/issues/3/resource_state_events", jsonReply(200, []map[string]any{{"state": "closed", "created_at": "2026-01-02T10:00:00Z", "user": map[string]any{"username": "ana"}}}))
	f.route("GET", projPath+"/issues/3/resource_label_events", jsonReply(200, []map[string]any{{"action": "add", "label": map[string]any{"name": "bug"}, "created_at": "2026-01-01T10:30:00Z", "user": map[string]any{"username": "ana"}}}))
	f.route("GET", projPath+"/issues/3/links", jsonReply(200, []map[string]any{{"iid": 4, "link_type": "is_blocked_by", "issue_link_id": 1}, {"iid": 8, "link_type": "relates_to", "issue_link_id": 2}}))
	f.route("GET", projPath+"/merge_requests/7/approvals", jsonReply(200, map[string]any{"approved_by": []map[string]any{{"user": map[string]any{"username": "cy"}, "approved_at": "2026-01-03T10:00:00Z"}}}))
	f.route("GET", projPath+"/merge_requests/7/commits", jsonReply(200, []map[string]any{{"id": "abc123", "title": "Add feature", "committed_date": "2026-01-04T09:00:00Z", "author_name": "bea"}}))
	f.route("GET", projPath+"/merge_requests/7/closes_issues", jsonReply(200, []map[string]any{{"iid": 3}}))
	f.route("GET", projPath+"/merge_requests/7/changes", jsonReply(200, map[string]any{"changes": []map[string]any{{"diff": "+a\n+b\n-c\n+++ x\n--- y\n"}}}))
	f.route("POST", "/api/graphql", jsonReply(200, map[string]any{"data": map[string]any{"project": map[string]any{"workItems": map[string]any{
		"pageInfo": map[string]any{"hasNextPage": false, "endCursor": nil},
		"nodes": []any{map[string]any{"iid": "3", "workItemType": map[string]any{"name": "Issue"}, "widgets": []any{map[string]any{
			"__typename": "WorkItemWidgetHierarchy",
			"parent":     map[string]any{"iid": "9", "title": "Epic", "webUrl": "ue", "workItemType": map[string]any{"name": "Epic"}},
			"children":   map[string]any{"nodes": []any{}},
		}}}},
	}}}}))
	return f
}

func TestFullSyncStoresInternalNotesPrivateAndConfidentialIssues(t *testing.T) {
	f := syncHost(t)
	r := testRepo(t, f)
	st := openStore(t)
	entry, err := r.Sync(context.Background(), st, provider.SyncOptions{Full: true})
	mustOK(t, err)
	if !entry.Full || entry.Issues != 2 || entry.Pulls != 1 {
		t.Fatalf("entry %+v", entry)
	}
	iss := st.Data.Issues["3"]
	if iss == nil || iss.Confidential == nil || !*iss.Confidential {
		t.Fatal("a confidential issue is recorded as confidential")
	}
	if st.Data.Issues["4"].Confidential == nil || *st.Data.Issues["4"].Confidential {
		t.Fatal("an issue that is not confidential is recorded so")
	}
	got := map[string]bool{}
	for _, c := range iss.Comments {
		got[c.ID] = c.Private
	}
	if len(iss.Comments) != 3 || got["10"] || !got["11"] || !got["13"] {
		t.Fatalf("internal and confidential notes are private, system notes are gone: %+v", iss.Comments)
	}
	if iss.ParentEpic == nil || iss.ParentEpic.IID != 9 || iss.Parent != nil {
		t.Fatalf("an epic is a parentEpic: parent %v epic %+v", iss.Parent, iss.ParentEpic)
	}
	if len(iss.BlockedBy) != 1 || iss.BlockedBy[0] != "4" || len(iss.Blocking) != 0 {
		t.Fatalf("blockedBy %v blocking %v (relates_to is left out)", iss.BlockedBy, iss.Blocking)
	}
	if iss.Milestone == nil || iss.Milestone.Title != "v1" || iss.DueDate == nil || *iss.DueDate != "2026-02-10" {
		t.Fatalf("milestone and due date: %+v %v", iss.Milestone, iss.DueDate)
	}
	if iss.Labels[0].Color != "d73a4a" {
		t.Fatalf("the label color comes from the project: %+v", iss.Labels)
	}
	pr := st.Data.Pulls["7"]
	if pr == nil || pr.State != "MERGED" || pr.Additions == nil || *pr.Additions != 2 || *pr.Deletions != 1 || *pr.ChangedFiles != 1 {
		t.Fatalf("merge request: %+v", pr)
	}
	if pr.MergeCommitSha == nil || *pr.MergeCommitSha != "abc123" || len(pr.Closes) != 1 || pr.Closes[0] != "3" {
		t.Fatalf("merge commit %v closes %v", pr.MergeCommitSha, pr.Closes)
	}
	if pr.ReviewDecision == nil || len(pr.Reviews) != 1 || pr.CommitsCount == nil || *pr.CommitsCount != 1 {
		t.Fatalf("approvals and commits: %+v", pr)
	}
	merged := false
	for _, e := range pr.Events {
		merged = merged || e.Type == "Merged"
	}
	if !merged {
		t.Fatal("the merge is in the history")
	}
	if st.Data.Meta.Repo == nil || *st.Data.Meta.Repo != r.Key() || st.Data.Meta.FullSyncAt == nil || st.Data.Meta.Totals == nil || st.Data.Meta.Totals.Issues != 2 {
		t.Fatalf("meta: %+v", st.Data.Meta)
	}
	if len(st.Data.Meta.Syncs) != 1 || !st.Data.Meta.Syncs[0].Full {
		t.Fatalf("syncs: %+v", st.Data.Meta.Syncs)
	}
}

func TestIncrementalSyncAsksForWhatChangedAndReadsOpenLinksAgain(t *testing.T) {
	f := syncHost(t)
	r := testRepo(t, f)
	st := openStore(t)
	_, err := r.Sync(context.Background(), st, provider.SyncOptions{Full: true})
	mustOK(t, err)
	last := *st.Data.Meta.LastSync
	// the next sync: nothing listed, so the open issue's links are read again
	f.route("GET", projPath+"/issues", jsonReply(200, []any{}))
	f.route("GET", projPath+"/merge_requests", jsonReply(200, []any{}))
	f.mu.Lock()
	f.calls = nil
	f.mu.Unlock()
	entry, err := r.Sync(context.Background(), st, provider.SyncOptions{})
	mustOK(t, err)
	if entry.Full {
		t.Fatal("an incremental sync")
	}
	q := f.callsTo("GET", projPath+"/issues")
	if len(q) != 1 {
		t.Fatalf("issues asked %d times", len(q))
	}
	want, _ := time.Parse(time.RFC3339Nano, last)
	from := isoMillis(want.Add(-syncMargin))
	if parsed, _ := url.ParseQuery(q[0].query); parsed.Get("updated_after") != from {
		t.Fatalf("updated_after %q, want %q", parsed.Get("updated_after"), from)
	}
	if n := len(f.callsTo("GET", projPath+"/issues/3/links")); n != 1 {
		t.Fatalf("the links of the open issue are read again: %d", n)
	}
	if st.Data.Issues["3"] == nil || len(st.Data.Issues["3"].BlockedBy) != 1 {
		t.Fatal("the stored issue keeps its links")
	}
}

func TestAHierarchyFailureIsLoggedAndTheSyncGoesOn(t *testing.T) {
	f := syncHost(t)
	f.route("POST", "/api/graphql", jsonReply(404, map[string]string{"message": "no work items"}))
	r := testRepo(t, f)
	st := openStore(t)
	var logs []string
	_, err := r.Sync(context.Background(), st, provider.SyncOptions{Full: true, Log: func(s string) { logs = append(logs, s) }})
	mustOK(t, err)
	if len(st.Data.Issues) != 2 {
		t.Fatal("the issues are stored")
	}
	if len(logs) == 0 || !strings.Contains(strings.Join(logs, "\n"), "work items unavailable") {
		t.Fatalf("logs %v", logs)
	}
}

func TestSyncLogsAnEventCooldownOnceAndKeepsGoing(t *testing.T) {
	f := syncHost(t)
	f.route("GET", projPath+"/issues/3/resource_state_events", jsonReply(429, map[string]string{"message": "slow"}, "Retry-After", "60"))
	f.route("GET", projPath+"/issues/4/resource_state_events", jsonReply(429, map[string]string{"message": "slow"}, "Retry-After", "60"))
	r := testRepo(t, f)
	st := openStore(t)
	var logs []string
	_, err := r.Sync(context.Background(), st, provider.SyncOptions{Full: true, Log: func(s string) { logs = append(logs, s) }})
	mustOK(t, err)
	n := 0
	for _, l := range logs {
		if strings.Contains(l, "rate-limited") {
			n++
		}
	}
	if n != 1 || len(st.Data.Pulls) != 1 {
		t.Fatalf("the cooldown is logged once and the merge requests are still read: logs %v", logs)
	}
}

func TestSyncFailsWithoutSavingWhenTheIssueListIsRefused(t *testing.T) {
	f := newFakeHost(t)
	f.route("GET", projPath+"/issues", jsonReply(403, map[string]string{"message": "403 Forbidden"}))
	r := testRepo(t, f)
	st := openStore(t)
	_, err := r.Sync(context.Background(), st, provider.SyncOptions{Full: true})
	if err == nil {
		t.Fatal("a refused list fails the sync")
	}
	if st.Data.Meta.LastSync != nil {
		t.Fatal("nothing is recorded as synced")
	}
}

func TestASyncOfAnotherRepoClearsTheRecordsOfTheOldOne(t *testing.T) {
	f := syncHost(t)
	r := testRepo(t, f)
	st := openStore(t)
	other := "gitlab:https://elsewhere.example/a/b"
	st.Data.Meta.Repo = &other
	st.Data.Meta.FullSyncAt = strPtr("2026-01-01T00:00:00.000Z")
	st.Data.Issues["99"] = &model.Record{Key: "99", Kind: "issue"}
	_, err := r.Sync(context.Background(), st, provider.SyncOptions{})
	mustOK(t, err)
	if _, ok := st.Data.Issues["99"]; ok {
		t.Fatal("the records of the other repo are dropped")
	}
	if len(st.Data.Issues) != 2 {
		t.Fatalf("this repo's records are stored: %d", len(st.Data.Issues))
	}
}

func strPtr(s string) *string { return &s }
