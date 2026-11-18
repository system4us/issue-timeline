package app

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"gitlab.com/system4us/issue-timeline/internal/auth"
	"gitlab.com/system4us/issue-timeline/internal/derive"
	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/provider"
	"gitlab.com/system4us/issue-timeline/internal/store"
)

func TestEditIssueAppliesLinksCopyOnWrite(t *testing.T) {
	dir := t.TempDir()
	writeNodeFiles(t, dir)
	a := newTestApp(t, dir, fixture{role: provider.RoleEditor}, nil)
	ctx := context.Background()

	before1, _ := a.Record(codeRepo, nil, "issue", "1")
	before2, _ := a.Record(codeRepo, nil, "issue", "2")
	titleBefore := before1.Title

	ch := provider.Changes{
		"title":    jsonOf(t, "Renamed"),
		"addLinks": jsonOf(t, []derive.LinkChange{{Type: "blocks", To: model.Ref("2")}}),
	}
	cred := provider.Cred{Token: "tok"}
	rec, warnings, err := a.EditIssue(ctx, codeRepo, cred, "1", ch)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings: %v", warnings)
	}
	if rec.Title != "Renamed" {
		t.Fatalf("the record kept has title %q", rec.Title)
	}
	if !model.HasRef(rec.Blocking, "2") {
		t.Fatalf("issue 1 does not block 2 after the edit: %v", rec.Blocking)
	}
	after2, _ := a.Record(codeRepo, nil, "issue", "2")
	if !model.HasRef(after2.BlockedBy, "1") {
		t.Fatalf("issue 2 is not blocked by 1 after the edit: %v", after2.BlockedBy)
	}

	// the records read before the edit are unchanged: copy-on-write
	if before1.Title != titleBefore || len(before1.Blocking) != 0 {
		t.Fatalf("the stored issue 1 was changed in place: %+v", before1.Blocking)
	}
	if len(before2.BlockedBy) != 0 {
		t.Fatalf("the stored issue 2 was changed in place: %v", before2.BlockedBy)
	}

	// the milestone's due date is kept while its title is the same
	if rec.Milestone == nil || rec.Milestone.DueOn == nil {
		t.Fatalf("the milestone's due date was lost: %+v", rec.Milestone)
	}

	// the change is on disk
	st, err := store.Open(filepath.Join(dir, "acme__app.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !model.HasRef(st.Data.Issues["2"].BlockedBy, "1") {
		t.Fatal("the other side of the link was not saved")
	}
}

func TestEditIssueDeletesNotes(t *testing.T) {
	dir := t.TempDir()
	writeNodeFiles(t, dir)
	a := newTestApp(t, dir, fixture{role: provider.RoleEditor}, nil)
	ctx := context.Background()
	ch := provider.Changes{
		"deleteComments": jsonOf(t, []string{"c2"}),
	}
	rec, _, err := a.EditIssue(ctx, codeRepo, provider.Cred{}, "1", ch)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Comments) != 1 || rec.Comments[0].ID != "c1" || rec.CommentsCount != 1 {
		t.Fatalf("the private note was not deleted: %+v", rec.Comments)
	}
}

func TestEditPullAndCreateIssue(t *testing.T) {
	dir := t.TempDir()
	writeRepos(t, dir, codeRepo)
	a := newTestApp(t, dir, fixture{role: provider.RoleEditor}, nil)
	ctx := context.Background()
	created, err := a.CreateIssue(ctx, codeRepo, provider.Cred{}, provider.Changes{"title": jsonOf(t, "New one")})
	if err != nil {
		t.Fatal(err)
	}
	if created.Key != "9" || created.Title != "New one" {
		t.Fatalf("created %+v", created)
	}
	if rec, ok := a.Record(codeRepo, nil, "issue", "9"); !ok || rec.Title != "New one" {
		t.Fatal("the created issue is not stored")
	}
	// a pull is not in this store: the edit is refused as not found
	if _, err := a.EditPull(ctx, codeRepo, provider.Cred{}, "5", provider.Changes{}); err == nil {
		t.Fatal("an unknown pull was edited")
	}
}

func TestVisibilityFiltersWithoutChangingStore(t *testing.T) {
	dir := t.TempDir()
	writeNodeFiles(t, dir)
	a := newTestApp(t, dir, fixture{}, nil)

	d, err := a.Data(codeRepo, nil)
	if err != nil {
		t.Fatal(err)
	}
	v := provider.Visibility{
		Issues:         map[string]bool{"1": true},
		HiddenComments: map[string]map[string]bool{"1": {"c2": true}},
	}
	got := ApplyVisibility(d, v)
	if len(got.Issues) != 1 || got.Issues[0].Record.Key != "1" {
		t.Fatalf("visible issues: %+v", got.Issues)
	}
	for _, t2 := range got.Tree {
		t.Fatalf("a tree edge names a hidden issue: %+v", t2)
	}
	rec, _ := a.Record(codeRepo, nil, "issue", "1")
	vr, ok := VisibleRecord(rec, v)
	if !ok || len(vr.Comments) != 1 || vr.Comments[0].ID != "c1" || vr.CommentsCount != 1 {
		t.Fatalf("the private note is visible: %+v", vr.Comments)
	}
	if _, ok := VisibleRecord(rec, provider.Visibility{Issues: map[string]bool{"2": true}}); ok {
		t.Fatal("a hidden issue is visible")
	}
	// the stored record keeps both notes
	again, _ := a.Record(codeRepo, nil, "issue", "1")
	if len(again.Comments) != 2 {
		t.Fatal("the stored record lost its note")
	}

	// the code is hidden: pulls, links to them, CI, and the pull's search results
	hidden := provider.Visibility{HideCode: true}
	code := ApplyVisibility(d, hidden)
	if len(code.Pulls) != 0 || len(code.Links) != 0 {
		t.Fatalf("code is visible: %d pulls, %d links", len(code.Pulls), len(code.Links))
	}
	if len(code.CI.ByPr) != 0 || len(code.CI.Changes) != 0 {
		t.Fatal("CI is visible with the code hidden")
	}
	if _, ok := VisibleRecord(&model.Record{Kind: "pull", Key: "5"}, hidden); ok {
		t.Fatal("a pull is visible with the code hidden")
	}
	rs := VisibleResults([]derive.SearchResult{{Key: "5", Kind: "pull"}, {Key: "1", Kind: "issue"}}, hidden)
	if !reflect.DeepEqual(rs, []derive.SearchResult{{Key: "1", Kind: "issue"}}) {
		t.Fatalf("visible results with the code hidden: %+v", rs)
	}
	if list := VisibleCommits(CommitList{Total: 1, Items: []CommitRow{{}}}, hidden); len(list.Items) != 0 {
		t.Fatal("commits are visible with the code hidden")
	}

	// commit rows keep only the issue keys the person may see
	rows := VisibleCommits(CommitList{Total: 1, Items: []CommitRow{{Issues: []string{"1", "2"}}}}, provider.Visibility{Issues: map[string]bool{"1": true}})
	if !reflect.DeepEqual(rows.Items[0].Issues, []string{"1"}) {
		t.Fatalf("commit issues: %v", rows.Items[0].Issues)
	}
}

func TestSessionVisibilityFromTheHost(t *testing.T) {
	dir := t.TempDir()
	writeNodeFiles(t, dir)
	hostVis := provider.Visibility{Issues: map[string]bool{"2": true}, HideCode: true}
	a := newTestApp(t, dir, fixture{role: provider.RoleViewer, vis: hostVis}, nil)
	sessions := auth.NewSessions(auth.SessionOptions{})
	sess := sessions.Get(sessions.Create("github", "github", "tok", provider.User{ID: 1, Login: "ann"}))
	v, err := a.Visibility(context.Background(), codeRepo, Who{Session: sess})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(v, hostVis) {
		t.Fatalf("visibility = %+v, want %+v", v, hostVis)
	}
	// kept under the repo and the store's last sync (nodeStore's lastSync), so a sync makes it read again
	if _, ok := sess.CachedVisibility(codeRepo + "@2026-11-01T10:00:00.000Z"); !ok {
		t.Fatal("the visibility was not kept in the session")
	}
}
