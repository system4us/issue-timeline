package github

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/provider"
)

// Edits go to GitHub's REST API first. The record the store keeps is read back from the host, so it shows what GitHub
// kept. Names are the store's: title, body, state ("OPEN" | "CLOSED"), labels (names), assignees (logins), milestone
// (the milestone number, or null), stateReason, type, locked, editComments, deleteComments, comment, addLinks,
// removeLinks, parent.

// EditIssue changes an issue and returns the record the store should now keep, plus warnings about values GitHub did not
// keep. The links and the notes the change made are applied to the returned record (see LocalLinks for the other side).
func (r *Repo) EditIssue(ctx context.Context, cred provider.Cred, old *model.Record, ch provider.Changes) (*model.Record, []string, error) {
	rec, warnings, err := r.editIssue(ctx, cred, old, ch)
	return rec, warnings, asProvider(err)
}

func (r *Repo) editIssue(ctx context.Context, cred provider.Cred, old *model.Record, ch provider.Changes) (*model.Record, []string, error) {
	number, err := provider.PathID(old.Key, "the issue number")
	if err != nil {
		return nil, nil, err
	}
	tok := cred.Token
	base := r.repoURL() + "/issues/" + number
	if err := r.patchIssue(ctx, tok, base, ch); err != nil {
		return nil, nil, err
	}
	if err := r.setLock(ctx, tok, base, ch); err != nil {
		return nil, nil, err
	}
	if err := r.editNotes(ctx, tok, base, ch); err != nil {
		return nil, nil, err
	}
	// GitHub answers with the comment it created: that confirmation is what the record keeps (no second read)
	var comment string
	var posted *restComment
	if ch.Get("comment", &comment) && comment != "" {
		posted = &restComment{}
		if err := r.api.rest(ctx, "POST", base+"/comments", tok, map[string]any{"body": comment}, posted); err != nil {
			return nil, nil, err
		}
	}
	var issue restIssue
	if err := r.api.rest(ctx, "GET", base, tok, nil, &issue); err != nil {
		return nil, nil, err
	}
	// A value GitHub did not keep is reported: an issue type needs an organization that has them.
	warnings := []string{}
	if ch.Has("type") {
		var want *string
		ch.Get("type", &want)
		if !sameText(issueTypeName(issue), want) {
			warnings = append(warnings, "GitHub did not keep the issue type (issue types need an organization that has them)")
		}
	}
	add, remove, parent := linksOf(ch)
	if len(add) > 0 || len(remove) > 0 || parent.Set {
		if err := r.applyLinks(ctx, tok, number, add, remove, parent); err != nil {
			return nil, nil, err
		}
	}
	merged := *old
	if local := LocalLinks(map[string]*model.Record{old.Key: old}, old.Key, ch)[old.Key]; local != nil {
		merged = *local
	}
	applyIssueState(&merged, issueStateOf(issue))
	if posted != nil {
		model.AddComment(&merged, posted.comment())
	}
	return &merged, warnings, nil
}

// patchIssue sends the fields the change carries in one PATCH. The values go as the change gave them; a state becomes
// GitHub's "open" or "closed", and a state reason goes with a closing.
func (r *Repo) patchIssue(ctx context.Context, tok, base string, ch provider.Changes) error {
	patch := map[string]json.RawMessage{}
	for _, k := range []string{"title", "body", "labels", "assignees", "milestone", "type"} {
		if raw, ok := ch[k]; ok {
			patch[k] = raw
		}
	}
	if ch.Has("state") {
		state := "open"
		if closing(ch) {
			state = "closed"
		}
		b, _ := json.Marshal(state)
		patch["state"] = b
	}
	if raw, ok := ch["stateReason"]; ok && closing(ch) {
		// REST takes the reason in lower case ("not_planned"); the page may send it as the store keeps it
		var reason *string
		if json.Unmarshal(raw, &reason) == nil && reason != nil {
			b, _ := json.Marshal(strings.ToLower(*reason))
			patch["state_reason"] = b
		} else {
			patch["state_reason"] = raw
		}
	}
	if len(patch) == 0 {
		return nil
	}
	return r.api.rest(ctx, "PATCH", base, tok, patch, nil)
}

// closing reports whether the change closes the issue.
func closing(ch provider.Changes) bool {
	var s string
	return ch.Get("state", &s) && s == "CLOSED"
}

// setLock locks or unlocks the conversation.
func (r *Repo) setLock(ctx context.Context, tok, base string, ch provider.Changes) error {
	if !ch.Has("locked") {
		return nil
	}
	var locked bool
	ch.Get("locked", &locked)
	if locked {
		return r.api.rest(ctx, "PUT", base+"/lock", tok, map[string]any{}, nil)
	}
	return r.api.rest(ctx, "DELETE", base+"/lock", tok, nil, nil)
}

// editNotes edits and deletes notes. The store keeps GraphQL ids; GitHub's REST ids are found through the issue's comments.
func (r *Repo) editNotes(ctx context.Context, tok, base string, ch provider.Changes) error {
	var edits []struct {
		ID   json.RawMessage `json:"id"`
		Body string          `json:"body"`
	}
	var deletes []json.RawMessage
	ch.Get("editComments", &edits)
	ch.Get("deleteComments", &deletes)
	if len(edits) == 0 && len(deletes) == 0 {
		return nil
	}
	restID, err := r.commentIDs(ctx, tok, base)
	if err != nil {
		return err
	}
	for _, c := range edits {
		id, err := restID(scalarText(c.ID))
		if err != nil {
			return err
		}
		if err := r.api.rest(ctx, "PATCH", r.commentURL(id), tok, map[string]any{"body": c.Body}, nil); err != nil {
			return err
		}
	}
	for _, raw := range deletes {
		id, err := restID(scalarText(raw))
		if err != nil {
			return err
		}
		if err := r.api.rest(ctx, "DELETE", r.commentURL(id), tok, nil, nil); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repo) commentURL(id string) string {
	return r.repoURL() + "/issues/comments/" + id
}

// commentIDs maps a note's GraphQL id to its REST id, from the issue's comments.
func (r *Repo) commentIDs(ctx context.Context, tok, base string) (func(string) (string, error), error) {
	list, err := restAll[restComment](ctx, r.api, base+"/comments?per_page=100", tok, 50)
	if err != nil {
		return nil, err
	}
	byNode := map[string]int64{}
	for _, c := range list {
		byNode[c.NodeID] = c.ID
	}
	return func(node string) (string, error) {
		id, ok := byNode[node]
		if !ok {
			return "", fmt.Errorf("that note is not on the issue on GitHub any more")
		}
		return provider.PathID(strconv.FormatInt(id, 10), "a note id")
	}, nil
}

// linkChange is one relation a change asks for: its type ("blocks", "blockedBy", "relates") and the other issue's number.
type linkChange struct {
	Type string          `json:"type"`
	To   json.RawMessage `json:"to"`
}

// parentChange is the parent a change sets: Set is false when the change has none; Null removes it.
type parentChange struct {
	Set  bool
	Null bool
	To   string
}

// linksOf reads the relation and parent parts of a change.
func linksOf(ch provider.Changes) ([]linkChange, []linkChange, parentChange) {
	var add, remove []linkChange
	ch.Get("addLinks", &add)
	ch.Get("removeLinks", &remove)
	var parent parentChange
	if ch.Has("parent") {
		parent.Set = true
		if ch.IsNull("parent") {
			parent.Null = true
		} else {
			parent.To = scalarText(ch["parent"])
		}
	}
	return add, remove, parent
}

// applyLinks sets the blocked-by relations and the sub-issue parent of an issue, by the issues' numeric ids. Each is its
// own request, after the change itself.
func (r *Repo) applyLinks(ctx context.Context, tok, number string, add, remove []linkChange, parent parentChange) error {
	idOf := func(n string) (int64, error) {
		var i struct {
			ID int64 `json:"id"`
		}
		if err := r.api.rest(ctx, "GET", r.repoURL()+"/issues/"+n, tok, nil, &i); err != nil {
			return 0, err
		}
		return i.ID, nil
	}
	change := func(l linkChange, drop bool) error {
		if l.Type == "relates" {
			return fmt.Errorf("GitHub has no native 'relates to' between issues: use blocks, blocked by, or parent")
		}
		if l.Type != "blocks" && l.Type != "blockedBy" {
			return fmt.Errorf("unknown link type %s", l.Type)
		}
		other, err := provider.PathID(scalarText(l.To), "the other issue")
		if err != nil {
			return err
		}
		// "A blocks B" is "B is blocked by A": the relation lives on the blocked issue, with the blocker's id
		blocked, blocker := other, number
		if l.Type == "blockedBy" {
			blocked, blocker = number, other
		}
		blockedPath := r.repoURL() + "/issues/" + blocked + "/dependencies/blocked_by"
		bid, err := idOf(blocker)
		if err != nil {
			return err
		}
		if drop {
			return r.api.rest(ctx, "DELETE", blockedPath+"/"+strconv.FormatInt(bid, 10), tok, nil, nil)
		}
		return r.api.rest(ctx, "POST", blockedPath, tok, map[string]any{"issue_id": bid}, nil)
	}
	for _, l := range add {
		if err := change(l, false); err != nil {
			return err
		}
	}
	for _, l := range remove {
		if err := change(l, true); err != nil {
			return err
		}
	}
	if !parent.Set {
		return nil
	}
	var current *struct {
		Number int `json:"number"`
	}
	if err := r.api.rest(ctx, "GET", r.repoURL()+"/issues/"+number+"/parent", tok, nil, &current); err != nil {
		var he *hostError
		if !asHostError(err, &he) || he.Status != 404 {
			return err
		}
		current = nil // no parent: a 404 is the answer
	}
	sid, err := idOf(number)
	if err != nil {
		return err
	}
	if current != nil {
		pn, err := provider.PathID(strconv.Itoa(current.Number), "the parent")
		if err != nil {
			return err
		}
		body := map[string]any{"sub_issue_id": sid}
		if err := r.api.rest(ctx, "DELETE", r.repoURL()+"/issues/"+pn+"/sub_issue", tok, body, nil); err != nil {
			return err
		}
	}
	if !parent.Null {
		pn, err := provider.PathID(parent.To, "the parent")
		if err != nil {
			return err
		}
		return r.api.rest(ctx, "POST", r.repoURL()+"/issues/"+pn+"/sub_issues", tok, map[string]any{"sub_issue_id": sid}, nil)
	}
	return nil
}

// CreateIssue creates an issue with the form's fields (title, body, labels, assignees, milestone).
func (r *Repo) CreateIssue(ctx context.Context, cred provider.Cred, ch provider.Changes) (*model.Record, error) {
	rec, err := r.createIssue(ctx, cred, ch)
	return rec, asProvider(err)
}

func (r *Repo) createIssue(ctx context.Context, cred provider.Cred, ch provider.Changes) (*model.Record, error) {
	var title string
	if !ch.Get("title", &title) || title == "" {
		return nil, provider.Errorf(400, "a title is needed")
	}
	body := map[string]any{"title": title, "body": ""}
	var text string
	if ch.Get("body", &text) {
		body["body"] = text
	}
	var labels, assignees []string
	ch.Get("labels", &labels)
	ch.Get("assignees", &assignees)
	if len(labels) > 0 {
		body["labels"] = labels
	}
	if len(assignees) > 0 {
		body["assignees"] = assignees
	}
	var milestone *int
	if ch.Get("milestone", &milestone) && milestone != nil {
		body["milestone"] = *milestone
	}
	var issue restIssue
	if err := r.api.rest(ctx, "POST", r.repoURL()+"/issues", cred.Token, body, &issue); err != nil {
		return nil, err
	}
	return newIssueRecord(issue), nil
}

// newIssueRecord is an issue GitHub has just created, as the store keeps it.
func newIssueRecord(i restIssue) *model.Record {
	id := strconv.FormatInt(i.ID, 10)
	if i.NodeID != "" {
		id = i.NodeID
	}
	st := issueStateOf(i)
	r := &model.Record{
		Kind: "issue", ID: id, Key: strconv.Itoa(i.Number), Number: model.Ptr(i.Number), Title: st.Title, Body: st.Body, URL: i.HTMLURL,
		State: st.State, CreatedAt: i.CreatedAt, UpdatedAt: i.UpdatedAt, ClosedAt: st.ClosedAt,
		Labels: st.Labels, Assignees: st.Assignees, Milestone: st.Milestone, Author: nil,
		Comments: []model.Comment{}, Events: []model.Event{}, BlockedBy: []model.Ref{}, Blocking: []model.Ref{}, SubIssues: []model.Ref{},
	}
	if i.User != nil {
		login := i.User.Login
		r.Author = &login
	}
	return r
}

// EditPull changes a pull request. Labels, assignees and the milestone go through its issue (a pull request is one).
// Reviewers are set to the list given: only the difference is asked or removed. A merged one is not editable here.
func (r *Repo) EditPull(ctx context.Context, cred provider.Cred, old *model.Record, ch provider.Changes) (*model.Record, error) {
	rec, err := r.editPull(ctx, cred, old, ch)
	return rec, asProvider(err)
}

func (r *Repo) editPull(ctx context.Context, cred provider.Cred, old *model.Record, ch provider.Changes) (*model.Record, error) {
	number, err := provider.PathID(old.Key, "the pull request number")
	if err != nil {
		return nil, err
	}
	tok := cred.Token
	base := r.repoURL()

	pr := map[string]any{}
	for _, k := range []string{"title", "body"} {
		if raw, ok := ch[k]; ok {
			pr[k] = raw
		}
	}
	if ch.Has("state") {
		state := "open"
		if closing(ch) {
			state = "closed"
		}
		pr["state"] = state
	}
	if len(pr) > 0 {
		if err := r.api.rest(ctx, "PATCH", base+"/pulls/"+number, tok, pr, nil); err != nil {
			return nil, err
		}
	}
	meta := map[string]any{}
	for _, k := range []string{"labels", "assignees", "milestone"} {
		if raw, ok := ch[k]; ok {
			meta[k] = raw
		}
	}
	if len(meta) > 0 {
		if err := r.api.rest(ctx, "PATCH", base+"/issues/"+number, tok, meta, nil); err != nil {
			return nil, err
		}
	}
	var comment string
	var posted *restComment
	if ch.Get("comment", &comment) && comment != "" {
		posted = &restComment{}
		if err := r.api.rest(ctx, "POST", base+"/issues/"+number+"/comments", tok, map[string]any{"body": comment}, posted); err != nil {
			return nil, err
		}
	}
	if ch.Has("reviewers") {
		if err := r.setReviewers(ctx, tok, base+"/pulls/"+number, ch); err != nil {
			return nil, err
		}
	}
	var p restPull
	if err := r.api.rest(ctx, "GET", base+"/pulls/"+number, tok, nil, &p); err != nil {
		return nil, err
	}
	if ch.Has("isDraft") {
		var want bool
		ch.Get("isDraft", &want)
		if want != p.Draft {
			if err := r.toggleDraft(ctx, tok, p.NodeID, want); err != nil {
				return nil, err
			}
			// read again: the draft changed after the first read
			p = restPull{}
			if err := r.api.rest(ctx, "GET", base+"/pulls/"+number, tok, nil, &p); err != nil {
				return nil, err
			}
		}
	}
	merged := *old
	merged.Title = p.Title
	merged.Body = derefOr(p.Body)
	merged.State = pullStateOf(p)
	merged.MergedAt = p.MergedAt
	merged.IsDraft = model.Ptr(p.Draft)
	merged.Labels = labelsFrom(p.Labels)
	merged.Assignees = assigneesFrom(p.Assignees)
	merged.Milestone = milestoneFrom(p.Milestone)
	merged.Reviewers = loginsOfUsers(p.RequestedReviewers)
	if posted != nil {
		model.AddComment(&merged, posted.comment())
	}
	merged.UpdatedAt = p.UpdatedAt
	return &merged, nil
}

// setReviewers asks for the reviewers the change lists that are not requested yet, and removes the ones not in it.
func (r *Repo) setReviewers(ctx context.Context, tok, prURL string, ch provider.Changes) error {
	var raw []json.RawMessage
	ch.Get("reviewers", &raw)
	want := map[string]bool{}
	var order []string
	for _, x := range raw {
		s := scalarText(x)
		if !want[s] {
			want[s] = true
			order = append(order, s)
		}
	}
	var now struct {
		Users []restUser `json:"users"`
	}
	if err := r.api.rest(ctx, "GET", prURL+"/requested_reviewers", tok, nil, &now); err != nil {
		return err
	}
	have := map[string]bool{}
	for _, u := range now.Users {
		have[u.Login] = true
	}
	var add, drop []string
	for _, x := range order {
		if !have[x] {
			add = append(add, x)
		}
	}
	for _, u := range now.Users {
		if !want[u.Login] {
			drop = append(drop, u.Login)
		}
	}
	if len(add) > 0 {
		if err := r.api.rest(ctx, "POST", prURL+"/requested_reviewers", tok, map[string]any{"reviewers": add}, nil); err != nil {
			return err
		}
	}
	if len(drop) > 0 {
		if err := r.api.rest(ctx, "DELETE", prURL+"/requested_reviewers", tok, map[string]any{"reviewers": drop}, nil); err != nil {
			return err
		}
	}
	return nil
}

// toggleDraft converts a pull request to a draft, or marks it ready: a GraphQL mutation on its node id.
func (r *Repo) toggleDraft(ctx context.Context, tok, nodeID string, draft bool) error {
	mutation := "markPullRequestReadyForReview"
	if draft {
		mutation = "convertPullRequestToDraft"
	}
	q := fmt.Sprintf("mutation($id: ID!) { %s(input: { pullRequestId: $id }) { pullRequest { id isDraft } } }", mutation)
	var res struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := r.api.rest(ctx, "POST", r.o.Endpoints.GraphQL, tok, map[string]any{"query": q, "variables": map[string]any{"id": nodeID}}, &res); err != nil {
		return err
	}
	if len(res.Errors) > 0 {
		return fmt.Errorf("GitHub refused the draft change: %s", res.Errors[0].Message)
	}
	return nil
}

// EditOptions is what the edit form offers: labels, milestones (open ones), members (assignees) and the issue types of
// the repo. Every list is present; a list the host would not give is empty.
func (r *Repo) EditOptions(ctx context.Context, cred provider.Cred) (provider.EditOptions, error) {
	out, err := r.editOptions(ctx, cred.Token)
	return out, asProvider(err)
}

func (r *Repo) editOptions(ctx context.Context, tok string) (provider.EditOptions, error) {
	base := r.repoURL()
	var (
		labels     []restLabel
		milestones []restMilestone
		assignees  []restUser
		types      []restIssueType
	)
	// the four lists are read at the same time; a list that fails is empty, as the Node server did
	_ = runAll(ctx, []func(context.Context) error{
		func(ctx context.Context) error {
			labels = listOrNil[restLabel](ctx, r.api, base+"/labels?per_page=100", tok)
			return nil
		},
		func(ctx context.Context) error {
			milestones = listOrNil[restMilestone](ctx, r.api, base+"/milestones?state=open&per_page=100", tok)
			return nil
		},
		func(ctx context.Context) error {
			assignees = listOrNil[restUser](ctx, r.api, base+"/assignees?per_page=100", tok)
			return nil
		},
		func(ctx context.Context) error {
			// GitHub lists the issue types of every repo (organizations and personal ones alike)
			var t []restIssueType
			if err := r.api.rest(ctx, "GET", base+"/issue-types", tok, nil, &t); err == nil {
				types = t
			}
			return nil
		},
	})
	out := provider.EditOptions{
		Trackers: []map[string]any{}, Priorities: []map[string]any{}, Members: []map[string]any{}, Versions: []map[string]any{},
		Categories: []map[string]any{}, Activities: []map[string]any{}, CustomFields: []map[string]any{},
		RelationTypes: []string{}, Labels: []map[string]any{}, Milestones: []map[string]any{}, IssueTypes: []map[string]any{},
	}
	for _, t := range types {
		if t.IsEnabled == nil || *t.IsEnabled {
			out.IssueTypes = append(out.IssueTypes, map[string]any{"id": t.ID, "name": t.Name})
		}
	}
	for _, l := range labels {
		out.Labels = append(out.Labels, map[string]any{"name": l.Name, "color": derefOr(l.Color)})
	}
	for _, m := range milestones {
		out.Milestones = append(out.Milestones, map[string]any{"id": m.Number, "name": m.Title})
	}
	for _, a := range assignees {
		out.Members = append(out.Members, map[string]any{"id": a.Login, "name": a.Login})
	}
	return out, nil
}

// listOrNil reads every page of a list; a list that fails is nil.
func listOrNil[T any](ctx context.Context, a *api, u, tok string) []T {
	items, err := restAll[T](ctx, a, u, tok, 20)
	if err != nil {
		return nil
	}
	return items
}

// comment is the comment in the store's shape.
func (c *restComment) comment() model.Comment {
	id := c.NodeID
	if id == "" {
		id = strconv.FormatInt(c.ID, 10)
	}
	var author *string
	if c.User != nil && c.User.Login != "" {
		author = model.Ptr(c.User.Login)
	}
	return model.Comment{ID: id, Author: author, CreatedAt: c.CreatedAt, Body: c.Body}
}
