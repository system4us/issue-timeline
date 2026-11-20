package github

import (
	"context"
	"encoding/json"
	"testing"

	"gitlab.com/system4us/issue-timeline/internal/provider"
)

func TestCreateLabelAndMilestone(t *testing.T) {
	f := &fakeHost{routes: map[string]route{
		"POST /repos/o/r/labels":     {201, `{"name":"p1","color":"0e8a16"}`},
		"POST /repos/o/r/milestones": {201, `{"number":4,"title":"v2"}`},
	}}
	r, _ := testRepo(t, f)
	if got := r.EntityKinds(); len(got) != 2 || got[0] != "label" || got[1] != "milestone" {
		t.Fatalf("kinds %v", got)
	}
	label, err := r.CreateEntity(context.Background(), provider.Cred{Token: "me"}, provider.NewEntity{Kind: "label", Name: "p1", Color: "0e8a16", Description: "urgent"})
	if err != nil || label["name"] != "p1" || label["color"] != "0e8a16" {
		t.Fatalf("label %v, %v", label, err)
	}
	ms, err := r.CreateEntity(context.Background(), provider.Cred{}, provider.NewEntity{Kind: "milestone", Name: "v2", DueOn: "2026-12-31"})
	if err != nil || ms["id"] != 4 || ms["name"] != "v2" {
		t.Fatalf("milestone %v, %v", ms, err)
	}
	var sent []map[string]any
	for _, c := range f.got() {
		if c.Method == "POST" {
			var b map[string]any
			_ = json.Unmarshal([]byte(c.Body), &b)
			sent = append(sent, b)
		}
	}
	if sent[0]["color"] != "0e8a16" || sent[0]["description"] != "urgent" {
		t.Fatalf("label body %v", sent[0])
	}
	if sent[1]["due_on"] != "2026-12-31T00:00:00Z" {
		t.Fatalf("milestone body %v", sent[1])
	}
}

func TestCreateLabelDefaultsTheColourAndSurfacesAnExistingName(t *testing.T) {
	f := &fakeHost{routes: map[string]route{"POST /repos/o/r/labels": {422, `{"message":"Validation Failed","errors":[{"code":"already_exists"}]}`}}}
	r, _ := testRepo(t, f)
	_, err := r.CreateEntity(context.Background(), provider.Cred{Token: "me"}, provider.NewEntity{Kind: "label", Name: "bug"})
	pe, ok := err.(*provider.Error)
	if !ok || pe.Status != 422 {
		t.Fatalf("want a 422 provider error, got %v", err)
	}
	var b map[string]any
	_ = json.Unmarshal([]byte(f.got()[0].Body), &b)
	if b["color"] != "ededed" {
		t.Fatalf("default colour %v", b["color"])
	}
}

func TestCreateEntityRefusesAKindItDoesNotHave(t *testing.T) {
	r, _ := testRepo(t, &fakeHost{})
	if _, err := r.CreateEntity(context.Background(), provider.Cred{Token: "me"}, provider.NewEntity{Kind: "version", Name: "x"}); err == nil {
		t.Fatal("GitHub has no versions")
	}
}
