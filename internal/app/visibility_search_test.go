package app

import (
	"testing"

	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/provider"
)

// A private note must not be found by the search, nor be the snippet, for someone who may not read it; a merge request
// sharing the issue's key keeps its own notes.
func TestVisibilityIsAppliedBeforeTheSearch(t *testing.T) {
	issue := &model.Record{Kind: "issue", Key: "5", Title: "plain", Comments: []model.Comment{{ID: "c1", Body: "secret plan", Private: true}}}
	pull := &model.Record{Kind: "pull", Key: "5", Title: "mr", Comments: []model.Comment{{ID: "c1", Body: "secret in the MR"}}}
	v := provider.Visibility{HiddenComments: map[string]map[string]bool{"5": {"c1": true}}}

	if r, _ := visibleRecord(issue, v); len(r.Comments) != 0 {
		t.Fatalf("the private note of the issue is still there: %+v", r.Comments)
	}
	if r, _ := visibleRecord(pull, v); len(r.Comments) != 1 {
		t.Fatalf("the merge request lost a note that belongs to the issue with its key: %+v", r.Comments)
	}
	if issue.Comments[0].Body != "secret plan" {
		t.Fatal("the stored record was changed")
	}
	if _, ok := visibleCommit(CommitDetail{}, provider.Visibility{HideCode: true}); ok {
		t.Fatal("a commit is shown to someone who may not see code")
	}
	d, _ := visibleCommit(CommitDetail{CommitRow: CommitRow{Issues: []string{"1", "2"}}}, provider.Visibility{Issues: map[string]bool{"2": true}})
	if len(d.Issues) != 1 || d.Issues[0] != "2" {
		t.Fatalf("issues of the commit: %v", d.Issues)
	}
}

func TestRedmineRelationsToHiddenIssuesAreLeftOut(t *testing.T) {
	r := &model.Record{Kind: "issue", Key: "3", Relations: []model.Relation{{ID: 1, Type: "blocks", From: 7, To: 3}, {ID: 2, Type: "relates", From: 3, To: 4}}}
	v := provider.Visibility{Issues: map[string]bool{"3": true, "4": true}}
	got, _ := visibleRecord(r, v)
	if len(got.Relations) != 1 || got.Relations[0].ID != 2 {
		t.Fatalf("relations: %+v", got.Relations)
	}
	if len(r.Relations) != 2 {
		t.Fatal("the stored record was changed")
	}
}
