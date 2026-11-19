package model

import (
	"encoding/json"
	"testing"
)

func TestRefReadsNumbersAndTextAndWritesThemBack(t *testing.T) {
	var refs []Ref
	if err := json.Unmarshal([]byte(`[12, "12", "PROJ-3", 0]`), &refs); err != nil {
		t.Fatal(err)
	}
	if refs[0] != refs[1] {
		t.Fatalf("12 and \"12\" are the same record: %q %q", refs[0], refs[1])
	}
	out, _ := json.Marshal(refs)
	if string(out) != `[12,12,"PROJ-3",0]` {
		t.Fatalf("written as %s", out)
	}
	if got := DropRef([]Ref{"1", "2"}, "2"); len(got) != 1 || got[0] != "1" {
		t.Fatalf("DropRef: %v", got)
	}
	if got := AddRef([]Ref{"1"}, "1"); len(got) != 1 {
		t.Fatalf("AddRef duplicated: %v", got)
	}
}

func TestRecordKeepsUnknownFieldsAndEventFields(t *testing.T) {
	in := `{"kind":"issue","id":"I_1","key":"7","number":7,"title":"t","body":"","url":"u","author":null,
	"createdAt":"a","updatedAt":"b","closedAt":null,"state":"OPEN","labels":[],"assignees":[],"milestone":null,
	"commentsCount":0,"comments":[],"events":[{"type":"Labeled","at":"c","actor":"ana","label":"bug"}],
	"blockedBy":[3],"futureField":{"x":1}}`
	var r Record
	if err := json.Unmarshal([]byte(in), &r); err != nil {
		t.Fatal(err)
	}
	var label string
	if !r.Events[0].Field("label", &label) || label != "bug" {
		t.Fatalf("event field lost: %+v", r.Events[0])
	}
	if r.BlockedBy[0] != "3" {
		t.Fatalf("blockedBy: %v", r.BlockedBy)
	}
	out, _ := json.Marshal(r)
	var back map[string]any
	_ = json.Unmarshal(out, &back)
	if back["futureField"] == nil {
		t.Fatalf("unknown field dropped: %s", out)
	}
	ev := back["events"].([]any)[0].(map[string]any)
	if ev["label"] != "bug" || ev["actor"] != "ana" {
		t.Fatalf("event not flat: %v", ev)
	}
	if back["blockedBy"].([]any)[0].(float64) != 3 {
		t.Fatalf("blockedBy not a number: %s", out)
	}
}

func TestRecordsAlwaysCarryTheFieldsOfTheirKind(t *testing.T) {
	out, _ := json.Marshal(Record{Kind: "issue", Key: "1"})
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	if v, ok := m["stateReason"]; !ok || v != nil {
		t.Fatalf("an issue without stateReason: %s", out)
	}
	out, _ = json.Marshal(Record{Kind: "pull", Key: "2", Closes: []Ref{"3"}})
	m = nil
	_ = json.Unmarshal(out, &m)
	if m["isDraft"] != false || m["mergedAt"] != nil || m["additions"].(float64) != 0 || len(m["closes"].([]any)) != 1 {
		t.Fatalf("pull defaults: %s", out)
	}
}

func TestAddCommentCopiesTheListAndNeverDoubles(t *testing.T) {
	stored := make([]Comment, 1, 2) // room after it: an append in place would write into stored's array
	stored[0] = Comment{ID: "a"}
	rec := Record{Comments: stored, CommentsCount: 3}
	AddComment(&rec, Comment{ID: "b"})
	AddComment(&rec, Comment{ID: "b"})
	if len(rec.Comments) != 2 || rec.CommentsCount != 4 {
		t.Fatalf("one new comment, the count up by one: %+v %d", rec.Comments, rec.CommentsCount)
	}
	if stored[:2][1].ID == "b" {
		t.Fatal("the stored list was written to")
	}
}
