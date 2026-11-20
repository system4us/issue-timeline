package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"gitlab.com/system4us/issue-timeline/internal/provider"
)

// EntityKinds are what can be created on a GitLab project.
func (r *Repo) EntityKinds() []string {
	return []string{provider.EntityLabel, provider.EntityMilestone}
}

// CreateEntity creates a label or a milestone with the caller's credential (the server's when nobody signed in).
func (r *Repo) CreateEntity(ctx context.Context, cred provider.Cred, e provider.NewEntity) (map[string]any, error) {
	tok, err := r.credToken(ctx, cred)
	if err != nil {
		return nil, err
	}
	c := r.newClient(tok, cred.Token == "", nil)
	switch e.Kind {
	case provider.EntityLabel:
		color := e.Color
		if color == "" {
			color = "ededed"
		}
		res, err := c.call(ctx, http.MethodPost, c.apiURL(r.projectPath("labels"), nil), map[string]any{"name": e.Name, "color": "#" + color, "description": e.Description})
		if err != nil {
			return nil, err
		}
		var made glLabel
		if err := json.Unmarshal(res.body, &made); err != nil {
			return nil, fmt.Errorf("reading the label GitLab made: %w", err)
		}
		return map[string]any{"name": made.Name, "color": "#" + stripHash(deref(made.Color))}, nil
	case provider.EntityMilestone:
		body := map[string]any{"title": e.Name, "description": e.Description}
		if e.DueOn != "" {
			body["due_date"] = e.DueOn
		}
		res, err := c.call(ctx, http.MethodPost, c.apiURL(r.projectPath("milestones"), nil), body)
		if err != nil {
			return nil, err
		}
		var made glMilestoneRow
		if err := json.Unmarshal(res.body, &made); err != nil {
			return nil, fmt.Errorf("reading the milestone GitLab made: %w", err)
		}
		return map[string]any{"id": made.ID, "name": made.Title}, nil
	}
	return nil, provider.Errorf(400, "GitLab cannot create a %s", e.Kind)
}
