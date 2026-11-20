package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"gitlab.com/system4us/issue-timeline/internal/provider"
)

// hookFlags are the triggers the server reads (the ones the README asks for by hand).
var hookFlags = map[string]any{
	"push_events": true, "note_events": true, "confidential_note_events": true, "issues_events": true,
	"confidential_issues_events": true, "merge_requests_events": true, "pipeline_events": true,
}

// findHook is the project's hook that posts to hookURL (0 when there is none).
func (r *Repo) findHook(ctx context.Context, c *client, hookURL string) (int, error) {
	for page := 1; page <= 10; page++ {
		res, err := c.call(ctx, http.MethodGet, c.apiURL(r.projectPath("hooks"), url.Values{"per_page": {"100"}, "page": {strconv.Itoa(page)}}), nil)
		if err != nil {
			return 0, err
		}
		var hooks []struct {
			ID  int    `json:"id"`
			URL string `json:"url"`
		}
		if err := json.Unmarshal(res.body, &hooks); err != nil {
			return 0, fmt.Errorf("reading the project's webhooks: %w", err)
		}
		for _, h := range hooks {
			if h.URL == hookURL {
				return h.ID, nil
			}
		}
		if len(hooks) < 100 {
			break
		}
	}
	return 0, nil
}

// EnsureWebhook creates the project's webhook, or updates the one that already posts to spec.URL (its token and triggers).
func (r *Repo) EnsureWebhook(ctx context.Context, spec provider.HookSpec) (string, error) {
	tok, err := r.serverToken(ctx)
	if err != nil {
		return "", err
	}
	c := r.newClient(tok, true, nil)
	id, err := r.findHook(ctx, c, spec.URL)
	if err != nil {
		return "", err
	}
	body := map[string]any{"url": spec.URL, "token": spec.Secret, "enable_ssl_verification": true}
	for k, v := range hookFlags {
		body[k] = v
	}
	if id != 0 {
		_, err = c.call(ctx, http.MethodPut, c.apiURL(r.projectPath("hooks/"+strconv.Itoa(id)), nil), body)
		return "updated", err
	}
	_, err = c.call(ctx, http.MethodPost, c.apiURL(r.projectPath("hooks"), nil), body)
	return "created", err
}

// RemoveWebhook deletes the project's webhook that posts to hookURL.
func (r *Repo) RemoveWebhook(ctx context.Context, hookURL string) (bool, error) {
	tok, err := r.serverToken(ctx)
	if err != nil {
		return false, err
	}
	c := r.newClient(tok, true, nil)
	id, err := r.findHook(ctx, c, hookURL)
	if err != nil || id == 0 {
		return false, err
	}
	_, err = c.call(ctx, http.MethodDelete, c.apiURL(r.projectPath("hooks/"+strconv.Itoa(id)), nil), nil)
	return true, err
}
