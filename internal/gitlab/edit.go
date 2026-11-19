package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/provider"
)

// issueEdit is an issue change as the API received it, decoded: the PUT body, and the calls that are their own requests.
type issueEdit struct {
	body         map[string]any
	weight       *float64
	weightSet    bool
	timeEstimate *string
	addSpent     string
	comment      string
	local        localChange
}

// parseIssueEdit decodes the change's names (the store's names, as the edit form sends them).
func parseIssueEdit(ch provider.Changes) (issueEdit, error) {
	e := issueEdit{body: map[string]any{}}
	var s string
	if ch.Get("title", &s) {
		e.body["title"] = s
	}
	if ch.Get("body", &s) {
		e.body["description"] = s
	}
	if ch.Has("state") {
		var state string
		ch.Get("state", &state)
		event := "reopen"
		if state == "CLOSED" {
			event = "close"
		}
		e.body["state_event"] = event
	}
	if ch.Has("labels") {
		var labels []string
		ch.Get("labels", &labels)
		e.body["labels"] = strings.Join(labels, ",")
	}
	if ch.Has("assignees") {
		e.body["assignee_ids"] = ch["assignees"]
	}
	if ch.Has("milestone") {
		if ch.IsNull("milestone") {
			e.body["milestone_id"] = 0 // 0 unassigns the milestone
		} else {
			e.body["milestone_id"] = ch["milestone"]
		}
	}
	if ch.Has("dueDate") {
		var due string
		ch.Get("dueDate", &due)
		e.body["due_date"] = due
	}
	if ch.Has("confidential") {
		var conf bool
		ch.Get("confidential", &conf)
		e.body["confidential"] = conf
	}
	if ch.Has("weight") {
		e.weightSet = true
		if ch.IsNull("weight") {
			e.body["weight"] = ""
		} else {
			var w float64
			if err := json.Unmarshal(ch["weight"], &w); err != nil {
				return e, provider.Errorf(422, "weight must be a number")
			}
			e.weight = &w
			e.body["weight"] = ch["weight"]
		}
	}
	// time: an estimate is set (or reset with "0h"), and time spent is added; GitLab reads durations such as "1h30m"
	if ch.Has("timeEstimate") {
		var d string
		ch.Get("timeEstimate", &d)
		if d == "" {
			d = "0h"
		}
		e.timeEstimate = &d
	}
	if ch.Get("addSpent", &s) {
		e.addSpent = s
	}
	if ch.Get("comment", &s) {
		e.comment = s
	}
	lc, err := localOf(ch)
	if err != nil {
		return e, err
	}
	e.local = lc
	return e, nil
}

// EditIssue changes an issue on GitLab, as the edit form sends it: the issue's fields, its time, its notes, its links
// and its parent, each a request of its own, in that order. Then it reads the issue back. The record it returns is old
// with what the host now says and with the links, the parent and the notes applied to itself; ApplyLocally does the
// other issues. Warnings name values the host did not keep.
func (r *Repo) EditIssue(ctx context.Context, cred provider.Cred, old *model.Record, ch provider.Changes) (*model.Record, []string, error) {
	if old == nil {
		return nil, nil, errors.New("no record to edit")
	}
	iid, err := provider.PathID(old.Key, "the issue number")
	if err != nil {
		return nil, nil, err
	}
	e, err := parseIssueEdit(ch)
	if err != nil {
		return nil, nil, err
	}
	// every id that goes in a path is checked before the first change is sent
	if err := validateLocal(e.local); err != nil {
		return nil, nil, err
	}
	tok, err := r.credToken(ctx, cred)
	if err != nil {
		return nil, nil, err
	}
	c := r.newClient(tok, cred.Token == "", nil)
	base := r.projectPath("issues/" + iid)
	if len(e.body) > 0 {
		if _, err := c.call(ctx, http.MethodPut, c.apiURL(base, nil), e.body); err != nil {
			return nil, nil, err
		}
	}
	if e.timeEstimate != nil {
		if _, err := c.call(ctx, http.MethodPost, c.apiURL(base+"/time_estimate", nil), map[string]any{"duration": *e.timeEstimate}); err != nil {
			return nil, nil, err
		}
	}
	if e.addSpent != "" {
		if _, err := c.call(ctx, http.MethodPost, c.apiURL(base+"/add_spent_time", nil), map[string]any{"duration": e.addSpent}); err != nil {
			return nil, nil, err
		}
	}
	for _, n := range e.local.editNotes {
		if _, err := c.call(ctx, http.MethodPut, c.apiURL(base+"/notes/"+n.ID, nil), map[string]any{"body": n.Body}); err != nil {
			return nil, nil, err
		}
	}
	for _, id := range e.local.deleteNotes {
		if _, err := c.call(ctx, http.MethodDelete, c.apiURL(base+"/notes/"+id, nil), nil); err != nil {
			return nil, nil, err
		}
	}
	// GitLab answers with the note it created: that confirmation is what the record keeps (no second read)
	var posted *model.Comment
	if e.comment != "" {
		res, err := c.call(ctx, http.MethodPost, c.apiURL(base+"/notes", nil), map[string]any{"body": e.comment})
		if err != nil {
			return nil, nil, err
		}
		posted = noteComment(res.body)
	}
	if len(e.local.addLinks)+len(e.local.removeLinks) > 0 {
		if err := r.applyLinks(ctx, c, base, e.local); err != nil {
			return nil, nil, err
		}
	}
	if e.local.parentSet {
		if err := r.setParent(ctx, c, iid, e.local.parent); err != nil {
			return nil, nil, err
		}
	}
	res, err := c.call(ctx, http.MethodGet, c.apiURL(base, nil), nil)
	if err != nil {
		return nil, nil, err
	}
	var issue glIssue
	if err := json.Unmarshal(res.body, &issue); err != nil {
		return nil, nil, fmt.Errorf("reading the issue back: %w", err)
	}
	// A value the host did not keep is reported, not hidden: weights need a paid plan on GitLab, and it ignores them quietly.
	var warnings []string
	if e.weightSet && !sameFloatPtr(issue.Weight, e.weight) {
		warnings = append(warnings, "GitLab did not keep the weight (weights need a paid plan)")
	}
	rec := *old
	rec.Title = issue.Title
	rec.Body = deref(issue.Description)
	rec.State = readState(issue.State)
	rec.ClosedAt = issue.ClosedAt
	rec.DueDate = issue.DueDate
	rec.Confidential = model.Ptr(issue.Confidential)
	rec.Weight = issue.Weight
	rec.Labels = keepColors(labelNames(issue.Labels), old.Labels)
	rec.Assignees = userNames(issue.Assignees)
	rec.Milestone = milestoneOf(issue.Milestone)
	rec.UpdatedAt = issue.UpdatedAt
	applySelf(&rec, e.local)
	if posted != nil {
		model.AddComment(&rec, *posted)
	}
	return &rec, warnings, nil
}

func sameFloatPtr(a, b *float64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// applyLinks adds the links of a change and removes the ones it names (found by their type and the other issue's iid).
func (r *Repo) applyLinks(ctx context.Context, c *client, base string, lc localChange) error {
	if len(lc.addLinks) > 0 {
		res, err := c.call(ctx, http.MethodGet, c.apiURL(r.projectRoot(), nil), nil)
		if err != nil {
			return err
		}
		var project struct {
			ID int `json:"id"`
		}
		if err := json.Unmarshal(res.body, &project); err != nil {
			return fmt.Errorf("reading the project: %w", err)
		}
		for _, l := range lc.addLinks {
			n, err := strconv.Atoi(l.To)
			if err != nil {
				return provider.Errorf(422, "the other issue must be a number, not %q", l.To)
			}
			body := map[string]any{"target_project_id": project.ID, "target_issue_iid": n, "link_type": linkTypes[l.Type]}
			if _, err := c.call(ctx, http.MethodPost, c.apiURL(base+"/links", nil), body); err != nil {
				return err
			}
		}
	}
	if len(lc.removeLinks) > 0 {
		current, err := r.listLinks(ctx, c, base)
		if err != nil {
			return err
		}
		for _, want := range lc.removeLinks {
			for _, have := range current {
				if have.to != want.To || have.typ != want.Type {
					continue
				}
				id, err := provider.PathID(strconv.Itoa(have.linkID), "a link id")
				if err != nil {
					return err
				}
				if _, err := c.call(ctx, http.MethodDelete, c.apiURL(base+"/links/"+id, nil), nil); err != nil {
					return err
				}
				break
			}
		}
	}
	return nil
}

// linkRow is a link of an issue as this package names it.
type linkRow struct {
	typ    string // blocks | blockedBy | relates
	to     string
	linkID int
}

// listLinks reads the links of an issue (base is its path).
func (r *Repo) listLinks(ctx context.Context, c *client, base string) ([]linkRow, error) {
	res, err := c.call(ctx, http.MethodGet, c.apiURL(base+"/links", nil), nil)
	if err != nil {
		return nil, err
	}
	var rows []glLink
	if err := json.Unmarshal(res.body, &rows); err != nil {
		return nil, fmt.Errorf("reading the links: %w", err)
	}
	out := make([]linkRow, 0, len(rows))
	for _, l := range rows {
		row := linkRow{to: strconv.Itoa(l.IID), linkID: l.LinkID, typ: "relates"}
		switch l.LinkType {
		case "blocks":
			row.typ = "blocks"
		case "is_blocked_by":
			row.typ = "blockedBy"
		}
		out = append(out, row)
	}
	return out, nil
}

// workItemID is the GraphQL id of an issue's work item, which the hierarchy is set through.
func (r *Repo) workItemID(ctx context.Context, c *client, iid string) (string, error) {
	const q = "query($fp: ID!, $iid: String!) { project(fullPath: $fp) { workItems(iid: $iid) { nodes { id } } } }"
	data, err := r.graphQL(ctx, c, q, map[string]any{"fp": r.project, "iid": iid})
	if err != nil {
		return "", err
	}
	var body struct {
		Project *struct {
			WorkItems *struct {
				Nodes []struct {
					ID string `json:"id"`
				} `json:"nodes"`
			} `json:"workItems"`
		} `json:"project"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return "", fmt.Errorf("reading the work item: %w", err)
	}
	if body.Project == nil || body.Project.WorkItems == nil || len(body.Project.WorkItems.Nodes) == 0 {
		return "", provider.Errorf(502, "GitLab has no work item for !%s", iid)
	}
	return body.Project.WorkItems.Nodes[0].ID, nil
}

// setParent sets the parent of an issue through the work item's hierarchy, or removes it (parent nil).
func (r *Repo) setParent(ctx context.Context, c *client, iid string, parent *string) error {
	id, err := r.workItemID(ctx, c, iid)
	if err != nil {
		return err
	}
	var parentID any
	if parent != nil {
		pid, err := r.workItemID(ctx, c, *parent)
		if err != nil {
			return err
		}
		parentID = pid
	}
	const m = "mutation($input: WorkItemUpdateInput!) { workItemUpdate(input: $input) { errors workItem { id } } }"
	data, err := r.graphQL(ctx, c, m, map[string]any{"input": map[string]any{"id": id, "hierarchyWidget": map[string]any{"parentId": parentID}}})
	if err != nil {
		return err
	}
	var out struct {
		WorkItemUpdate *struct {
			Errors []string `json:"errors"`
		} `json:"workItemUpdate"`
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &out); err != nil {
			return fmt.Errorf("reading the parent change: %w", err)
		}
	}
	if out.WorkItemUpdate != nil && len(out.WorkItemUpdate.Errors) > 0 {
		return provider.Errorf(422, "GitLab refused the parent: %s", strings.Join(out.WorkItemUpdate.Errors, "; "))
	}
	return nil
}

// CreateIssue creates an issue with the form's fields and returns it as the store keeps it.
func (r *Repo) CreateIssue(ctx context.Context, cred provider.Cred, ch provider.Changes) (*model.Record, error) {
	var title, description string
	ch.Get("title", &title)
	ch.Get("body", &description)
	body := map[string]any{"title": title, "description": description}
	var labels []string
	if ch.Get("labels", &labels) && len(labels) > 0 {
		body["labels"] = strings.Join(labels, ",")
	}
	var assignees []json.RawMessage
	if ch.Get("assignees", &assignees) && len(assignees) > 0 {
		body["assignee_ids"] = assignees
	}
	if ch.Has("milestone") && !ch.IsNull("milestone") {
		body["milestone_id"] = ch["milestone"]
	}
	var due string
	if ch.Get("dueDate", &due) && due != "" {
		body["due_date"] = due
	}
	tok, err := r.credToken(ctx, cred)
	if err != nil {
		return nil, err
	}
	c := r.newClient(tok, cred.Token == "", nil)
	res, err := c.call(ctx, http.MethodPost, c.apiURL(r.projectPath("issues"), nil), body)
	if err != nil {
		return nil, err
	}
	var i glIssue
	if err := json.Unmarshal(res.body, &i); err != nil {
		return nil, fmt.Errorf("reading the new issue: %w", err)
	}
	return &model.Record{
		Kind: "issue", ID: strconv.Itoa(i.ID), Key: strconv.Itoa(i.IID), Number: model.Ptr(i.IID),
		Title: i.Title, Body: deref(i.Description), URL: i.WebURL, State: readState(i.State),
		CreatedAt: i.CreatedAt, UpdatedAt: i.UpdatedAt, ClosedAt: i.ClosedAt, DueDate: i.DueDate,
		Labels: normalizeLabels(i.Labels, nil), Assignees: userNames(i.Assignees), Milestone: milestoneOf(i.Milestone),
		Author: userName(i.Author), CommentsCount: 0, Comments: []model.Comment{}, Events: []model.Event{},
		Confidential: model.Ptr(i.Confidential),
	}, nil
}

// EditOptions are the lists the edit form offers: the project's labels, its active milestones, and its members. A list
// the host does not give is empty.
func (r *Repo) EditOptions(ctx context.Context, cred provider.Cred) (provider.EditOptions, error) {
	tok, err := r.credToken(ctx, cred)
	if err != nil {
		return provider.EditOptions{}, err
	}
	c := r.newClient(tok, cred.Token == "", nil)
	var labels []glLabel
	var milestones []glMilestoneRow
	var members []glUser
	_ = runAll(ctx,
		func(ctx context.Context) (err error) {
			labels, err = listPages[glLabel](ctx, c, r.projectPath("labels"), nil)
			if err != nil {
				labels = nil
			}
			return nil
		},
		func(ctx context.Context) (err error) {
			milestones, err = listPages[glMilestoneRow](ctx, c, r.projectPath("milestones"), url.Values{"state": {"active"}})
			if err != nil {
				milestones = nil
			}
			return nil
		},
		func(ctx context.Context) (err error) {
			members, err = listPages[glUser](ctx, c, r.projectPath("members/all"), nil)
			if err != nil {
				members = nil
			}
			return nil
		},
	)
	out := provider.EditOptions{
		Trackers: []map[string]any{}, Priorities: []map[string]any{}, Members: []map[string]any{}, Versions: []map[string]any{},
		Categories: []map[string]any{}, Activities: []map[string]any{}, CustomFields: []map[string]any{}, RelationTypes: []string{},
		Labels: []map[string]any{}, Milestones: []map[string]any{}, IssueTypes: []map[string]any{},
	}
	for _, l := range labels {
		out.Labels = append(out.Labels, map[string]any{"name": l.Name, "color": "#" + stripHash(deref(l.Color))})
	}
	for _, m := range milestones {
		out.Milestones = append(out.Milestones, map[string]any{"id": m.ID, "name": m.Title})
	}
	for _, m := range members {
		out.Members = append(out.Members, map[string]any{"id": m.ID, "name": m.Username})
	}
	return out, nil
}

// glMilestoneRow is a milestone of the milestone list.
type glMilestoneRow struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
}

var draftPrefix = regexp.MustCompile(`(?i)^(draft|wip):\s*`)

// EditPull changes a merge request by its iid. A draft is a title that starts with "Draft:" (GitLab's own convention).
// It returns the record as the host now says, merged onto old.
func (r *Repo) EditPull(ctx context.Context, cred provider.Cred, old *model.Record, ch provider.Changes) (*model.Record, error) {
	if old == nil {
		return nil, errors.New("no record to edit")
	}
	iid, err := provider.PathID(old.Key, "the merge request number")
	if err != nil {
		return nil, err
	}
	body := map[string]any{}
	var s string
	var title *string
	if ch.Get("title", &s) {
		title = &s
	}
	if ch.Get("body", &s) {
		body["description"] = s
	}
	if ch.Has("state") {
		var state string
		ch.Get("state", &state)
		event := "reopen"
		if state == "CLOSED" {
			event = "close"
		}
		body["state_event"] = event
	}
	var labels []string
	if ch.Get("labels", &labels) {
		body["labels"] = strings.Join(labels, ",")
	}
	if ch.Has("assignees") {
		body["assignee_ids"] = ch["assignees"]
	}
	if ch.Has("reviewers") {
		body["reviewer_ids"] = ch["reviewers"]
	}
	if ch.Has("milestone") {
		if ch.IsNull("milestone") {
			body["milestone_id"] = 0
		} else {
			body["milestone_id"] = ch["milestone"]
		}
	}
	tok, err := r.credToken(ctx, cred)
	if err != nil {
		return nil, err
	}
	c := r.newClient(tok, cred.Token == "", nil)
	base := r.projectPath("merge_requests/" + iid)
	var wantDraft bool
	if ch.Has("isDraft") && ch.Get("isDraft", &wantDraft) {
		current := ""
		if title != nil {
			current = *title
		} else {
			m, err := getOne[glMergeRequest](ctx, c, base, nil)
			if err != nil {
				return nil, err
			}
			if m != nil {
				current = m.Title
			}
		}
		plain := draftPrefix.ReplaceAllString(current, "")
		next := plain
		if wantDraft {
			next = "Draft: " + plain
		}
		title = &next
	}
	if title != nil {
		body["title"] = *title
	}
	if len(body) > 0 {
		if _, err := c.call(ctx, http.MethodPut, c.apiURL(base, nil), body); err != nil {
			return nil, err
		}
	}
	var comment string
	var posted *model.Comment
	if ch.Get("comment", &comment) && comment != "" {
		res, err := c.call(ctx, http.MethodPost, c.apiURL(base+"/notes", nil), map[string]any{"body": comment})
		if err != nil {
			return nil, err
		}
		posted = noteComment(res.body)
	}
	res, err := c.call(ctx, http.MethodGet, c.apiURL(base, nil), nil)
	if err != nil {
		return nil, err
	}
	var m glMergeRequest
	if err := json.Unmarshal(res.body, &m); err != nil {
		return nil, fmt.Errorf("reading the merge request back: %w", err)
	}
	out := *old
	out.Title = m.Title
	out.Body = deref(m.Description)
	out.State = readState(m.State)
	out.MergedAt = m.MergedAt
	out.IsDraft = model.Ptr(draftNow(&m))
	out.Labels = keepColors(labelNames(m.Labels), old.Labels)
	out.Assignees = userNames(m.Assignees)
	out.Reviewers = userNames(m.Reviewers)
	out.Milestone = milestoneOf(m.Milestone)
	out.UpdatedAt = m.UpdatedAt
	if posted != nil {
		model.AddComment(&out, *posted)
	}
	return &out, nil
}

// draftNow is a merge request's draft flag as an edit's read-back gives it: draft or work in progress.
func draftNow(m *glMergeRequest) bool {
	return (m.Draft != nil && *m.Draft) || (m.WorkInProgress != nil && *m.WorkInProgress)
}

// noteComment is the note GitLab answered a POST of one with, in the store's shape (as the sync keeps notes); nil
// when the answer cannot be read (the note was made: the next sync brings it).
func noteComment(body []byte) *model.Comment {
	var n struct {
		ID        int64  `json:"id"`
		Body      string `json:"body"`
		CreatedAt string `json:"created_at"`
		Internal  bool   `json:"internal"`
		Author    *struct {
			Username string `json:"username"`
		} `json:"author"`
	}
	if json.Unmarshal(body, &n) != nil || n.ID == 0 {
		return nil
	}
	var author *string
	if n.Author != nil && n.Author.Username != "" {
		author = model.Ptr(n.Author.Username)
	}
	return &model.Comment{ID: strconv.FormatInt(n.ID, 10), Author: author, CreatedAt: n.CreatedAt, Body: n.Body, Private: n.Internal}
}
