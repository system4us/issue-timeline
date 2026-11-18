package gitlab

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/provider"
)

// changesOf builds the change set an edit receives: each name with its JSON value.
func changesOf(t *testing.T, kv map[string]any) provider.Changes {
	t.Helper()
	ch := provider.Changes{}
	for k, v := range kv {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		ch[k] = b
	}
	return ch
}

// bodyOf decodes a call's JSON body.
func bodyOf(t *testing.T, c fakeCall) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(c.body), &m); err != nil {
		t.Fatalf("body %q: %v", c.body, err)
	}
	return m
}

// issueJSON is an issue as the host gives it after an edit.
func issueJSON(state string) map[string]any {
	return map[string]any{
		"iid": 3, "title": "t", "description": "d", "state": state, "labels": []string{"bug", "ux"},
		"assignees": []map[string]any{{"username": "ana"}}, "milestone": nil, "due_date": nil, "updated_at": "2026-01-02T00:00:00Z",
	}
}

func TestEditIssueSendsTheGitLabNamesThenReadsItBack(t *testing.T) {
	f := newFakeHost(t)
	f.route("PUT", projPath+"/issues/3", jsonReply(200, map[string]any{}))
	f.route("POST", projPath+"/issues/3/notes", jsonReply(201, map[string]any{"id": 1}))
	f.route("GET", projPath+"/issues/3", jsonReply(200, issueJSON("opened")))
	r := testRepo(t, f)
	old := &model.Record{Kind: "issue", Key: "3", ID: "103", Number: model.Ptr(3), State: "CLOSED"}
	ch := changesOf(t, map[string]any{"state": "OPEN", "labels": []string{"bug", "ux"}, "milestone": nil, "dueDate": nil, "comment": "note"})
	rec, warnings, err := r.EditIssue(context.Background(), provider.Cred{Token: "person"}, old, ch)
	mustOK(t, err)
	if len(warnings) != 0 {
		t.Fatalf("no warnings: %v", warnings)
	}
	put := f.callsTo("PUT", projPath+"/issues/3")
	if len(put) != 1 {
		t.Fatalf("one PUT: %d", len(put))
	}
	got := bodyOf(t, put[0])
	if got["state_event"] != "reopen" || got["labels"] != "bug,ux" || got["milestone_id"] != float64(0) || got["due_date"] != "" {
		t.Fatalf("PUT body %v", got)
	}
	order := []string{}
	for _, c := range f.allCalls() {
		order = append(order, c.method+" "+c.path)
	}
	want := []string{"PUT " + projPath + "/issues/3", "POST " + projPath + "/issues/3/notes", "GET " + projPath + "/issues/3"}
	if strings.Join(order, "|") != strings.Join(want, "|") {
		t.Fatalf("order %v", order)
	}
	if rec.State != "OPEN" || len(rec.Labels) != 2 || rec.Labels[0].Name != "bug" || rec.Labels[0].Color != "b6c5d2" {
		t.Fatalf("the record is what the host says: %+v", rec)
	}
	if rec.ID != "103" || old.State != "CLOSED" {
		t.Fatal("the stored record is not changed, and the id is kept")
	}
}

func TestEditIssueDoesNotSendAPathIdThatIsNotANumber(t *testing.T) {
	f := newFakeHost(t)
	r := testRepo(t, f)
	old := &model.Record{Key: "3"}
	for _, ch := range []map[string]any{
		{"deleteComments": []string{"../../../../user"}, "title": "x"},
		{"addLinks": []map[string]any{{"type": "blocks", "to": "../x"}}, "title": "x"},
		{"parent": "1/../2"},
		{"editComments": []map[string]any{{"id": "../1", "body": "b"}}},
		{"addLinks": []map[string]any{{"type": "unknown", "to": "4"}}},
	} {
		_, _, err := r.EditIssue(context.Background(), provider.Cred{Token: "t"}, old, changesOf(t, ch))
		if err == nil {
			t.Fatalf("%v should be refused", ch)
		}
	}
	if n := len(f.allCalls()); n != 0 {
		t.Fatalf("no change is sent when an id is refused: %d requests", n)
	}
}

func TestEditIssueFailureKindsKeepTheHostsStatus(t *testing.T) {
	f := newFakeHost(t)
	f.route("PUT", projPath+"/issues/3", jsonReply(403, map[string]string{"message": "not allowed to set confidential"}))
	r := testRepo(t, f)
	_, _, err := r.EditIssue(context.Background(), provider.Cred{Token: "t"}, &model.Record{Key: "3"}, changesOf(t, map[string]any{"title": "x"}))
	if errorStatus(err) != 403 || !strings.Contains(err.Error(), "not allowed to set confidential") {
		t.Fatalf("a refused change is 403 with the host's reason: %v", err)
	}
	f.route("PUT", projPath+"/issues/3", jsonReply(422, map[string]any{"message": map[string]any{"title": []string{"is too long"}}}))
	_, _, err = r.EditIssue(context.Background(), provider.Cred{Token: "t"}, &model.Record{Key: "3"}, changesOf(t, map[string]any{"title": "x"}))
	if errorStatus(err) != 422 {
		t.Fatalf("a rejected change is 422: %v", err)
	}
	f.route("PUT", projPath+"/issues/3", jsonReply(401, map[string]string{"message": "401"}))
	_, _, err = r.EditIssue(context.Background(), provider.Cred{Token: "t"}, &model.Record{Key: "3"}, changesOf(t, map[string]any{"title": "x"}))
	if errorStatus(err) != 401 {
		t.Fatalf("a refused token is 401: %v", err)
	}
}

func TestEditIssueWarnsWhenTheHostDoesNotKeepTheWeight(t *testing.T) {
	f := newFakeHost(t)
	f.route("PUT", projPath+"/issues/3", jsonReply(200, map[string]any{}))
	f.route("GET", projPath+"/issues/3", jsonReply(200, issueJSON("opened")))
	r := testRepo(t, f)
	_, warnings, err := r.EditIssue(context.Background(), provider.Cred{Token: "t"}, &model.Record{Key: "3"}, changesOf(t, map[string]any{"weight": 2}))
	mustOK(t, err)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "weight") {
		t.Fatalf("warnings %v", warnings)
	}
}

// graphqlRoute answers the GraphQL calls of an edit: the work item of an iid, and the parent change.
func graphqlRoute(f *fakeHost, parentErrors []string) {
	f.route("POST", "/api/graphql", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if strings.Contains(body.Query, "workItemUpdate") {
			errs := []string{}
			errs = append(errs, parentErrors...)
			jsonReply(200, map[string]any{"data": map[string]any{"workItemUpdate": map[string]any{"errors": errs, "workItem": map[string]any{"id": "gid://wi/3"}}}})(w, r)
			return
		}
		iid, _ := body.Variables["iid"].(string)
		jsonReply(200, map[string]any{"data": map[string]any{"project": map[string]any{"workItems": map[string]any{"nodes": []any{map[string]any{"id": "gid://wi/" + iid}}}}}})(w, r)
	})
}

func TestEditIssueAddsRemovesLinksAndSetsTheParent(t *testing.T) {
	f := newFakeHost(t)
	f.route("GET", projPath, jsonReply(200, map[string]any{"id": 77}))
	f.route("POST", projPath+"/issues/3/links", jsonReply(201, map[string]any{}))
	f.route("GET", projPath+"/issues/3/links", jsonReply(200, []map[string]any{{"iid": 8, "link_type": "is_blocked_by", "issue_link_id": 55}}))
	f.route("DELETE", projPath+"/issues/3/links/55", jsonReply(204, nil))
	f.route("GET", projPath+"/issues/3", jsonReply(200, issueJSON("opened")))
	graphqlRoute(f, nil)
	r := testRepo(t, f)
	old := &model.Record{Key: "3", Kind: "issue", BlockedBy: []model.Ref{"8"}}
	ch := changesOf(t, map[string]any{
		"addLinks":    []map[string]any{{"type": "blocks", "to": "4"}},
		"removeLinks": []map[string]any{{"type": "blockedBy", "to": "8"}},
		"parent":      "5",
	})
	rec, _, err := r.EditIssue(context.Background(), provider.Cred{Token: "t"}, old, ch)
	mustOK(t, err)
	post := f.callsTo("POST", projPath+"/issues/3/links")
	if len(post) != 1 {
		t.Fatalf("one link added: %d", len(post))
	}
	b := bodyOf(t, post[0])
	if b["target_project_id"] != float64(77) || b["target_issue_iid"] != float64(4) || b["link_type"] != "blocks" {
		t.Fatalf("link body %v", b)
	}
	if len(f.callsTo("DELETE", projPath+"/issues/3/links/55")) != 1 {
		t.Fatal("the removed link is deleted by its own id")
	}
	var parentSet bool
	for _, c := range f.callsTo("POST", "/api/graphql") {
		if strings.Contains(c.body, "workItemUpdate") && strings.Contains(c.body, `"parentId":"gid://wi/5"`) {
			parentSet = true
		}
	}
	if !parentSet {
		t.Fatal("the parent is set through the work item hierarchy")
	}
	if len(rec.Blocking) != 1 || rec.Blocking[0] != "4" || len(rec.BlockedBy) != 0 || rec.Parent == nil || *rec.Parent != "5" {
		t.Fatalf("the record has the links and the parent: blocking %v blockedBy %v parent %v", rec.Blocking, rec.BlockedBy, rec.Parent)
	}
}

func TestEditIssueReportsAParentTheHostRefuses(t *testing.T) {
	f := newFakeHost(t)
	f.route("GET", projPath+"/issues/3", jsonReply(200, issueJSON("opened")))
	graphqlRoute(f, []string{"parent is not an epic"})
	r := testRepo(t, f)
	_, _, err := r.EditIssue(context.Background(), provider.Cred{Token: "t"}, &model.Record{Key: "3"}, changesOf(t, map[string]any{"parent": "5"}))
	if errorStatus(err) != 422 || !strings.Contains(err.Error(), "parent is not an epic") {
		t.Fatalf("the refusal of the parent: %v", err)
	}
}

func TestEditIssueAppliesNotesToItsOwnRecord(t *testing.T) {
	f := newFakeHost(t)
	f.route("PUT", projPath+"/issues/3/notes/10", jsonReply(200, map[string]any{}))
	f.route("DELETE", projPath+"/issues/3/notes/11", jsonReply(204, nil))
	f.route("GET", projPath+"/issues/3", jsonReply(200, issueJSON("opened")))
	r := testRepo(t, f)
	old := &model.Record{Key: "3", Comments: []model.Comment{{ID: "10", Body: "a"}, {ID: "11", Body: "b"}}, CommentsCount: 2}
	ch := changesOf(t, map[string]any{"editComments": []map[string]any{{"id": 10, "body": "A"}}, "deleteComments": []int{11}})
	rec, _, err := r.EditIssue(context.Background(), provider.Cred{Token: "t"}, old, ch)
	mustOK(t, err)
	if len(rec.Comments) != 1 || rec.Comments[0].Body != "A" || rec.CommentsCount != 1 {
		t.Fatalf("notes: %+v", rec.Comments)
	}
	if len(old.Comments) != 2 || old.Comments[0].Body != "a" {
		t.Fatal("the stored notes are not changed")
	}
}

func TestApplyLocallyFollowsTheJavaScriptLinkRules(t *testing.T) {
	// as a GitLab sync writes them: keys as text, the other issues as numbers
	issues := map[string]*model.Record{
		"1": {Key: "1", BlockedBy: []model.Ref{"2"}, Blocking: nil, Parent: refPtr("3"), SubIssues: nil},
		"2": {Key: "2", BlockedBy: nil, Blocking: []model.Ref{"1"}},
		"3": {Key: "3", SubIssues: []model.Ref{"1"}},
		"4": {Key: "4", SubIssues: []model.Ref{}},
	}
	editOf := func(rec *model.Record, kv map[string]any, t *testing.T) {
		t.Helper()
		ch := changesOf(t, kv)
		old := rec
		if err := ApplyLocally(issues, old, ch); err != nil {
			t.Fatal(err)
		}
		self := *old
		lc, err := localOf(ch)
		mustOK(t, err)
		applySelf(&self, lc)
		issues[self.Key] = &self
	}
	editOf(issues["1"], map[string]any{"addLinks": []map[string]any{{"type": "blockedBy", "to": "2"}}}, t)
	if len(issues["1"].BlockedBy) != 1 || len(issues["2"].Blocking) != 1 {
		t.Fatalf("already there: not added twice: %v %v", issues["1"].BlockedBy, issues["2"].Blocking)
	}
	editOf(issues["1"], map[string]any{"removeLinks": []map[string]any{{"type": "blockedBy", "to": "2"}}}, t)
	if len(issues["1"].BlockedBy) != 0 || len(issues["2"].Blocking) != 0 {
		t.Fatalf("removed on both sides: %v %v", issues["1"].BlockedBy, issues["2"].Blocking)
	}
	editOf(issues["1"], map[string]any{"addLinks": []map[string]any{{"type": "blocks", "to": "4"}}, "parent": "4"}, t)
	if len(issues["1"].Blocking) != 1 || issues["1"].Blocking[0] != "4" {
		t.Fatalf("blocking: %v", issues["1"].Blocking)
	}
	if len(issues["3"].SubIssues) != 0 || len(issues["4"].SubIssues) != 1 || issues["4"].SubIssues[0] != "1" {
		t.Fatalf("the old parent loses it, the new one gains it: %v %v", issues["3"].SubIssues, issues["4"].SubIssues)
	}
	if issues["1"].Parent == nil || *issues["1"].Parent != "4" {
		t.Fatal("the parent is set on the issue")
	}
	editOf(issues["1"], map[string]any{"parent": nil}, t)
	if issues["1"].Parent != nil || len(issues["4"].SubIssues) != 0 {
		t.Fatal("removing the parent takes the issue out of its sub-issues")
	}
}

func TestApplyLocallyNeverChangesAStoredRecord(t *testing.T) {
	stored := &model.Record{Key: "2", BlockedBy: []model.Ref{"9"}, Blocking: []model.Ref{}}
	issues := map[string]*model.Record{"1": {Key: "1"}, "2": stored}
	old := issues["1"]
	if err := ApplyLocally(issues, old, changesOf(t, map[string]any{"addLinks": []map[string]any{{"type": "blocks", "to": "2"}}})); err != nil {
		t.Fatal(err)
	}
	if issues["2"] == stored {
		t.Fatal("the other record is replaced by a copy")
	}
	if len(stored.BlockedBy) != 1 || stored.BlockedBy[0] != "9" || len(issues["2"].BlockedBy) != 2 {
		t.Fatalf("the stored one: %v, the copy: %v", stored.BlockedBy, issues["2"].BlockedBy)
	}
}

func TestEditPullDraftIsATitlePrefix(t *testing.T) {
	f := newFakeHost(t)
	f.route("PUT", projPath+"/merge_requests/7", jsonReply(200, map[string]any{}))
	f.route("GET", projPath+"/merge_requests/7", jsonReply(200, map[string]any{"iid": 7, "title": "Draft: Fix", "state": "opened", "draft": true, "description": "", "assignees": []map[string]any{{"username": "ana"}}}))
	r := testRepo(t, f)
	rec, err := r.EditPull(context.Background(), provider.Cred{Token: "t"}, &model.Record{Key: "7", Title: "Fix"}, changesOf(t, map[string]any{"title": "Fix", "isDraft": true}))
	mustOK(t, err)
	if got := bodyOf(t, f.callsTo("PUT", projPath+"/merge_requests/7")[0])["title"]; got != "Draft: Fix" {
		t.Fatalf("the title gets the prefix: %v", got)
	}
	if rec.IsDraft == nil || !*rec.IsDraft || rec.State != "OPEN" || rec.Assignees[0] != "ana" {
		t.Fatalf("read back: %+v", rec)
	}
}

func TestEditPullWithoutTitleReadsTheCurrentOne(t *testing.T) {
	f := newFakeHost(t)
	f.route("GET", projPath+"/merge_requests/7", jsonReply(200, map[string]any{"iid": 7, "title": "Draft: WIP: x", "state": "opened"}))
	f.route("PUT", projPath+"/merge_requests/7", jsonReply(200, map[string]any{}))
	r := testRepo(t, f)
	_, err := r.EditPull(context.Background(), provider.Cred{Token: "t"}, &model.Record{Key: "7"}, changesOf(t, map[string]any{"isDraft": false}))
	mustOK(t, err)
	if got := bodyOf(t, f.callsTo("PUT", projPath+"/merge_requests/7")[0])["title"]; got != "WIP: x" {
		t.Fatalf("the draft prefix goes, the rest stays: %v", got)
	}
}

func TestCreateIssueSendsTheFormAndReturnsTheRecord(t *testing.T) {
	f := newFakeHost(t)
	f.route("POST", projPath+"/issues", jsonReply(201, map[string]any{"id": 300, "iid": 12, "title": "New", "description": "", "web_url": "u", "state": "opened", "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z", "labels": []string{"bug"}, "assignees": []map[string]any{{"username": "ana"}}, "author": map[string]any{"username": "ana"}, "due_date": "2026-02-01", "confidential": false}))
	r := testRepo(t, f)
	rec, err := r.CreateIssue(context.Background(), provider.Cred{Token: "t"}, changesOf(t, map[string]any{"title": "New", "labels": []string{"bug", "ux"}, "assignees": []int{4}, "milestone": 2, "dueDate": "2026-02-01"}))
	mustOK(t, err)
	b := bodyOf(t, f.callsTo("POST", projPath+"/issues")[0])
	if b["title"] != "New" || b["labels"] != "bug,ux" || b["milestone_id"] != float64(2) || b["due_date"] != "2026-02-01" || b["description"] != "" {
		t.Fatalf("body %v", b)
	}
	if rec.Key != "12" || rec.ID != "300" || rec.Number == nil || *rec.Number != 12 || rec.Author == nil || *rec.Author != "ana" || rec.Confidential == nil {
		t.Fatalf("record %+v", rec)
	}
}

func TestEditOptionsListsLabelsActiveMilestonesAndMembers(t *testing.T) {
	f := newFakeHost(t)
	f.route("GET", projPath+"/labels", jsonReply(200, []map[string]any{{"name": "bug", "color": "#d73a4a"}}))
	f.route("GET", projPath+"/milestones", jsonReply(200, []map[string]any{{"id": 2, "title": "v1"}}))
	f.route("GET", projPath+"/members/all", jsonReply(200, []map[string]any{{"id": 9, "username": "ana"}}))
	r := testRepo(t, f)
	o, err := r.EditOptions(context.Background(), provider.Cred{Token: "t"})
	mustOK(t, err)
	if len(o.Labels) != 1 || o.Labels[0]["color"] != "#d73a4a" || len(o.Milestones) != 1 || o.Members[0]["name"] != "ana" {
		t.Fatalf("options %+v", o)
	}
	if q := f.callsTo("GET", projPath+"/milestones")[0].query; !strings.Contains(q, "state=active") {
		t.Fatalf("the active milestones: %s", q)
	}
	if o.Trackers == nil || o.RelationTypes == nil {
		t.Fatal("every list is present, empty when there is none")
	}
}

func refPtr(s string) *model.Ref { r := model.Ref(s); return &r }

func TestAPostAnsweredWith5xxIsSentExactlyOnce(t *testing.T) {
	f := newFakeHost(t)
	f.route("POST", projPath+"/issues/3/notes", jsonReply(500, map[string]string{"message": "boom"}, "Retry-After", "0"))
	r := testRepo(t, f)
	_, _, err := r.EditIssue(context.Background(), provider.Cred{Token: "t"}, &model.Record{Key: "3"}, changesOf(t, map[string]any{"comment": "note"}))
	if err == nil {
		t.Fatal("the failure goes back to the caller")
	}
	if n := len(f.callsTo("POST", projPath+"/issues/3/notes")); n != 1 {
		t.Fatalf("a POST that got a 5xx is sent once, sent %d times", n)
	}
}

func TestAPostWith429IsRetried(t *testing.T) {
	f := newFakeHost(t)
	hits := 0
	f.route("POST", projPath+"/issues/3/notes", func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits == 1 {
			w.Header().Set("Retry-After", "0")
			jsonReply(429, map[string]string{"message": "slow"})(w, r)
			return
		}
		jsonReply(201, map[string]any{"id": 1})(w, r)
	})
	f.route("GET", projPath+"/issues/3", jsonReply(200, issueJSON("opened")))
	r := testRepo(t, f)
	_, _, err := r.EditIssue(context.Background(), provider.Cred{Token: "t"}, &model.Record{Key: "3"}, changesOf(t, map[string]any{"comment": "note"}))
	mustOK(t, err)
	if hits != 2 {
		t.Fatalf("a 429 is retried for a POST: %d requests", hits)
	}
}

func TestAGraphQLMutationAnsweredWith5xxIsSentOnce(t *testing.T) {
	f := newFakeHost(t)
	f.route("POST", "/api/graphql", jsonReply(500, map[string]string{"message": "boom"}, "Retry-After", "0"))
	r := testRepo(t, f)
	mutation := "mutation($input: WorkItemUpdateInput!) { workItemUpdate(input: $input) { errors workItem { id } } }"
	_, err := r.graphQL(context.Background(), r.newClient("t", false, nil), mutation, map[string]any{"input": map[string]any{}})
	if err == nil {
		t.Fatal("the failure goes back to the caller")
	}
	if n := len(f.callsTo("POST", "/api/graphql")); n != 1 {
		t.Fatalf("a mutation is sent once, sent %d times", n)
	}
}

func TestAGraphQLQueryAnsweredWith5xxIsRetried(t *testing.T) {
	f := newFakeHost(t)
	hits := 0
	f.route("POST", "/api/graphql", func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits == 1 {
			w.Header().Set("Retry-After", "0")
			jsonReply(500, map[string]string{"message": "boom"})(w, r)
			return
		}
		jsonReply(200, map[string]any{"data": map[string]any{"ok": true}})(w, r)
	})
	r := testRepo(t, f)
	data, err := r.graphQL(context.Background(), r.newClient("t", false, nil), "query { ok }", nil)
	mustOK(t, err)
	if hits != 2 || len(data) == 0 {
		t.Fatalf("a query is retried after a 5xx: %d requests, data %s", hits, data)
	}
}

func TestANewNoteIsKeptAsGitLabConfirmedIt(t *testing.T) {
	note := map[string]any{"id": 501, "body": "note", "created_at": "2026-01-02T11:00:00Z", "internal": false, "author": map[string]any{"username": "bea"}}
	f := newFakeHost(t)
	f.route("POST", projPath+"/issues/3/notes", jsonReply(201, note))
	f.route("GET", projPath+"/issues/3", jsonReply(200, issueJSON("opened")))
	f.route("POST", projPath+"/merge_requests/4/notes", jsonReply(201, map[string]any{"id": 502, "body": "mr note", "created_at": "2026-01-02T12:00:00Z", "author": map[string]any{"username": "bea"}}))
	f.route("GET", projPath+"/merge_requests/4", jsonReply(200, map[string]any{"iid": 4, "title": "m", "state": "opened", "labels": []string{}, "assignees": []any{}, "reviewers": []any{}}))
	r := testRepo(t, f)

	old := &model.Record{Kind: "issue", Key: "3", ID: "103", Number: model.Ptr(3), State: "OPEN"}
	rec, _, err := r.EditIssue(context.Background(), provider.Cred{Token: "person"}, old, changesOf(t, map[string]any{"comment": "note"}))
	mustOK(t, err)
	if len(rec.Comments) != 1 || rec.CommentsCount != 1 || rec.Comments[0].ID != "501" || *rec.Comments[0].Author != "bea" || rec.Comments[0].Body != "note" {
		t.Fatalf("the note the host confirmed is kept: %+v", rec.Comments)
	}

	pull := &model.Record{Kind: "pull", Key: "4", ID: "204", Number: model.Ptr(4), State: "OPEN"}
	pr, err := r.EditPull(context.Background(), provider.Cred{Token: "person"}, pull, changesOf(t, map[string]any{"comment": "mr note"}))
	mustOK(t, err)
	if len(pr.Comments) != 1 || pr.Comments[0].ID != "502" {
		t.Fatalf("a merge request's note too: %+v", pr.Comments)
	}
}
