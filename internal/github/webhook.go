package github

import (
	"context"
	"net/http"
	"strconv"

	"gitlab.com/system4us/issue-timeline/internal/provider"
)

// hookEvents are the deliveries the server reads (the ones the README asks for by hand).
var hookEvents = []string{"issues", "issue_comment", "pull_request", "pull_request_review", "pull_request_review_comment", "push", "workflow_run"}

type restHook struct {
	ID     int `json:"id"`
	Config struct {
		URL string `json:"url"`
	} `json:"config"`
}

// findHook is the repo's hook that posts to url (0 when there is none).
func (r *Repo) findHook(ctx context.Context, tok, url string) (int, error) {
	hooks, err := restAll[restHook](ctx, r.api, r.repoURL()+"/hooks?per_page=100", tok, 10)
	if err != nil {
		return 0, err
	}
	for _, h := range hooks {
		if h.Config.URL == url {
			return h.ID, nil
		}
	}
	return 0, nil
}

// EnsureWebhook creates the repo's webhook, or updates the one that already posts to spec.URL (its secret and events).
func (r *Repo) EnsureWebhook(ctx context.Context, spec provider.HookSpec) (string, error) {
	action, err := r.ensureWebhook(ctx, spec)
	return action, asProvider(err)
}

func (r *Repo) ensureWebhook(ctx context.Context, spec provider.HookSpec) (string, error) {
	tok, err := r.serverToken(ctx)
	if err != nil {
		return "", err
	}
	id, err := r.findHook(ctx, tok, spec.URL)
	if err != nil {
		return "", err
	}
	body := map[string]any{
		"active": true,
		"events": hookEvents,
		"config": map[string]any{"url": spec.URL, "content_type": "json", "secret": spec.Secret, "insecure_ssl": "0"},
	}
	if id != 0 {
		return "updated", r.api.rest(ctx, http.MethodPatch, r.repoURL()+"/hooks/"+strconv.Itoa(id), tok, body, nil)
	}
	body["name"] = "web"
	return "created", r.api.rest(ctx, http.MethodPost, r.repoURL()+"/hooks", tok, body, nil)
}

// RemoveWebhook deletes the repo's webhook that posts to url.
func (r *Repo) RemoveWebhook(ctx context.Context, url string) (bool, error) {
	removed, err := r.removeWebhook(ctx, url)
	return removed, asProvider(err)
}

func (r *Repo) removeWebhook(ctx context.Context, url string) (bool, error) {
	tok, err := r.serverToken(ctx)
	if err != nil {
		return false, err
	}
	id, err := r.findHook(ctx, tok, url)
	if err != nil || id == 0 {
		return false, err
	}
	return true, r.api.rest(ctx, http.MethodDelete, r.repoURL()+"/hooks/"+strconv.Itoa(id), tok, nil, nil)
}
