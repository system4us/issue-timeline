package gitlab

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/provider"
)

// memberRoute answers the person's membership with an access level (0 means not a member: 404).
func memberRoute(f *fakeHost, userID string, level int) {
	if level == 0 {
		f.route("GET", projPath+"/members/all/"+userID, jsonReply(404, map[string]string{"message": "404"}))
		return
	}
	f.route("GET", projPath+"/members/all/"+userID, jsonReply(200, map[string]any{"id": 9, "access_level": level}))
}

func TestRoleFromAccessLevel(t *testing.T) {
	cases := map[int]provider.Role{50: provider.RoleAdmin, 40: provider.RoleEditor, 30: provider.RoleEditor, 20: provider.RoleViewer, 10: provider.RoleViewer, 0: provider.RoleNone}
	for level, want := range cases {
		if got := roleOfLevel(level); got != want {
			t.Errorf("level %d: %q, want %q", level, got, want)
		}
	}
}

func TestRoleOfReadsTheMembershipWithThePersonsToken(t *testing.T) {
	f := newFakeHost(t)
	memberRoute(f, "9", 30)
	r := testRepo(t, f)
	role, err := r.RoleOf(context.Background(), provider.Cred{Token: "person-token"}, provider.User{ID: 9})
	mustOK(t, err)
	if role != provider.RoleEditor {
		t.Fatalf("role %q", role)
	}
	calls := f.callsTo("GET", projPath+"/members/all/9")
	if len(calls) != 1 {
		t.Fatalf("one membership read: %d", len(calls))
	}
}

func TestRoleOfNonMemberIsNoRole(t *testing.T) {
	f := newFakeHost(t)
	memberRoute(f, "4", 0)
	r := testRepo(t, f)
	role, err := r.RoleOf(context.Background(), provider.Cred{Token: "t"}, provider.User{ID: 4})
	mustOK(t, err)
	if role != provider.RoleNone {
		t.Fatalf("a 404 membership is no role, got %q", role)
	}
}

func TestRoleOfRefusesAnIDThatIsNotANumber(t *testing.T) {
	f := newFakeHost(t)
	r := testRepo(t, f)
	if _, err := r.RoleOf(context.Background(), provider.Cred{Token: "t"}, provider.User{ID: 0}); errorStatus(err) != 400 {
		t.Fatalf("a user id must be a positive number: %v", err)
	}
	if len(f.allCalls()) != 0 {
		t.Fatal("no request is made for a refused id")
	}
}

func TestWhoAmIReadsTheAccountOfTheToken(t *testing.T) {
	f := newFakeHost(t)
	f.route("GET", "/api/v4/user", jsonReply(200, map[string]any{"id": 9, "username": "ana", "name": "Ana", "is_admin": true}))
	r := testRepo(t, f)
	u, err := r.WhoAmI(context.Background(), "t")
	mustOK(t, err)
	if u.ID != 9 || u.Login != "ana" || u.Name != "Ana" || !u.Admin {
		t.Fatalf("user %+v", u)
	}
}

func TestWhoAmIRefusedTokenIs401(t *testing.T) {
	f := newFakeHost(t)
	f.route("GET", "/api/v4/user", jsonReply(401, map[string]string{"message": "401 Unauthorized"}))
	r := testRepo(t, f)
	if _, err := r.WhoAmI(context.Background(), "bad"); errorStatus(err) != 401 {
		t.Fatalf("a refused token is 401: %v", err)
	}
}

// visibilityFixture is a project with a confidential issue (6) and a public one (7), and internal notes on 6.
func visibilityFixture(t *testing.T, level int, listed []map[string]any) (*fakeHost, *Repo, []*model.Record) {
	t.Helper()
	f := newFakeHost(t)
	memberRoute(f, "9", level)
	f.route("GET", projPath+"/issues", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("confidential") == "true" {
			jsonReply(200, listed)(w, r)
			return
		}
		jsonReply(200, []any{})(w, r)
	})
	r := testRepo(t, f)
	conf := true
	notPrivate := false
	issues := []*model.Record{
		{Key: "6", Kind: "issue", Confidential: &conf, Comments: []model.Comment{{ID: "60", Body: "internal", Private: true}, {ID: "61", Body: "open"}}},
		{Key: "7", Kind: "issue", Confidential: &notPrivate, Comments: []model.Comment{{ID: "70", Body: "hm", Private: true}}},
	}
	return f, r, issues
}

func TestConfidentialIssuesAreHiddenUnlessTheTokenListsThem(t *testing.T) {
	_, r, issues := visibilityFixture(t, 30, []map[string]any{})
	vis, err := r.VisibilityOf(context.Background(), provider.Cred{Token: "person"}, provider.User{ID: 9}, provider.RoleEditor, issues)
	mustOK(t, err)
	if vis.Issues == nil || vis.Issues["6"] || !vis.Issues["7"] {
		t.Fatalf("the confidential issue 6 is not listed by the token, so hidden: %v", vis.Issues)
	}
	if vis.HideCode || vis.HiddenComments != nil {
		t.Fatalf("a developer sees the code and the internal notes: %+v", vis)
	}
}

func TestConfidentialIssueIsVisibleWhenTheTokenListsIt(t *testing.T) {
	_, r, issues := visibilityFixture(t, 30, []map[string]any{{"iid": 6}})
	vis, err := r.VisibilityOf(context.Background(), provider.Cred{Token: "person"}, provider.User{ID: 9}, provider.RoleEditor, issues)
	mustOK(t, err)
	if !vis.Issues["6"] || !vis.Issues["7"] {
		t.Fatalf("both are visible: %v", vis.Issues)
	}
}

func TestBelowReporterHidesCodeAndInternalNotes(t *testing.T) {
	_, r, issues := visibilityFixture(t, 10, []map[string]any{})
	vis, err := r.VisibilityOf(context.Background(), provider.Cred{Token: "person"}, provider.User{ID: 9}, provider.RoleViewer, issues)
	mustOK(t, err)
	if !vis.HideCode {
		t.Fatal("a Guest does not see the code")
	}
	if !vis.HiddenComments["6"]["60"] || !vis.HiddenComments["7"]["70"] || vis.HiddenComments["6"]["61"] {
		t.Fatalf("every private note is hidden, the public one is not: %v", vis.HiddenComments)
	}
}

func TestReporterSeesCodeAndInternalNotes(t *testing.T) {
	_, r, issues := visibilityFixture(t, 20, []map[string]any{})
	vis, err := r.VisibilityOf(context.Background(), provider.Cred{Token: "person"}, provider.User{ID: 9}, provider.RoleViewer, issues)
	mustOK(t, err)
	if vis.HideCode || vis.HiddenComments != nil {
		t.Fatalf("a Reporter sees the code and the internal notes: %+v", vis)
	}
}

func TestANonMemberSeesNoCodeAndNoInternalNotes(t *testing.T) {
	f, r, issues := visibilityFixture(t, 0, []map[string]any{})
	_ = f
	vis, err := r.VisibilityOf(context.Background(), provider.Cred{Token: "person"}, provider.User{ID: 9}, provider.RoleNone, issues)
	mustOK(t, err)
	if !vis.HideCode || !vis.HiddenComments["6"]["60"] {
		t.Fatalf("no membership is below Reporter: %+v", vis)
	}
}

func TestVisibilityWithNoConfidentialIssueMakesNoListRequest(t *testing.T) {
	f := newFakeHost(t)
	memberRoute(f, "9", 30)
	r := testRepo(t, f)
	vis, err := r.VisibilityOf(context.Background(), provider.Cred{Token: "p"}, provider.User{ID: 9}, provider.RoleEditor, []*model.Record{{Key: "1", Kind: "issue"}})
	mustOK(t, err)
	if vis.Issues != nil {
		t.Fatal("nil means all issues")
	}
	if n := len(f.callsTo("GET", projPath+"/issues")); n != 0 {
		t.Fatalf("no confidential listing: %d", n)
	}
}

func TestRefreshMergesWhatTheHostSaysNow(t *testing.T) {
	f := newFakeHost(t)
	f.route("GET", projPath+"/issues/3", jsonReply(200, map[string]any{
		"iid": 3, "title": "New", "description": "b", "state": "closed", "closed_at": "2026-02-01T00:00:00Z",
		"labels": []string{"bug"}, "assignees": []map[string]any{{"username": "bea"}},
		"milestone": map[string]any{"title": "v1", "due_date": "2026-03-01"}, "due_date": "2026-03-05", "confidential": true, "weight": 2,
	}))
	r := testRepo(t, f)
	old := &model.Record{Kind: "issue", Key: "3", Number: model.Ptr(3), Title: "Old", State: "OPEN", ID: "103",
		Labels: []model.Label{{Name: "bug", Color: "d73a4a"}}, Milestone: &model.Milestone{Title: "v1", DueOn: strPtr("2026-02-28")}}
	got, err := r.Refresh(context.Background(), "issue", old)
	mustOK(t, err)
	if got.Title != "New" || got.State != "CLOSED" || got.ID != "103" {
		t.Fatalf("merged: %+v", got)
	}
	if got.Labels[0].Color != "d73a4a" {
		t.Fatalf("a label keeps its color: %+v", got.Labels)
	}
	if got.Milestone.DueOn == nil || *got.Milestone.DueOn != "2026-02-28" {
		t.Fatalf("a milestone keeps its date for the same title: %+v", got.Milestone)
	}
	if got.Confidential == nil || !*got.Confidential || got.DueDate == nil || got.Weight == nil || *got.Weight != 2 {
		t.Fatalf("confidential, due date and weight: %+v", got)
	}
	if old.Title != "Old" {
		t.Fatal("the record given is not changed")
	}
}

func TestRefreshOfAPullUsesTheMergeRequest(t *testing.T) {
	f := newFakeHost(t)
	f.route("GET", projPath+"/merge_requests/7", jsonReply(200, map[string]any{
		"iid": 7, "title": "Draft: X", "state": "merged", "draft": true, "reviewers": []map[string]any{{"username": "cy"}},
	}))
	r := testRepo(t, f)
	got, err := r.Refresh(context.Background(), "pull", &model.Record{Kind: "pull", Key: "7", Title: "X"})
	mustOK(t, err)
	if got.State != "MERGED" || got.IsDraft == nil || !*got.IsDraft || len(got.Reviewers) != 1 || got.Reviewers[0] != "cy" {
		t.Fatalf("pull: %+v", got)
	}
}

func TestRefreshOfAGoneRecordIsNotFound(t *testing.T) {
	f := newFakeHost(t)
	r := testRepo(t, f)
	_, err := r.Refresh(context.Background(), "issue", &model.Record{Key: "3"})
	if err == nil || !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("a 404 is provider.ErrNotFound: %v", err)
	}
	_, err = r.Refresh(context.Background(), "issue", &model.Record{Key: "../3"})
	if errorStatus(err) != 400 {
		t.Fatalf("a key that is not a number never reaches a path: %v", err)
	}
}
