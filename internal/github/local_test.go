package github

import (
	"testing"

	"gitlab.com/system4us/issue-timeline/internal/model"
)

// applyTo puts the copies a local change made into the map, as the caller's store would.
func applyTo(issues map[string]*model.Record, out map[string]*model.Record) {
	for k, v := range out {
		issues[k] = v
	}
}

func refsText(list []model.Ref) []string {
	out := []string{}
	for _, r := range list {
		out = append(out, string(r))
	}
	return out
}

func eqList(t *testing.T, what string, got []model.Ref, want ...string) {
	t.Helper()
	g := refsText(got)
	if len(g) != len(want) {
		t.Fatalf("%s: got %v, want %v", what, g, want)
	}
	for i := range want {
		if g[i] != want[i] {
			t.Fatalf("%s: got %v, want %v", what, g, want)
		}
	}
}

func TestLocalLinksAddAndRemoveBothSides(t *testing.T) {
	issues := map[string]*model.Record{
		"1": {Key: "1", BlockedBy: []model.Ref{"2"}, Parent: refPtr("3")},
		"2": {Key: "2", Blocking: []model.Ref{"1"}},
		"3": {Key: "3", SubIssues: []model.Ref{"1"}},
		"4": {Key: "4", SubIssues: []model.Ref{}},
	}
	// already there: not added twice
	applyTo(issues, LocalLinks(issues, "1", changes(t, map[string]any{"addLinks": []map[string]any{{"type": "blockedBy", "to": "2"}}})))
	eqList(t, "blockedBy after add", issues["1"].BlockedBy, "2")

	applyTo(issues, LocalLinks(issues, "1", changes(t, map[string]any{"removeLinks": []map[string]any{{"type": "blockedBy", "to": "2"}}})))
	eqList(t, "blockedBy after remove", issues["1"].BlockedBy)
	eqList(t, "the other side", issues["2"].Blocking)

	applyTo(issues, LocalLinks(issues, "1", changes(t, map[string]any{"addLinks": []map[string]any{{"type": "blocks", "to": 4}}, "parent": 4})))
	eqList(t, "blocking", issues["1"].Blocking, "4")
	eqList(t, "the old parent loses it", issues["3"].SubIssues)
	eqList(t, "the new parent gets it", issues["4"].SubIssues, "1")
	if issues["1"].Parent == nil || *issues["1"].Parent != "4" {
		t.Fatalf("parent: %v", issues["1"].Parent)
	}

	applyTo(issues, LocalLinks(issues, "1", changes(t, map[string]any{"parent": nil})))
	if issues["1"].Parent != nil {
		t.Fatalf("parent cleared: %v", issues["1"].Parent)
	}
	eqList(t, "parent's sub-issues after clearing", issues["4"].SubIssues)
}

func TestLocalLinksNeverChangeTheStoredRecord(t *testing.T) {
	stored := &model.Record{Key: "1", BlockedBy: []model.Ref{"2"}}
	issues := map[string]*model.Record{"1": stored}
	out := LocalLinks(issues, "1", changes(t, map[string]any{"removeLinks": []map[string]any{{"type": "blockedBy", "to": "2"}}}))
	if len(out) != 1 || len(out["1"].BlockedBy) != 0 {
		t.Fatalf("copy: %+v", out)
	}
	eqList(t, "stored", stored.BlockedBy, "2")
}

func TestLocalLinksKeysStayText(t *testing.T) {
	issues := map[string]*model.Record{"P-1": {Key: "P-1"}, "P-2": {Key: "P-2"}}
	out := LocalLinks(issues, "P-1", changes(t, map[string]any{"addLinks": []map[string]any{{"type": "blocks", "to": "P-2"}}}))
	eqList(t, "blocking", out["P-1"].Blocking, "P-2")
	eqList(t, "blockedBy", out["P-2"].BlockedBy, "P-1")
}

func TestLocalNotesEditedAndDeleted(t *testing.T) {
	issues := map[string]*model.Record{"1": {Key: "1", Comments: []model.Comment{{ID: "10", Body: "a"}, {ID: "11", Body: "b"}}, CommentsCount: 2}}
	out := LocalLinks(issues, "1", changes(t, map[string]any{
		"editComments":   []map[string]any{{"id": "10", "body": "A"}},
		"deleteComments": []int{11},
	}))
	got := out["1"]
	if len(got.Comments) != 1 || got.Comments[0].ID != "10" || got.Comments[0].Body != "A" || got.CommentsCount != 1 {
		t.Fatalf("notes: %+v", got.Comments)
	}
	if len(issues["1"].Comments) != 2 {
		t.Fatal("stored notes changed")
	}
}

func TestLocalLinksSkipsARecordNotInTheMap(t *testing.T) {
	out := LocalLinks(map[string]*model.Record{}, "1", changes(t, map[string]any{"addLinks": []map[string]any{{"type": "blocks", "to": 2}}}))
	if len(out) != 0 {
		t.Fatalf("nothing to change without the record: %v", out)
	}
}

func refPtr(s string) *model.Ref {
	r := model.Ref(s)
	return &r
}
