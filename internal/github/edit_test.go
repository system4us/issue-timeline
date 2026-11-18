package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/provider"
)

// bodyOf is the JSON body of a call, compared as the JSON it is (key order does not matter).
func bodyOf(t *testing.T, c call) any {
	t.Helper()
	if c.Body == "" {
		return nil
	}
	var v any
	if err := json.Unmarshal([]byte(c.Body), &v); err != nil {
		t.Fatalf("body %q: %v", c.Body, err)
	}
	return v
}

func jsonEq(t *testing.T, got any, want string) {
	t.Helper()
	var w any
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(got)
	b, _ := json.Marshal(w)
	if string(a) != string(b) {
		t.Fatalf("got %s, want %s", a, b)
	}
}

func TestEditIssueSendsGitHubsNamesAndReadsTheRecordBack(t *testing.T) {
	issue := `{"title":"new title","body":"b","state":"closed","closed_at":"2026-01-01T00:00:00Z","labels":[{"name":"bug","color":"d73a4a"}],"assignees":[{"login":"ana"}],"milestone":{"title":"v1","number":1,"due_on":null},"updated_at":"2026-01-02T00:00:00Z"}`
	host := &fakeHost{routes: map[string]route{
		"PATCH /repos/o/r/issues/7": {body: issue},
		"GET /repos/o/r/issues/7":   {body: issue},
	}}
	r, _ := testRepo(t, host)
	old := &model.Record{Kind: "issue", Key: "7", Number: model.Ptr(7), State: "OPEN"}
	ch := changes(t, map[string]any{"title": "new title", "state": "CLOSED", "labels": []string{"bug"}, "assignees": []string{"ana"}, "milestone": 1, "comment": "done"})
	rec, warnings, err := r.EditIssue(context.Background(), provider.Cred{Token: "person"}, old, ch)
	if err != nil {
		t.Fatal(err)
	}
	calls := host.got()
	if calls[0].Method != "PATCH" || calls[0].Path != "/repos/o/r/issues/7" {
		t.Fatalf("first call: %+v", calls[0])
	}
	jsonEq(t, bodyOf(t, calls[0]), `{"title":"new title","state":"closed","labels":["bug"],"assignees":["ana"],"milestone":1}`)
	if calls[1].Method != "POST" || calls[1].Path != "/repos/o/r/issues/7/comments" {
		t.Fatalf("comment call: %+v", calls[1])
	}
	jsonEq(t, bodyOf(t, calls[1]), `{"body":"done"}`)
	if rec.State != "CLOSED" || rec.Labels[0] != (model.Label{Name: "bug", Color: "d73a4a"}) || rec.Milestone == nil || rec.Milestone.Title != "v1" {
		t.Fatalf("record read back: %+v", rec)
	}
	if warnings == nil || len(warnings) != 0 {
		t.Fatalf("warnings: %v", warnings)
	}
	if old.State != "OPEN" {
		t.Fatal("the stored record was changed in place")
	}
}

func TestEditIssueWarnsWhenGitHubKeptNoIssueType(t *testing.T) {
	issue := `{"title":"t","body":null,"state":"open","labels":[],"assignees":[],"milestone":null,"updated_at":"x"}`
	host := &fakeHost{routes: map[string]route{"GET /repos/o/r/issues/7": {body: issue}}}
	r, _ := testRepo(t, host)
	old := &model.Record{Kind: "issue", Key: "7"}
	_, warnings, err := r.EditIssue(context.Background(), provider.Cred{Token: "p"}, old, changes(t, map[string]any{"type": "Bug"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "issue type") {
		t.Fatalf("warnings: %v", warnings)
	}
}

func TestEditIssueLinksAndNotesAreAppliedLocally(t *testing.T) {
	issue := `{"id":700,"node_id":"I_7","title":"t","body":"","state":"open","labels":[],"assignees":[],"milestone":null,"updated_at":"x"}`
	host := &fakeHost{routes: map[string]route{
		"GET /repos/o/r/issues/7":               {body: issue},
		"GET /repos/o/r/issues/7/comments":      {body: `[{"id":101,"node_id":"IC_1"},{"id":102,"node_id":"IC_2"}]`},
		"GET /repos/o/r/issues/4":               {body: `{"id":404}`},
		"GET /repos/o/r/issues/7/parent":        {status: 404, body: `{"message":"Not Found"}`},
		"PATCH /repos/o/r/issues/comments/101":  {body: `{}`},
		"DELETE /repos/o/r/issues/comments/102": {status: 204},
	}}
	r, _ := testRepo(t, host)
	old := &model.Record{
		Kind: "issue", Key: "7", Number: model.Ptr(7),
		Comments:      []model.Comment{{ID: "IC_1", Body: "a"}, {ID: "IC_2", Body: "b"}},
		CommentsCount: 2,
	}
	ch := changes(t, map[string]any{
		"editComments":   []map[string]any{{"id": "IC_1", "body": "A"}},
		"deleteComments": []string{"IC_2"},
		"addLinks":       []map[string]any{{"type": "blocks", "to": 4}},
		"parent":         9,
	})
	rec, _, err := r.EditIssue(context.Background(), provider.Cred{Token: "p"}, old, ch)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Comments) != 1 || rec.Comments[0].Body != "A" || rec.CommentsCount != 1 {
		t.Fatalf("notes applied: %+v", rec.Comments)
	}
	if !model.HasRef(rec.Blocking, "4") {
		t.Fatalf("blocking: %v", rec.Blocking)
	}
	if rec.Parent == nil || *rec.Parent != "9" {
		t.Fatalf("parent: %v", rec.Parent)
	}
	if len(old.Comments) != 2 {
		t.Fatal("the stored record's notes were changed in place")
	}
	var blockedBy, subIssue bool
	for _, c := range host.got() {
		if c.Method == "POST" && c.Path == "/repos/o/r/issues/4/dependencies/blocked_by" {
			blockedBy = true
			jsonEq(t, bodyOf(t, c), `{"issue_id":700}`)
		}
		if c.Method == "POST" && c.Path == "/repos/o/r/issues/9/sub_issues" {
			subIssue = true
			jsonEq(t, bodyOf(t, c), `{"sub_issue_id":700}`)
		}
	}
	if !blockedBy || !subIssue {
		t.Fatalf("relations sent: blockedBy=%v subIssue=%v", blockedBy, subIssue)
	}
}

func TestIdsInPathsMustBeNumbers(t *testing.T) {
	host := &fakeHost{}
	r, _ := testRepo(t, host)
	ctx := context.Background()
	if _, err := provider.PathID("12", "x"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"../../user", "1/../2", "0", "-1", "1.5", ""} {
		_, _, err := r.EditIssue(ctx, provider.Cred{Token: "p"}, &model.Record{Key: bad}, changes(t, map[string]any{"title": "x"}))
		var pe *provider.Error
		if !errors.As(err, &pe) || pe.Status != 400 {
			t.Errorf("key %q: want a 400, got %v", bad, err)
		}
	}
	// a link to a path trick is refused before any link request
	_, _, err := r.EditIssue(ctx, provider.Cred{Token: "p"}, &model.Record{Key: "1"}, changes(t, map[string]any{"addLinks": []map[string]any{{"type": "blocks", "to": "../x"}}}))
	var pe *provider.Error
	if !errors.As(err, &pe) || pe.Status != 400 {
		t.Fatalf("link to ../x: %v", err)
	}
	for _, c := range host.got() {
		if c.Method == "POST" || c.Method == "DELETE" {
			t.Fatalf("a change was sent: %+v", c)
		}
	}
}

func TestEditIssueRefusedTokenAndRejectedChangeKeepTheirStatus(t *testing.T) {
	host := &fakeHost{routes: map[string]route{
		"PATCH /repos/o/r/issues/7": {status: 422, body: `{"message":"Validation Failed"}`},
	}}
	r, _ := testRepo(t, host)
	_, _, err := r.EditIssue(context.Background(), provider.Cred{Token: "p"}, &model.Record{Key: "7"}, changes(t, map[string]any{"title": ""}))
	var pe *provider.Error
	if !errors.As(err, &pe) || pe.Status != 422 || !strings.Contains(pe.Message, "Validation Failed") {
		t.Fatalf("422: %v", err)
	}
	host.routes["PATCH /repos/o/r/issues/7"] = route{status: 404, body: `{}`}
	_, _, err = r.EditIssue(context.Background(), provider.Cred{Token: "p"}, &model.Record{Key: "7"}, changes(t, map[string]any{"title": "x"}))
	if !errors.As(err, &pe) || pe.Status != 502 {
		t.Fatalf("a 404 on an edit is a 502: %v", err)
	}
}

func TestEditOptionsFollowEveryPageButNeverAnotherHost(t *testing.T) {
	var srvURL string
	host := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/labels"):
			if r.URL.Query().Get("page") == "2" {
				_, _ = w.Write([]byte(`[{"name":"b","color":"2"}]`))
				return
			}
			w.Header().Set("link", `<`+srvURL+`/repos/o/r/labels?per_page=100&page=2>; rel="next"`)
			_, _ = w.Write([]byte(`[{"name":"a","color":"1"}]`))
		case strings.HasSuffix(r.URL.Path, "/assignees"):
			w.Header().Set("link", `<https://evil.example/steal>; rel="next"`)
			_, _ = w.Write([]byte(`[{"login":"ana"}]`))
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	})
	r, srv := testRepo(t, host)
	srvURL = srv.URL
	out, err := r.EditOptions(context.Background(), provider.Cred{Token: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Labels) != 2 || out.Labels[0]["name"] != "a" || out.Labels[1]["name"] != "b" {
		t.Fatalf("labels: %v", out.Labels)
	}
	if len(out.Members) != 1 || out.Members[0]["id"] != "ana" {
		t.Fatalf("members: %v", out.Members)
	}
	if out.Trackers == nil || out.RelationTypes == nil {
		t.Fatal("every list is present, empty when the host has none")
	}
}

func TestEditPullAsksOnlyForTheReviewersThatDiffer(t *testing.T) {
	pr := `{"title":"t","body":"","state":"open","draft":false,"labels":[],"assignees":[],"milestone":null,"requested_reviewers":[{"login":"bea"},{"login":"caro"}],"updated_at":"x","merged_at":null,"node_id":"PR_1"}`
	host := &fakeHost{routes: map[string]route{
		"GET /repos/o/r/pulls/9/requested_reviewers": {body: `{"users":[{"login":"bea"},{"login":"caro"}]}`},
		"GET /repos/o/r/pulls/9":                     {body: pr},
	}}
	r, _ := testRepo(t, host)
	old := &model.Record{Kind: "pull", Key: "9"}
	rec, err := r.EditPull(context.Background(), provider.Cred{Token: "p"}, old, changes(t, map[string]any{"reviewers": []string{"bea", "dan"}}))
	if err != nil {
		t.Fatal(err)
	}
	var post, del *call
	calls := host.got()
	for i := range calls {
		if calls[i].Method == "POST" && strings.HasSuffix(calls[i].Path, "/requested_reviewers") {
			post = &calls[i]
		}
		if calls[i].Method == "DELETE" && strings.HasSuffix(calls[i].Path, "/requested_reviewers") {
			del = &calls[i]
		}
	}
	if post == nil || del == nil {
		t.Fatalf("calls: %+v", calls)
	}
	jsonEq(t, bodyOf(t, *post), `{"reviewers":["dan"]}`)
	jsonEq(t, bodyOf(t, *del), `{"reviewers":["caro"]}`)
	if len(rec.Reviewers) != 2 {
		t.Fatalf("reviewers read back: %v", rec.Reviewers)
	}
}

func TestEditPullDraftSwitchIsAGraphQLMutation(t *testing.T) {
	pr := `{"title":"t","body":"","state":"open","draft":false,"labels":[],"assignees":[],"milestone":null,"updated_at":"x","merged_at":null,"node_id":"PR_9","requested_reviewers":[]}`
	host := &fakeHost{routes: map[string]route{"GET /repos/o/r/pulls/9": {body: pr}}}
	r, _ := testRepo(t, host)
	rec, err := r.EditPull(context.Background(), provider.Cred{Token: "p"}, &model.Record{Kind: "pull", Key: "9"}, changes(t, map[string]any{"isDraft": true}))
	if err != nil {
		t.Fatal(err)
	}
	var mutation *call
	for _, c := range host.got() {
		if c.Method == "POST" && c.Path == "/graphql" {
			mutation = &c
		}
	}
	if mutation == nil || !strings.Contains(mutation.Body, "convertPullRequestToDraft") || !strings.Contains(mutation.Body, "PR_9") {
		t.Fatalf("mutation: %+v", mutation)
	}
	if rec.IsDraft == nil || *rec.IsDraft != false {
		// the second read still says not a draft in this fake, so the record keeps what GitHub last said
		t.Fatalf("the record keeps the read-back: %v", rec.IsDraft)
	}
}

func TestCreateIssueSendsTheFormAndKeepsWhatGitHubSays(t *testing.T) {
	host := &fakeHost{routes: map[string]route{
		"POST /repos/o/r/issues": {status: 201, body: `{"id":55,"node_id":"I_12","number":12,"title":"New","body":null,"html_url":"https://x/12","state":"open","created_at":"c","updated_at":"u","closed_at":null,"labels":[{"name":"bug"}],"assignees":[{"login":"ana"}],"milestone":null,"user":{"login":"me"}}`},
	}}
	r, _ := testRepo(t, host)
	rec, err := r.CreateIssue(context.Background(), provider.Cred{Token: "p"}, changes(t, map[string]any{"title": "New", "body": "", "labels": []string{"bug"}, "milestone": nil}))
	if err != nil {
		t.Fatal(err)
	}
	jsonEq(t, bodyOf(t, host.got()[0]), `{"title":"New","body":"","labels":["bug"]}`)
	if rec.Key != "12" || rec.ID != "I_12" || rec.Author == nil || *rec.Author != "me" || rec.Labels[0].Color != "b6c5d2" {
		t.Fatalf("record: %+v", rec)
	}
	if _, err := r.CreateIssue(context.Background(), provider.Cred{Token: "p"}, changes(t, map[string]any{"title": ""})); err == nil {
		t.Fatal("a title is needed")
	}
}

func TestANewCommentIsKeptAsGitHubConfirmedIt(t *testing.T) {
	issue := `{"title":"t","body":"","state":"open","labels":[],"assignees":[],"updated_at":"2026-01-02T00:00:00Z"}`
	posted := `{"id":99,"node_id":"IC_kw99","user":{"login":"ana"},"created_at":"2026-01-02T10:00:00Z","body":"done"}`
	host := &fakeHost{routes: map[string]route{
		"POST /repos/o/r/issues/7/comments": {body: posted},
		"GET /repos/o/r/issues/7":           {body: issue},
	}}
	r, _ := testRepo(t, host)
	first := model.Comment{ID: "IC_old", Body: "first"}
	old := &model.Record{Kind: "issue", Key: "7", Number: model.Ptr(7), State: "OPEN", Comments: []model.Comment{first}, CommentsCount: 1}
	rec, _, err := r.EditIssue(context.Background(), provider.Cred{Token: "person"}, old, changes(t, map[string]any{"comment": "done"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Comments) != 2 || rec.CommentsCount != 2 {
		t.Fatalf("comments: %+v (count %d)", rec.Comments, rec.CommentsCount)
	}
	got := rec.Comments[1]
	if got.ID != "IC_kw99" || got.Author == nil || *got.Author != "ana" || got.CreatedAt != "2026-01-02T10:00:00Z" || got.Body != "done" {
		t.Fatalf("new comment: %+v", got)
	}
	if len(old.Comments) != 1 {
		t.Fatalf("the stored record changed: %+v", old.Comments)
	}
}
