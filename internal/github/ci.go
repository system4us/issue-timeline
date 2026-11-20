package github

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"gitlab.com/system4us/issue-timeline/internal/model"
)

// The CI runs are GitHub Actions runs. What they become (ci, dependabot, pages, other) is decided by Options.Categorize
// (ci.Categorize), which the caller passes in: a run's category is never set here.

const (
	runsPerPage      = 100
	maxRunPages      = 100
	maxWorkflowPages = 10
)

// restRun is one Actions run, as GitHub gives it.
type restRun struct {
	ID         int64     `json:"id"`
	HTMLURL    string    `json:"html_url"`
	Name       *string   `json:"name"`
	Path       *string   `json:"path"`
	Event      *string   `json:"event"`
	Actor      *restUser `json:"actor"`
	HeadBranch *string   `json:"head_branch"`
	HeadSha    string    `json:"head_sha"`
	Status     string    `json:"status"`
	Conclusion *string   `json:"conclusion"`
	CreatedAt  string    `json:"created_at"`
}

// CIRuns reads the Actions runs created since `since` (by day, as the API filters), newest first.
func (r *Repo) CIRuns(ctx context.Context, since time.Time) ([]model.CIRun, error) {
	runs, err := r.ciRuns(ctx, since)
	return runs, asProvider(err)
}

func (r *Repo) ciRuns(ctx context.Context, since time.Time) ([]model.CIRun, error) {
	tok, err := r.serverToken(ctx)
	if err != nil {
		return nil, err
	}
	day := since.UTC().Format("2006-01-02")
	out := []model.CIRun{}
	for page := 1; page <= maxRunPages; page++ {
		u := fmt.Sprintf("%s/actions/runs?per_page=%d&page=%d&created=%s", r.repoURL(), runsPerPage, page, url.QueryEscape(">="+day))
		var body struct {
			WorkflowRuns []restRun `json:"workflow_runs"`
		}
		if err := r.api.rest(ctx, "GET", u, tok, nil, &body); err != nil {
			return nil, err
		}
		for _, w := range body.WorkflowRuns {
			run := r.runOf(w)
			out = append(out, run)
		}
		if len(body.WorkflowRuns) < runsPerPage {
			break
		}
	}
	return out, nil
}

// runOf is an Actions run as the store keeps it, with its category.
func (r *Repo) runOf(w restRun) model.CIRun {
	run := model.CIRun{
		ID: strconv.FormatInt(w.ID, 10), URL: w.HTMLURL, Name: derefOr(w.Name), Path: derefOr(w.Path), Event: derefOr(w.Event),
		Branch: w.HeadBranch, Sha: w.HeadSha, Status: w.Status, Conclusion: w.Conclusion, At: w.CreatedAt,
	}
	if w.Actor != nil {
		run.Actor = w.Actor.Login
	}
	if r.o.Categorize != nil {
		run.Category = r.o.Categorize(run)
	}
	return run
}

// restWorkflowCommit is a commit that touched the workflow files, as the commits API lists it.
type restWorkflowCommit struct {
	SHA     string `json:"sha"`
	HTMLURL string `json:"html_url"`
	Commit  *struct {
		Message string `json:"message"`
		Author  *struct {
			Name string `json:"name"`
			Date string `json:"date"`
		} `json:"author"`
		Committer *struct {
			Date string `json:"date"`
		} `json:"committer"`
	} `json:"commit"`
	Author *restUser `json:"author"`
}

// WorkflowChanges reads the commits that changed the CI definition (.github/workflows) since `since`, newest first.
func (r *Repo) WorkflowChanges(ctx context.Context, since time.Time) ([]model.WorkflowChange, error) {
	out, err := r.workflowChanges(ctx, since)
	return out, asProvider(err)
}

func (r *Repo) workflowChanges(ctx context.Context, since time.Time) ([]model.WorkflowChange, error) {
	tok, err := r.serverToken(ctx)
	if err != nil {
		return nil, err
	}
	out := []model.WorkflowChange{}
	for page := 1; page <= maxWorkflowPages; page++ {
		u := fmt.Sprintf("%s/commits?path=%s&since=%s&per_page=%d&page=%d", r.repoURL(), url.QueryEscape(".github/workflows"),
			url.QueryEscape(since.UTC().Format(isoMillis)), runsPerPage, page)
		var rows []restWorkflowCommit
		if err := r.api.rest(ctx, "GET", u, tok, nil, &rows); err != nil {
			return nil, err
		}
		for _, c := range rows {
			ch := model.WorkflowChange{Sha: c.SHA, URL: strPtr(c.HTMLURL)}
			if c.Commit != nil {
				ch.Title = firstLine(c.Commit.Message)
				if c.Commit.Committer != nil && c.Commit.Committer.Date != "" {
					ch.At = strPtr(c.Commit.Committer.Date)
				} else if c.Commit.Author != nil && c.Commit.Author.Date != "" {
					ch.At = strPtr(c.Commit.Author.Date)
				}
			}
			switch {
			case c.Author != nil:
				ch.Author = strPtr(c.Author.Login)
			case c.Commit != nil && c.Commit.Author != nil:
				ch.Author = strPtr(c.Commit.Author.Name)
			}
			out = append(out, ch)
		}
		if len(rows) < runsPerPage {
			break
		}
	}
	return out, nil
}

func strPtr(s string) *string { return &s }
