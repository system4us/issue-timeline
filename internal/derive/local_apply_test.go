package derive

import (
	"encoding/json"
	"reflect"
	"testing"

	"gitlab.com/system4us/issue-timeline/internal/model"
)

func wantRefs(t *testing.T, what string, got []model.Ref, want ...string) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, refs(want...)) {
		t.Fatalf("%s = %q, want %q", what, got, want)
	}
}

func TestARemovedLinkIsRemovedAndAnAddedOneIsNotDuplicated(t *testing.T) {
	// as a GitHub or GitLab sync writes them: keys as text, the other issues as numbers
	issues := recs(
		&model.Record{Key: "1", BlockedBy: refs("2"), Parent: refPtr("3")},
		&model.Record{Key: "2", Blocking: refs("1")},
		&model.Record{Key: "3", SubIssues: refs("1")},
		&model.Record{Key: "4"},
	)
	ApplyLinks(issues, "1", []LinkChange{{Type: "blockedBy", To: "2"}}, nil, nil, false)
	wantRefs(t, "blockedBy after an add of a link already there", issues["1"].BlockedBy, "2")

	ApplyLinks(issues, "1", nil, []LinkChange{{Type: "blockedBy", To: "2"}}, nil, false)
	wantRefs(t, "blockedBy", issues["1"].BlockedBy)
	wantRefs(t, "the other side's blocking", issues["2"].Blocking)

	ApplyLinks(issues, "1", []LinkChange{{Type: "blocks", To: "4"}}, nil, refPtr("4"), true)
	wantRefs(t, "blocking", issues["1"].Blocking, "4")
	wantRefs(t, "old parent's subIssues", issues["3"].SubIssues)
	wantRefs(t, "new parent's subIssues", issues["4"].SubIssues, "1")
	if issues["1"].Parent == nil || *issues["1"].Parent != "4" {
		t.Fatalf("parent = %v", issues["1"].Parent)
	}

	ApplyLinks(issues, "1", nil, nil, nil, true)
	if issues["1"].Parent != nil {
		t.Fatal("setParent with a nil parent removes it")
	}
	wantRefs(t, "subIssues of 4", issues["4"].SubIssues)
}

func TestJiraKeysStayText(t *testing.T) {
	issues := recs(&model.Record{Key: "P-1"}, &model.Record{Key: "P-2"})
	ApplyLinks(issues, "P-1", []LinkChange{{Type: "blocks", To: "P-2"}}, nil, nil, false)
	wantRefs(t, "P-1 blocking", issues["P-1"].Blocking, "P-2")
	wantRefs(t, "P-2 blockedBy", issues["P-2"].BlockedBy, "P-1")
}

func TestNumbersAreStoredAsNumbersWithoutLeadingZeros(t *testing.T) {
	issues := recs(&model.Record{Key: "1"}, &model.Record{Key: "2"})
	ApplyLinks(issues, "1", []LinkChange{{Type: "blocks", To: "2"}}, nil, nil, false)
	b := string(mustMarshal(t, issues["1"].Blocking))
	if b != `[2]` {
		t.Fatalf("stored as %s, want [2] (a number, as a sync writes it)", b)
	}
}

func TestAnUnknownKeyOrTypeChangesNothing(t *testing.T) {
	issues := recs(&model.Record{Key: "1"})
	ApplyLinks(issues, "9", []LinkChange{{Type: "blocks", To: "1"}}, nil, nil, false)
	ApplyLinks(issues, "1", []LinkChange{{Type: "relates", To: "1"}}, nil, nil, false)
	if len(issues) != 1 || issues["1"].Blocking != nil || issues["1"].BlockedBy != nil {
		t.Fatalf("issues changed: %+v", issues["1"])
	}
}

func TestNotesEditedAndDeletedAreAppliedToTheRecord(t *testing.T) {
	issues := recs(&model.Record{Key: "1", Comments: []model.Comment{{ID: "10", Body: "a"}, {ID: "11", Body: "b"}}, CommentsCount: 2})
	ApplyComments(issues, "1", []CommentEdit{{ID: "10", Body: "A"}}, []string{"11"})
	want := []model.Comment{{ID: "10", Body: "A"}}
	if !reflect.DeepEqual(issues["1"].Comments, want) {
		t.Fatalf("comments %+v", issues["1"].Comments)
	}
	if issues["1"].CommentsCount != 1 {
		t.Fatalf("commentsCount %d", issues["1"].CommentsCount)
	}
}

func TestACommentsEditOnARecordWithoutACommentsListChangesNothing(t *testing.T) {
	issues := recs(&model.Record{Key: "1"})
	ApplyComments(issues, "1", []CommentEdit{{ID: "1", Body: "x"}}, []string{"1"})
	if issues["1"].Comments != nil || issues["1"].CommentsCount != 0 {
		t.Fatalf("changed: %+v", issues["1"])
	}
}

func TestACountIsKeptWhenNoNoteIsDeleted(t *testing.T) {
	issues := recs(&model.Record{Key: "1", Comments: []model.Comment{{ID: "1", Body: "a"}}, CommentsCount: 7})
	ApplyComments(issues, "1", []CommentEdit{{ID: "1", Body: "b"}}, nil)
	if issues["1"].CommentsCount != 7 || issues["1"].Comments[0].Body != "b" {
		t.Fatalf("got %+v", issues["1"])
	}
}

// Copy on write: the record an edit changes is replaced by a copy, and the record it copies is not changed.

func TestApplyLinksLeavesTheOldRecordAndItsSlicesAsTheyWere(t *testing.T) {
	old := &model.Record{Key: "1", BlockedBy: refs("2"), Blocking: refs("3"), Parent: refPtr("4")}
	other := &model.Record{Key: "2", Blocking: refs("1")}
	third := &model.Record{Key: "3", BlockedBy: refs("1")}
	parent := &model.Record{Key: "4", SubIssues: refs("1")}
	issues := map[string]*model.Record{"1": old, "2": other, "3": third, "4": parent}

	ApplyLinks(issues, "1",
		[]LinkChange{{Type: "blocks", To: "5"}},
		[]LinkChange{{Type: "blockedBy", To: "2"}, {Type: "blocks", To: "3"}},
		refPtr("6"), true)

	if issues["1"] == old {
		t.Fatal("the changed record must be a new one")
	}
	wantRefs(t, "old BlockedBy", old.BlockedBy, "2")
	wantRefs(t, "old Blocking", old.Blocking, "3")
	if old.Parent == nil || *old.Parent != "4" {
		t.Fatalf("old parent changed: %v", old.Parent)
	}
	wantRefs(t, "old parent's SubIssues", parent.SubIssues, "1")
	wantRefs(t, "old other's Blocking", other.Blocking, "1")
	wantRefs(t, "old third's BlockedBy", third.BlockedBy, "1")

	wantRefs(t, "new BlockedBy", issues["1"].BlockedBy)
	wantRefs(t, "new Blocking", issues["1"].Blocking, "5")
	if issues["1"].Parent == nil || *issues["1"].Parent != "6" {
		t.Fatalf("new parent: %v", issues["1"].Parent)
	}
	if issues["2"] == other || issues["3"] == third || issues["4"] == parent {
		t.Fatal("the other records an edit changes are copies too")
	}
	wantRefs(t, "new other's Blocking", issues["2"].Blocking)
	wantRefs(t, "new third's BlockedBy (1 blocks 3 was removed)", issues["3"].BlockedBy)
	wantRefs(t, "old parent 4's copy (1 left it)", issues["4"].SubIssues)
}

func TestApplyLinksDoesNotShareASliceWithTheOldRecord(t *testing.T) {
	// the list has room to grow: an append must not write into the old record's array
	list := make([]model.Ref, 1, 8)
	list[0] = "2"
	old := &model.Record{Key: "1", BlockedBy: list}
	issues := map[string]*model.Record{"1": old, "2": {Key: "2"}, "3": {Key: "3"}}
	ApplyLinks(issues, "1", []LinkChange{{Type: "blockedBy", To: "3"}}, nil, nil, false)
	wantRefs(t, "new", issues["1"].BlockedBy, "2", "3")
	full := list[:cap(list)]
	if full[1] != "" {
		t.Fatalf("the old record's array was written: %q", full)
	}
}

func TestApplyLinksToItselfChangesOneCopy(t *testing.T) {
	old := &model.Record{Key: "1"}
	issues := map[string]*model.Record{"1": old}
	ApplyLinks(issues, "1", []LinkChange{{Type: "blocks", To: "1"}}, nil, nil, false)
	if issues["1"] == old {
		t.Fatal("a copy must replace the record")
	}
	wantRefs(t, "blocking", issues["1"].Blocking, "1")
	wantRefs(t, "blockedBy", issues["1"].BlockedBy, "1")
	if old.Blocking != nil || old.BlockedBy != nil {
		t.Fatal("the old record is unchanged")
	}
}

func TestApplyCommentsLeavesTheOldRecordAsItWas(t *testing.T) {
	old := &model.Record{Key: "1", Comments: []model.Comment{{ID: "10", Body: "a"}, {ID: "11", Body: "b"}}, CommentsCount: 2}
	issues := map[string]*model.Record{"1": old}
	ApplyComments(issues, "1", []CommentEdit{{ID: "10", Body: "A"}}, []string{"11"})
	if issues["1"] == old {
		t.Fatal("a copy must replace the record")
	}
	if len(old.Comments) != 2 || old.Comments[0].Body != "a" || old.CommentsCount != 2 {
		t.Fatalf("old record changed: %+v", old)
	}
}

func TestTheStoredJSONOfAChangedRecordIsWhatASyncWrites(t *testing.T) {
	issues := recs(&model.Record{Key: "1", Kind: "issue", ID: "1"}, &model.Record{Key: "2", Kind: "issue", ID: "2"})
	ApplyLinks(issues, "1", []LinkChange{{Type: "blocks", To: "2"}}, nil, nil, false)
	var m map[string]json.RawMessage
	if err := json.Unmarshal(mustMarshal(t, issues["2"]), &m); err != nil {
		t.Fatal(err)
	}
	if string(m["blockedBy"]) != "[1]" {
		t.Fatalf("blockedBy written as %s", m["blockedBy"])
	}
}
