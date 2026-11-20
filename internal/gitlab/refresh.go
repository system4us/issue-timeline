package gitlab

import (
	"context"
	"encoding/json"
	"fmt"

	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/provider"
)

// readState is an issue's or merge request's state as a read gives it (the edit and the refresh use it).
func readState(s string) string {
	switch s {
	case "merged":
		return "MERGED"
	case "closed":
		return "CLOSED"
	}
	return "OPEN"
}

// keepColors gives the labels the colors the record had for the same names, and the default for the new ones: a read
// does not say the colors.
func keepColors(names []string, old []model.Label) []model.Label {
	out := make([]model.Label, 0, len(names))
	for _, n := range names {
		color := defaultColor
		for _, l := range old {
			if l.Name == n {
				color = l.Color
				break
			}
		}
		out = append(out, model.Label{Name: n, Color: color})
	}
	return out
}

// labelNames is the names of the labels a host gave.
func labelNames(raw []json.RawMessage) []string {
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		out = append(out, labelName(r))
	}
	return out
}

// Refresh reads one issue or merge request again and returns the record with the fields the host gives now merged onto
// rec. A 404 is provider.ErrNotFound.
func (r *Repo) Refresh(ctx context.Context, kind string, rec *model.Record) (*model.Record, error) {
	iid, err := provider.PathID(rec.Key, "the record number")
	if err != nil {
		return nil, err
	}
	tok, err := r.serverToken(ctx)
	if err != nil {
		return nil, err
	}
	c := r.newClient(tok, true, nil)
	out := *rec
	if kind == "pull" {
		m, err := getOne[glMergeRequest](ctx, c, r.projectPath("merge_requests/"+iid), nil)
		if err != nil {
			return nil, err
		}
		if m == nil {
			return nil, notFound(r.key, iid)
		}
		out.Title = m.Title
		out.Body = deref(m.Description)
		out.State = readState(m.State)
		out.Labels = keepColors(labelNames(m.Labels), rec.Labels)
		out.Assignees = userNames(m.Assignees)
		out.Milestone = keepDueOn(milestoneOf(m.Milestone), rec.Milestone)
		out.IsDraft = model.Ptr(isDraft(m))
		out.Reviewers = userNames(m.Reviewers)
		return &out, nil
	}
	i, err := getOne[glIssue](ctx, c, r.projectPath("issues/"+iid), nil)
	if err != nil {
		return nil, err
	}
	if i == nil {
		return nil, notFound(r.key, iid)
	}
	out.Title = i.Title
	out.Body = deref(i.Description)
	out.State = readState(i.State)
	out.Labels = keepColors(labelNames(i.Labels), rec.Labels)
	out.Assignees = userNames(i.Assignees)
	out.Milestone = keepDueOn(milestoneOf(i.Milestone), rec.Milestone)
	out.DueDate = i.DueDate
	out.Confidential = model.Ptr(i.Confidential)
	out.Weight = i.Weight
	return &out, nil
}

// keepDueOn keeps the due date the record had for the same milestone: a read of an issue does not always say it.
func keepDueOn(now, old *model.Milestone) *model.Milestone {
	if now == nil || old == nil || old.Title != now.Title || old.DueOn == nil {
		return now
	}
	cp := *now
	cp.DueOn = old.DueOn
	return &cp
}

func notFound(key, iid string) error {
	return fmt.Errorf("%w: %s #%s", provider.ErrNotFound, key, iid)
}
