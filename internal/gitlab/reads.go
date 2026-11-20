package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/provider"
)

// GitLab's pipeline "source" in the words GitHub uses for the same trigger. A merge request pipeline is the PR's CI.
var gitlabSource = map[string]string{"merge_request_event": "pull_request", "external_pull_request_event": "pull_request"}

var commitSha = regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`)

// Exists reports whether the project is there and the server's own token can read it.
func (r *Repo) Exists(ctx context.Context) error {
	tok, err := r.serverToken(ctx)
	if err != nil {
		return err
	}
	c := r.newClient(tok, true, nil)
	p, err := getOne[map[string]any](ctx, c, r.projectRoot(), nil)
	if err != nil {
		return err
	}
	if p == nil {
		return fmt.Errorf("%w: %s (the glab login cannot see it, or it does not exist)", provider.ErrNotFound, r.project)
	}
	return nil
}

// CommitPage is one page of the default branch's commits, newest first. The cursor is the next page number ("" at the end).
func (r *Repo) CommitPage(ctx context.Context, cursor string) (model.CommitPage, error) {
	tok, err := r.serverToken(ctx)
	if err != nil {
		return model.CommitPage{}, err
	}
	page := 1
	if cursor != "" {
		n, err := strconv.Atoi(cursor)
		if err != nil || n < 1 {
			return model.CommitPage{}, provider.Errorf(400, "the commit cursor must be a page number, not %q", cursor)
		}
		page = n
	}
	c := r.newClient(tok, true, nil)
	res, found, err := c.get(ctx, c.apiURL(r.projectPath("repository/commits"), url.Values{
		"per_page": {strconv.Itoa(pageSize)}, "page": {strconv.Itoa(page)},
	}))
	if err != nil {
		return model.CommitPage{}, err
	}
	if !found {
		return model.CommitPage{}, nil
	}
	var rows []glCommit
	if err := json.Unmarshal(res.body, &rows); err != nil {
		return model.CommitPage{}, fmt.Errorf("reading the commits: %w", err)
	}
	out := model.CommitPage{Items: []model.Commit{}}
	for _, cm := range rows {
		out.Items = append(out.Items, model.Commit{
			Sha: cm.ID, Title: deref(firstOf(cm.Title, cm.Message)), Author: cm.AuthorName,
			At: firstOf(cm.CommittedDate, cm.CreatedAt),
		})
	}
	if next := res.header.Get("X-Next-Page"); next != "" {
		if n, err := strconv.Atoi(next); err == nil && n > page {
			out.Next = strconv.Itoa(n)
		}
	}
	return out, nil
}

// CommitFiles reads one commit: its title, author and time, and its files with their diffs (the text as GitLab gives it).
func (r *Repo) CommitFiles(ctx context.Context, sha string) (*model.CommitInfo, []model.HostFile, error) {
	if !commitSha.MatchString(sha) {
		return nil, nil, provider.Errorf(400, "a commit id must be hexadecimal, not %q", sha)
	}
	tok, err := r.serverToken(ctx)
	if err != nil {
		return nil, nil, err
	}
	c := r.newClient(tok, true, nil)
	base := r.projectPath("repository/commits/" + sha)
	commit, err := getOne[glCommit](ctx, c, base, nil)
	if err != nil {
		return nil, nil, err
	}
	diffs, err := listPages[glDiff](ctx, c, base+"/diff", nil)
	if err != nil {
		return nil, nil, err
	}
	var info *model.CommitInfo
	if commit != nil {
		info = &model.CommitInfo{Title: deref(firstOf(commit.Title, commit.Message)), Author: commit.AuthorName, At: commit.CommittedDate}
	}
	files := []model.HostFile{}
	for _, d := range diffs {
		f := model.HostFile{Path: deref(d.NewPath), Status: "modified", Patch: d.Diff, TooLarge: d.TooLarge}
		switch {
		case d.NewFile:
			f.Status = "added"
		case d.DeletedFile:
			f.Status = "deleted"
		case d.RenamedFile:
			f.Status = "renamed"
		}
		if d.RenamedFile {
			f.OldPath = d.OldPath
		}
		files = append(files, f)
	}
	return info, files, nil
}

// glDiff is one file of a commit's diff.
type glDiff struct {
	OldPath     *string `json:"old_path"`
	NewPath     *string `json:"new_path"`
	NewFile     bool    `json:"new_file"`
	DeletedFile bool    `json:"deleted_file"`
	RenamedFile bool    `json:"renamed_file"`
	Diff        *string `json:"diff"`
	TooLarge    bool    `json:"too_large"`
}

// MergeMethod is how a merged merge request landed: "squash", "merge commit" or "rebase". "" when it is not merged, or
// the host does not show it.
func (r *Repo) MergeMethod(ctx context.Context, rec *model.Record) (string, error) {
	iid, err := provider.PathID(rec.Key, "the merge request number")
	if err != nil {
		return "", err
	}
	tok, err := r.serverToken(ctx)
	if err != nil {
		return "", err
	}
	c := r.newClient(tok, true, nil)
	mr, err := getOne[glMergeRequest](ctx, c, r.projectPath("merge_requests/"+iid), nil)
	if err != nil || mr == nil || mr.State != "merged" {
		return "", err
	}
	switch {
	case mr.SquashCommitSHA != nil && *mr.SquashCommitSHA != "":
		return "squash", nil
	case mr.MergeCommitSHA != nil && *mr.MergeCommitSHA != "":
		return "merge commit", nil
	}
	return "rebase", nil
}

// CIRuns are the project's pipelines updated since `since`, newest first, categorized by the server's categorizer.
func (r *Repo) CIRuns(ctx context.Context, since time.Time) ([]model.CIRun, error) {
	tok, err := r.serverToken(ctx)
	if err != nil {
		return nil, err
	}
	c := r.newClient(tok, true, nil)
	rows, err := listPages[glPipeline](ctx, c, r.projectPath("pipelines"), url.Values{
		"updated_after": {isoMillis(since)}, "order_by": {"updated_at"}, "sort": {"desc"},
	})
	if err != nil {
		return nil, err
	}
	out := make([]model.CIRun, 0, len(rows))
	for _, p := range rows {
		out = append(out, r.pipelineRun(p))
	}
	return out, nil
}

// glPipeline is a pipeline as the list gives it.
type glPipeline struct {
	ID        int     `json:"id"`
	Status    string  `json:"status"`
	Source    string  `json:"source"`
	Ref       *string `json:"ref"`
	SHA       string  `json:"sha"`
	WebURL    string  `json:"web_url"`
	CreatedAt string  `json:"created_at"`
}

// pipelineRun is a pipeline as a CI run: the branch goes in Branch, never in Path (the categorizer reads the path of a
// workflow file, and a branch named "pages-redesign" must not look like Pages).
func (r *Repo) pipelineRun(p glPipeline) model.CIRun {
	event := p.Source
	if mapped, ok := gitlabSource[p.Source]; ok {
		event = mapped
	}
	run := model.CIRun{
		ID: strconv.Itoa(p.ID), URL: p.WebURL, Name: "pipeline", Path: "", Event: event, Actor: "",
		Branch: p.Ref, Sha: p.SHA, Status: p.Status, Conclusion: model.Ptr(p.Status), At: p.CreatedAt,
	}
	run.Category = r.categorize(run)
	return run
}

// WorkflowChanges are the commits that changed .gitlab-ci.yml since `since`, newest first.
func (r *Repo) WorkflowChanges(ctx context.Context, since time.Time) ([]model.WorkflowChange, error) {
	tok, err := r.serverToken(ctx)
	if err != nil {
		return nil, err
	}
	c := r.newClient(tok, true, nil)
	rows, err := listPages[glCommit](ctx, c, r.projectPath("repository/commits"), url.Values{
		"path": {".gitlab-ci.yml"}, "since": {isoMillis(since)},
	})
	if err != nil {
		return nil, err
	}
	out := []model.WorkflowChange{}
	for _, cm := range rows {
		out = append(out, model.WorkflowChange{
			Sha: cm.ID, Title: deref(cm.Title), Author: cm.AuthorName, At: firstOf(cm.CommittedDate, cm.CreatedAt), URL: cm.WebURL,
		})
	}
	return out, nil
}

// Identity is the username of the account a host's token belongs to, and where the token came from: an environment
// variable name, or "glab session". The host is a host name, or an origin (scheme and host) for a server on another
// port or scheme.
func Identity(ctx context.Context, host string, o Options) (login, source string, err error) {
	origin := "https://" + host
	hostName := host
	if strings.Contains(host, "://") {
		u, perr := url.Parse(host)
		if perr != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return "", "", errors.New("the origin must be http or https")
		}
		origin = u.Scheme + "://" + u.Host
		hostName = strings.ToLower(u.Host)
	}
	tokenFn := o.Token
	if tokenFn == nil {
		tokenFn = TokenSource()
	}
	tok, err := tokenFn(ctx, hostName)
	if err != nil {
		return "", "", err
	}
	hc := sameOrigin(o.HTTP)
	c := &client{http: hc, base: origin + "/api/v4", token: tok}
	u, err := getOne[glUser](ctx, c, "user", nil)
	if err != nil {
		return "", "", err
	}
	if u == nil {
		return "", "", fmt.Errorf("GitLab did not answer for /user")
	}
	return u.Username, identitySource(hostName), nil
}

// identitySource names the environment variable a host's token came from, or "glab session".
func identitySource(host string) string {
	if name := tokenEnvName(host); name != "" {
		return name
	}
	return "glab session"
}
