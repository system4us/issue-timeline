package github

import (
	"context"
	"encoding/json"
	"testing"

	"gitlab.com/system4us/issue-timeline/internal/provider"
)

const hookURL = "https://timeline.example.com/api/webhook"

func TestEnsureWebhookCreates(t *testing.T) {
	f := &fakeHost{routes: map[string]route{
		"GET /repos/o/r/hooks":  {200, `[{"id":7,"config":{"url":"https://other.example.com/hook"}}]`},
		"POST /repos/o/r/hooks": {201, `{"id":8}`},
	}}
	r, _ := testRepo(t, f)
	action, err := r.EnsureWebhook(context.Background(), provider.HookSpec{URL: hookURL, Secret: "s3cret"})
	if err != nil || action != "created" {
		t.Fatalf("action %q, err %v", action, err)
	}
	var posted struct {
		Name   string
		Active bool
		Events []string
		Config map[string]string
	}
	for _, c := range f.got() {
		if c.Method == "POST" {
			if err := json.Unmarshal([]byte(c.Body), &posted); err != nil {
				t.Fatal(err)
			}
		}
	}
	if posted.Name != "web" || !posted.Active || posted.Config["url"] != hookURL || posted.Config["secret"] != "s3cret" || posted.Config["content_type"] != "json" || posted.Config["insecure_ssl"] != "0" {
		t.Fatalf("posted %+v", posted)
	}
	want := map[string]bool{"issues": true, "issue_comment": true, "pull_request": true, "pull_request_review": true, "pull_request_review_comment": true, "push": true, "workflow_run": true}
	for _, e := range posted.Events {
		delete(want, e)
	}
	if len(want) != 0 {
		t.Fatalf("events missing: %v (got %v)", want, posted.Events)
	}
}

func TestEnsureWebhookUpdatesTheOneThatPointsHere(t *testing.T) {
	f := &fakeHost{routes: map[string]route{
		"GET /repos/o/r/hooks":     {200, `[{"id":7,"config":{"url":"` + hookURL + `"}}]`},
		"PATCH /repos/o/r/hooks/7": {200, `{"id":7}`},
	}}
	r, _ := testRepo(t, f)
	action, err := r.EnsureWebhook(context.Background(), provider.HookSpec{URL: hookURL, Secret: "new"})
	if err != nil || action != "updated" {
		t.Fatalf("action %q, err %v", action, err)
	}
	for _, c := range f.got() {
		if c.Method == "POST" {
			t.Fatalf("created a second hook: %+v", c)
		}
	}
}

func TestEnsureWebhookRefusedIsAProviderError(t *testing.T) {
	f := &fakeHost{routes: map[string]route{"GET /repos/o/r/hooks": {404, `{"message":"Not Found"}`}}}
	r, _ := testRepo(t, f)
	_, err := r.EnsureWebhook(context.Background(), provider.HookSpec{URL: hookURL, Secret: "s"})
	var pe *provider.Error
	if err == nil || !asPE(err, &pe) {
		t.Fatalf("want a provider error, got %v", err)
	}
}

func TestRemoveWebhook(t *testing.T) {
	f := &fakeHost{routes: map[string]route{
		"GET /repos/o/r/hooks":      {200, `[{"id":7,"config":{"url":"` + hookURL + `"}}]`},
		"DELETE /repos/o/r/hooks/7": {204, ``},
	}}
	r, _ := testRepo(t, f)
	removed, err := r.RemoveWebhook(context.Background(), hookURL)
	if err != nil || !removed {
		t.Fatalf("removed %v, err %v", removed, err)
	}
	removed, err = r.RemoveWebhook(context.Background(), "https://nobody.example.com/api/webhook")
	if err != nil || removed {
		t.Fatalf("a hook that is not there: removed %v, err %v", removed, err)
	}
}

func asPE(err error, target **provider.Error) bool {
	pe, ok := err.(*provider.Error)
	if ok {
		*target = pe
	}
	return ok
}
