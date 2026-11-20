package gitlab

import (
	"context"
	"encoding/json"
	"testing"

	"gitlab.com/system4us/issue-timeline/internal/provider"
)

func TestCreateLabelAndMilestone(t *testing.T) {
	f := newFakeHost(t)
	f.route("POST", projPath+"/labels", jsonReply(201, map[string]any{"name": "p1", "color": "#0e8a16"}))
	f.route("POST", projPath+"/milestones", jsonReply(201, map[string]any{"id": 12, "title": "v2"}))
	r := testRepo(t, f)
	label, err := r.CreateEntity(context.Background(), provider.Cred{Token: "me"}, provider.NewEntity{Kind: "label", Name: "p1", Color: "0e8a16"})
	if err != nil || label["name"] != "p1" || label["color"] != "#0e8a16" {
		t.Fatalf("label %v, %v", label, err)
	}
	ms, err := r.CreateEntity(context.Background(), provider.Cred{}, provider.NewEntity{Kind: "milestone", Name: "v2", DueOn: "2026-12-31"})
	if err != nil || ms["id"] != 12 || ms["name"] != "v2" {
		t.Fatalf("milestone %v, %v", ms, err)
	}
	var lb, mb map[string]any
	_ = json.Unmarshal([]byte(f.callsTo("POST", projPath+"/labels")[0].body), &lb)
	_ = json.Unmarshal([]byte(f.callsTo("POST", projPath+"/milestones")[0].body), &mb)
	if lb["color"] != "#0e8a16" {
		t.Fatalf("GitLab wants a # colour: %v", lb)
	}
	if mb["due_date"] != "2026-12-31" {
		t.Fatalf("milestone body %v", mb)
	}
}

func TestCreateLabelAnExistingNameIsAProviderError(t *testing.T) {
	f := newFakeHost(t)
	f.route("POST", projPath+"/labels", jsonReply(409, map[string]any{"message": "Label already exists"}))
	r := testRepo(t, f)
	_, err := r.CreateEntity(context.Background(), provider.Cred{Token: "me"}, provider.NewEntity{Kind: "label", Name: "bug"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if errorStatus(err) == 0 {
		t.Fatalf("not a provider error: %v", err)
	}
}
