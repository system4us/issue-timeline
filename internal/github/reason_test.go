package github

import "testing"

func TestTheRESTStateReasonIsKeptAsTheSyncKeepsIt(t *testing.T) {
	r := "not_planned"
	if got := issueStateOf(restIssue{State: "closed", StateReason: &r}).StateReason; got == nil || *got != "NOT_PLANNED" {
		t.Fatalf("state reason: %v", got)
	}
}
