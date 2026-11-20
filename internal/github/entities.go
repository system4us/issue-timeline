package github

import (
	"context"
	"fmt"

	"gitlab.com/system4us/issue-timeline/internal/provider"
)

// EntityKinds are what can be created on a GitHub repo.
func (r *Repo) EntityKinds() []string {
	return []string{provider.EntityLabel, provider.EntityMilestone}
}

// CreateEntity creates a label or a milestone with the caller's credential.
func (r *Repo) CreateEntity(ctx context.Context, cred provider.Cred, e provider.NewEntity) (map[string]any, error) {
	out, err := r.createEntity(ctx, cred, e)
	return out, asProvider(err)
}

func (r *Repo) createEntity(ctx context.Context, cred provider.Cred, e provider.NewEntity) (map[string]any, error) {
	tok := cred.Token
	if tok == "" {
		var err error
		if tok, err = r.serverToken(ctx); err != nil {
			return nil, err
		}
	}
	switch e.Kind {
	case provider.EntityLabel:
		color := e.Color
		if color == "" {
			color = "ededed"
		}
		var made restLabel
		body := map[string]any{"name": e.Name, "color": color, "description": e.Description}
		if err := r.api.rest(ctx, "POST", r.repoURL()+"/labels", tok, body, &made); err != nil {
			return nil, err
		}
		return map[string]any{"name": made.Name, "color": derefOr(made.Color)}, nil
	case provider.EntityMilestone:
		body := map[string]any{"title": e.Name, "description": e.Description}
		if e.DueOn != "" {
			body["due_on"] = e.DueOn + "T00:00:00Z"
		}
		var made restMilestone
		if err := r.api.rest(ctx, "POST", r.repoURL()+"/milestones", tok, body, &made); err != nil {
			return nil, err
		}
		return map[string]any{"id": made.Number, "name": made.Title}, nil
	}
	return nil, provider.Errorf(400, "GitHub cannot create a %s", fmt.Sprint(e.Kind))
}
