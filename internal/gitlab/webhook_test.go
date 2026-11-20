package gitlab

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"gitlab.com/system4us/issue-timeline/internal/provider"
)

const hookURL = "https://timeline.example.com/api/webhook"

func TestEnsureWebhookCreates(t *testing.T) {
	f := newFakeHost(t)
	f.route("GET", projPath+"/hooks", jsonReply(200, []map[string]any{{"id": 3, "url": "https://other.example.com/x"}}))
	f.route("POST", projPath+"/hooks", jsonReply(201, map[string]any{"id": 4}))
	r := testRepo(t, f)
	action, err := r.EnsureWebhook(context.Background(), provider.HookSpec{URL: hookURL, Secret: "s3cret"})
	if err != nil || action != "created" {
		t.Fatalf("action %q, err %v", action, err)
	}
	posts := f.callsTo("POST", projPath+"/hooks")
	if len(posts) != 1 {
		t.Fatalf("posts: %d", len(posts))
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(posts[0].body), &body); err != nil {
		t.Fatal(err)
	}
	if body["url"] != hookURL || body["token"] != "s3cret" || body["enable_ssl_verification"] != true {
		t.Fatalf("body %+v", body)
	}
	for _, k := range []string{"push_events", "note_events", "confidential_note_events", "issues_events", "confidential_issues_events", "merge_requests_events", "pipeline_events"} {
		if body[k] != true {
			t.Fatalf("%s is not on: %+v", k, body)
		}
	}
}

func TestEnsureWebhookUpdatesTheOneThatPointsHere(t *testing.T) {
	f := newFakeHost(t)
	f.route("GET", projPath+"/hooks", jsonReply(200, []map[string]any{{"id": 3, "url": hookURL}}))
	f.route("PUT", projPath+"/hooks/3", jsonReply(200, map[string]any{"id": 3}))
	r := testRepo(t, f)
	action, err := r.EnsureWebhook(context.Background(), provider.HookSpec{URL: hookURL, Secret: "new"})
	if err != nil || action != "updated" {
		t.Fatalf("action %q, err %v", action, err)
	}
	if len(f.callsTo("POST", projPath+"/hooks")) != 0 {
		t.Fatal("created a second hook")
	}
}

func TestEnsureWebhookRefused(t *testing.T) {
	f := newFakeHost(t)
	f.route("GET", projPath+"/hooks", jsonReply(403, map[string]any{"message": "403 Forbidden"}))
	r := testRepo(t, f)
	_, err := r.EnsureWebhook(context.Background(), provider.HookSpec{URL: hookURL, Secret: "s"})
	if got := errorStatus(err); got != 403 {
		t.Fatalf("status %d, err %v", got, err)
	}
}

func TestRemoveWebhook(t *testing.T) {
	f := newFakeHost(t)
	f.route("GET", projPath+"/hooks", jsonReply(200, []map[string]any{{"id": 3, "url": hookURL}}))
	f.route("DELETE", projPath+"/hooks/3", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	r := testRepo(t, f)
	removed, err := r.RemoveWebhook(context.Background(), hookURL)
	if err != nil || !removed {
		t.Fatalf("removed %v, err %v", removed, err)
	}
	removed, err = r.RemoveWebhook(context.Background(), "https://nobody.example.com/api/webhook")
	if err != nil || removed {
		t.Fatalf("removed %v, err %v", removed, err)
	}
}
