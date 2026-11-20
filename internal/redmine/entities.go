package redmine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"gitlab.com/system4us/issue-timeline/internal/provider"
)

// EntityKinds are what can be created on a Redmine project. Redmine's milestone is a version.
func (r *Repo) EntityKinds() []string {
	return []string{provider.EntityVersion, provider.EntityCategory}
}

// CreateEntity creates a version or an issue category with the caller's key; it needs the right to manage the project's
// versions or categories, and Redmine says so with a 403 otherwise.
func (r *Repo) CreateEntity(ctx context.Context, cred provider.Cred, e provider.NewEntity) (map[string]any, error) {
	c, err := r.clientFor(ctx, cred)
	if err != nil {
		return nil, err
	}
	base := "/projects/" + url.PathEscape(r.identifier)
	var path, wrap string
	payload := map[string]any{"name": e.Name}
	switch e.Kind {
	case provider.EntityVersion:
		path, wrap = base+"/versions.json", "version"
		payload["status"] = "open"
		if e.Description != "" {
			payload["description"] = e.Description
		}
		if e.DueOn != "" {
			payload["effective_date"] = e.DueOn
		}
	case provider.EntityCategory:
		path, wrap = base+"/issue_categories.json", "issue_category"
	default:
		return nil, provider.Errorf(400, "Redmine cannot create a %s", e.Kind)
	}
	body, err := json.Marshal(map[string]any{wrap: payload})
	if err != nil {
		return nil, err
	}
	var made map[string]struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	if err := c.do(ctx, http.MethodPost, path, nil, "application/json", body, &made); err != nil {
		return nil, err
	}
	m := made[wrap]
	return map[string]any{"id": m.ID, "name": m.Name}, nil
}
