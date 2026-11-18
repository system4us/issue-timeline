package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/provider"
)

// Refresh reads one record again (an issue, or a pull request by its number) and returns the copy the store should keep:
// rec with what GitHub says now merged on. A milestone with the same title keeps its due date (GitHub's milestone answer
// has none of its own). ErrNotFound when GitHub no longer shows it.
func (r *Repo) Refresh(ctx context.Context, kind string, rec *model.Record) (*model.Record, error) {
	out, err := r.refresh(ctx, kind, rec)
	return out, asProvider(err)
}

func (r *Repo) refresh(ctx context.Context, kind string, rec *model.Record) (*model.Record, error) {
	number, err := provider.PathID(rec.Key, "the number")
	if err != nil {
		return nil, err
	}
	tok, err := r.serverToken(ctx)
	if err != nil {
		return nil, err
	}
	f, err := r.readOne(ctx, tok, number, kind)
	if err != nil {
		var he *hostError
		if errors.As(err, &he) && he.Status == http.StatusNotFound {
			return nil, fmt.Errorf("%w: %s #%s", provider.ErrNotFound, r.Key(), number)
		}
		return nil, err
	}
	out := *rec
	applyIssueState(&out, f.issueState)
	if f.pull != nil {
		out.IsDraft = model.Ptr(f.pull.isDraft)
		out.Reviewers = f.pull.reviewers
		out.State = f.pull.state
	}
	if f.issueState.Milestone != nil && rec.Milestone != nil && rec.Milestone.Title == f.issueState.Milestone.Title && rec.Milestone.DueOn != nil {
		m := *f.issueState.Milestone
		m.DueOn = rec.Milestone.DueOn
		out.Milestone = &m
	}
	return &out, nil
}

// readFields is one read of a record: the issue fields, and for a pull request its draft, state and reviewers.
type readFields struct {
	issueState
	pull *pullRead
}

type pullRead struct {
	isDraft   bool
	state     string
	reviewers []string
}

// readOne reads one issue or pull request by its number (REST). A pull request is read as an issue, then as a pull with its
// requested reviewers.
func (r *Repo) readOne(ctx context.Context, tok, number, kind string) (readFields, error) {
	var i restIssue
	if err := r.api.rest(ctx, "GET", r.repoURL()+"/issues/"+number, tok, nil, &i); err != nil {
		return readFields{}, err
	}
	out := readFields{issueState: issueStateOf(i)}
	if kind != "pull" {
		return out, nil
	}
	var p restPull
	if err := r.api.rest(ctx, "GET", r.repoURL()+"/pulls/"+number, tok, nil, &p); err != nil {
		return readFields{}, err
	}
	var rr struct {
		Users []restUser `json:"users"`
	}
	if err := r.api.rest(ctx, "GET", r.repoURL()+"/pulls/"+number+"/requested_reviewers", tok, nil, &rr); err != nil {
		return readFields{}, err
	}
	out.State = pullStateOf(p)
	out.pull = &pullRead{isDraft: p.Draft, state: out.State, reviewers: loginsOfUsers(rr.Users)}
	return out, nil
}
