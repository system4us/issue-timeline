package app

import (
	"testing"

	"gitlab.com/system4us/issue-timeline/internal/derive"
	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/provider"
)

// A relation that names a hidden issue is left out of the record that goes to the person; the stored record keeps it.
func TestRelationsToHiddenIssuesAreLeftOut(t *testing.T) {
	parent := model.Ref("#9")
	stored := &model.Record{
		Kind: "issue", Key: "#1", Title: "one",
		Parent:    &parent,
		BlockedBy: []model.Ref{"#2", "#3"},
		Blocking:  []model.Ref{"#4"},
		SubIssues: []model.Ref{"#2"},
	}
	v := provider.Visibility{Issues: map[string]bool{"#1": true, "#3": true}}

	got, ok := visibleRecord(stored, v)
	if !ok {
		t.Fatal("the issue itself is visible")
	}
	if got.Parent != nil {
		t.Fatalf("the parent #9 is hidden but came back: %v", *got.Parent)
	}
	if len(got.BlockedBy) != 1 || got.BlockedBy[0] != "#3" {
		t.Fatalf("blockedBy: %v", got.BlockedBy)
	}
	if len(got.Blocking) != 0 || len(got.SubIssues) != 0 {
		t.Fatalf("blocking %v, subIssues %v: hidden issues came back", got.Blocking, got.SubIssues)
	}
	if stored.Parent == nil || *stored.Parent != "#9" || len(stored.BlockedBy) != 2 {
		t.Fatal("the stored record was changed")
	}
}

// A merge request keeps only the closes of issues the person may see.
func TestClosesOfAHiddenIssueAreLeftOutOfAPull(t *testing.T) {
	pull := &model.Record{Kind: "pull", Key: "!5", Closes: []model.Ref{"#1", "#2"}}
	v := provider.Visibility{Issues: map[string]bool{"#1": true}}
	got, ok := visibleRecord(pull, v)
	if !ok || len(got.Closes) != 1 || got.Closes[0] != "#1" {
		t.Fatalf("closes: %+v %v", got, ok)
	}
	if len(pull.Closes) != 2 {
		t.Fatal("the stored pull was changed")
	}
}

// The payload's light records and edges drop what names a hidden issue.
func TestPayloadDropsRelationsAndEdgesToHiddenIssues(t *testing.T) {
	parent := model.Ref("#9")
	child := &model.Record{Kind: "issue", Key: "#1", Parent: &parent, BlockedBy: []model.Ref{"#2"}}
	pull := &model.Record{Kind: "pull", Key: "!5", Closes: []model.Ref{"#2", "#1"}}
	d := Data{Payload: derive.Payload{
		Issues: []derive.Light{{Record: child}},
		Pulls:  []derive.Light{{Record: pull}},
		Links:  []derive.Link{{Issue: "#2", Pull: "!5"}, {Issue: "#1", Pull: "!5"}},
		Tree:   []derive.TreeEdge{{Parent: "#9", Child: "#1"}},
		Deps:   []derive.DepEdge{{Blocker: "#2", Blocked: "#1"}},
	}}
	v := provider.Visibility{Issues: map[string]bool{"#1": true, "#5": true}}
	out := applyVisibilityData(d, v)

	if out.Issues[0].Record.Parent != nil {
		t.Fatal("the light record still names the hidden parent #9")
	}
	if out.Pulls[0].Record.Closes == nil || len(out.Pulls[0].Record.Closes) != 1 || out.Pulls[0].Record.Closes[0] != "#1" {
		t.Fatalf("pull closes: %v", out.Pulls[0].Record.Closes)
	}
	if len(out.Links) != 1 || out.Links[0].Issue != "#1" {
		t.Fatalf("links: %+v", out.Links)
	}
	if len(out.Tree) != 0 || len(out.Deps) != 0 {
		t.Fatalf("tree %+v, deps %+v: edges to hidden issues came back", out.Tree, out.Deps)
	}
	if child.Parent == nil || len(pull.Closes) != 2 {
		t.Fatal("the stored records were changed")
	}
}
