package github

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/provider"
)

// fullSha is a full commit sha: the only form a commit's URL takes (commits.js FULL_SHA). A sha is not a number, so
// provider.PathID does not apply to it; this check does.
var fullSha = regexp.MustCompile(`^[0-9a-f]{40}$`)

const historyQuery = `query($owner: String!, $name: String!, $cursor: String) {
  repository(owner: $owner, name: $name) {
    defaultBranchRef { target { ... on Commit {
      history(first: 100, after: $cursor) {
        pageInfo { hasNextPage endCursor }
        nodes { oid messageHeadline committedDate author { name user { login } } additions deletions }
      }
    } } }
  }
}`

// CommitPage reads one page of the default branch's history, newest first. cursor "" is the first page.
func (r *Repo) CommitPage(ctx context.Context, cursor string) (model.CommitPage, error) {
	page, err := r.commitPage(ctx, cursor)
	return page, asProvider(err)
}

func (r *Repo) commitPage(ctx context.Context, cursor string) (model.CommitPage, error) {
	tok, err := r.serverToken(ctx)
	if err != nil {
		return model.CommitPage{}, err
	}
	var cur any
	if cursor != "" {
		cur = cursor
	}
	vars := map[string]any{"owner": r.owner, "name": r.name, "cursor": cur}
	var data struct {
		Repository *struct {
			DefaultBranchRef *struct {
				Target *struct {
					History *struct {
						PageInfo struct {
							HasNextPage bool   `json:"hasNextPage"`
							EndCursor   string `json:"endCursor"`
						} `json:"pageInfo"`
						Nodes []*struct {
							Oid             string `json:"oid"`
							MessageHeadline string `json:"messageHeadline"`
							CommittedDate   string `json:"committedDate"`
							Additions       *int   `json:"additions"`
							Deletions       *int   `json:"deletions"`
							Author          *struct {
								Name string     `json:"name"`
								User *loginNode `json:"user"`
							} `json:"author"`
						} `json:"nodes"`
					} `json:"history"`
				} `json:"target"`
			} `json:"defaultBranchRef"`
		} `json:"repository"`
	}
	if err := r.api.graphqlInto(ctx, r.o.Endpoints.GraphQL, tok, historyQuery, vars, &data); err != nil {
		return model.CommitPage{}, err
	}
	if data.Repository == nil || data.Repository.DefaultBranchRef == nil || data.Repository.DefaultBranchRef.Target == nil || data.Repository.DefaultBranchRef.Target.History == nil {
		return model.CommitPage{}, nil // an empty repo has no history yet
	}
	h := data.Repository.DefaultBranchRef.Target.History
	page := model.CommitPage{Items: []model.Commit{}}
	for _, c := range h.Nodes {
		if c == nil {
			continue
		}
		var author *string
		if c.Author != nil {
			switch {
			case c.Author.User != nil:
				login := c.Author.User.Login
				author = &login
			case c.Author.Name != "":
				name := c.Author.Name
				author = &name
			}
		}
		at := c.CommittedDate
		page.Items = append(page.Items, model.Commit{Sha: c.Oid, Title: c.MessageHeadline, Author: author, At: &at, Additions: c.Additions, Deletions: c.Deletions})
	}
	if h.PageInfo.HasNextPage {
		page.Next = h.PageInfo.EndCursor
	}
	return page, nil
}

// githubStatus maps GitHub's file status to the store's.
var githubStatus = map[string]string{"added": "added", "removed": "deleted", "modified": "modified", "renamed": "renamed", "copied": "added", "changed": "modified"}

// restCommit is one commit of the REST API, with its files.
type restCommit struct {
	Commit *struct {
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
	Files  []struct {
		Filename         string  `json:"filename"`
		PreviousFilename *string `json:"previous_filename"`
		Status           string  `json:"status"`
		Additions        int     `json:"additions"`
		Deletions        int     `json:"deletions"`
		Patch            *string `json:"patch"`
	} `json:"files"`
}

// CommitFiles reads the info (title, author, date) and the files of one commit, as the host gives them.
func (r *Repo) CommitFiles(ctx context.Context, sha string) (*model.CommitInfo, []model.HostFile, error) {
	info, files, err := r.commitFiles(ctx, sha)
	return info, files, asProvider(err)
}

func (r *Repo) commitFiles(ctx context.Context, sha string) (*model.CommitInfo, []model.HostFile, error) {
	if !fullSha.MatchString(sha) {
		return nil, nil, provider.Errorf(400, "not a full commit sha: %q", sha)
	}
	tok, err := r.serverToken(ctx)
	if err != nil {
		return nil, nil, err
	}
	var c restCommit
	if err := r.api.rest(ctx, "GET", r.repoURL()+"/commits/"+sha, tok, nil, &c); err != nil {
		return nil, nil, err
	}
	info := &model.CommitInfo{}
	if c.Commit != nil {
		info.Title = firstLine(c.Commit.Message)
		if c.Commit.Committer != nil && c.Commit.Committer.Date != "" {
			d := c.Commit.Committer.Date
			info.At = &d
		} else if c.Commit.Author != nil && c.Commit.Author.Date != "" {
			d := c.Commit.Author.Date
			info.At = &d
		}
	}
	switch {
	case c.Author != nil:
		login := c.Author.Login
		info.Author = &login
	case c.Commit != nil && c.Commit.Author != nil:
		name := c.Commit.Author.Name
		info.Author = &name
	}
	out := make([]model.HostFile, 0, len(c.Files))
	for _, f := range c.Files {
		status := githubStatus[f.Status]
		if status == "" {
			status = "modified"
		}
		out = append(out, model.HostFile{
			Path: f.Filename, OldPath: f.PreviousFilename, Status: status,
			Additions: model.Ptr(f.Additions), Deletions: model.Ptr(f.Deletions), Patch: f.Patch,
		})
	}
	return info, out, nil
}

// firstLine is the first line of a message (the title of a commit).
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// MergeMethod is how a merged pull landed: "squash", "merge commit" or "rebase"; "" while it has no merge commit.
func (r *Repo) MergeMethod(ctx context.Context, rec *model.Record) (string, error) {
	m, err := r.mergeMethod(ctx, rec)
	return m, asProvider(err)
}

func (r *Repo) mergeMethod(ctx context.Context, rec *model.Record) (string, error) {
	number, err := recordNumber(rec)
	if err != nil {
		return "", err
	}
	if r.o.ClassifyMerge == nil {
		return "", fmt.Errorf("no merge classifier for %s", r.Key())
	}
	tok, err := r.serverToken(ctx)
	if err != nil {
		return "", err
	}
	var pr restPull
	if err := r.api.rest(ctx, "GET", r.repoURL()+"/pulls/"+strconv.Itoa(number), tok, nil, &pr); err != nil {
		return "", err
	}
	if pr.MergeCommitSHA == nil || *pr.MergeCommitSHA == "" {
		return "", nil
	}
	sha := *pr.MergeCommitSHA
	if !fullSha.MatchString(sha) {
		return "", fmt.Errorf("GitHub gave a merge commit that is not a sha: %q", sha)
	}
	var commit struct {
		Commit *struct {
			Message string `json:"message"`
		} `json:"commit"`
		Parents []json.RawMessage `json:"parents"`
	}
	if err := r.api.rest(ctx, "GET", r.repoURL()+"/commits/"+sha, tok, nil, &commit); err != nil {
		return "", err
	}
	title := ""
	if commit.Commit != nil {
		title = firstLine(commit.Commit.Message)
	}
	var prTitles []string
	for _, c := range rec.Commits {
		prTitles = append(prTitles, c.Message)
	}
	return r.o.ClassifyMerge(len(commit.Parents), title, rec.Title, number, prTitles), nil
}

// recordNumber is the record's number: its Number, or its key read as one.
func recordNumber(rec *model.Record) (int, error) {
	if rec.Number != nil {
		return *rec.Number, nil
	}
	if _, err := provider.PathID(rec.Key, "the pull request number"); err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(rec.Key)
	if err != nil {
		return 0, err
	}
	return n, nil
}
