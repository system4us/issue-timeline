package app

import (
	"context"
	"fmt"
	"strconv"

	"gitlab.com/system4us/issue-timeline/internal/derive"
	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/provider"
)

// The writes. The caller has checked the repo's mode and the writer's credential. The host is asked first; what it
// says is kept, then the other side of each link and the notes are applied to the records the store keeps, so the
// timeline shows the change before the next sync. Records are replaced, never changed in place.

// editComment is one note of an edit's editComments list (its id may come as a number).
type editComment struct {
	ID   model.Ref `json:"id"`
	Body string    `json:"body"`
}

// editIssue changes an issue on its host and keeps the record the host reads back. The version's own date stays while
// the milestone title is the same (the host's read-back has none).
func (s *state) editIssue(ctx context.Context, repo string, cred provider.Cred, key string, ch provider.Changes) (*model.Record, []string, error) {
	es, ok := s.get(repo)
	if !ok {
		return nil, nil, provider.Errorf(400, "repo not configured")
	}
	ed, ok := es.Host.(provider.Editor)
	if !ok {
		return nil, nil, provider.Errorf(501, "editing %s issues is not built yet", providerName(repo))
	}
	es.Store.RLock()
	old := es.Store.Data.Issues[key]
	es.Store.RUnlock()
	if old == nil {
		return nil, nil, provider.Errorf(404, "not found")
	}
	fresh, warnings, err := ed.EditIssue(ctx, cred, old, ch)
	if err != nil {
		return nil, nil, err
	}
	if fresh == nil {
		return nil, warnings, fmt.Errorf("the host gave no issue for %s", key)
	}
	if fresh.Milestone != nil && old.Milestone != nil && fresh.Milestone.Title == old.Milestone.Title {
		m := *fresh.Milestone
		m.DueOn = old.Milestone.DueOn
		c := *fresh
		c.Milestone = &m
		fresh = &c
	}

	var add, remove []derive.LinkChange
	_ = ch.Get("addLinks", &add)
	_ = ch.Get("removeLinks", &remove)
	var parent *model.Ref
	setParent := ch.Has("parent")
	if setParent && !ch.IsNull("parent") {
		var p model.Ref
		if ch.Get("parent", &p) {
			parent = &p
		}
	}
	var comments []editComment
	_ = ch.Get("editComments", &comments)
	edits := make([]derive.CommentEdit, 0, len(comments))
	for _, c := range comments {
		edits = append(edits, derive.CommentEdit{ID: string(c.ID), Body: c.Body})
	}
	var deletedRefs []model.Ref
	_ = ch.Get("deleteComments", &deletedRefs)
	deletes := make([]string, 0, len(deletedRefs))
	for _, d := range deletedRefs {
		deletes = append(deletes, string(d))
	}

	c := *fresh
	c.Key = key
	es.Store.Lock()
	es.Store.Data.Upsert("issues", &c)
	derive.ApplyLinks(es.Store.Data.Issues, key, add, remove, parent, setParent)
	derive.ApplyComments(es.Store.Data.Issues, key, edits, deletes)
	stored := es.Store.Data.Issues[key]
	es.Store.Unlock()
	if err := es.Store.Save(); err != nil {
		return nil, warnings, fmt.Errorf("saving %s: %w", repo, err)
	}
	s.edited(repo, "issue", key)
	return stored, warnings, nil
}

// createIssue creates an issue on the host and keeps it.
func (s *state) createIssue(ctx context.Context, repo string, cred provider.Cred, ch provider.Changes) (*model.Record, error) {
	es, ok := s.get(repo)
	if !ok {
		return nil, provider.Errorf(400, "repo not configured")
	}
	ed, ok := es.Host.(provider.Editor)
	if !ok {
		return nil, provider.Errorf(501, "creating %s issues is not built yet", providerName(repo))
	}
	rec, err := ed.CreateIssue(ctx, cred, ch)
	if err != nil {
		return nil, err
	}
	if rec == nil {
		return nil, fmt.Errorf("the host gave no issue")
	}
	c := *rec
	if c.Key == "" {
		if c.Number != nil {
			c.Key = strconv.Itoa(*c.Number)
		} else {
			c.Key = c.ID
		}
	}
	es.Store.Lock()
	es.Store.Data.Upsert("issues", &c)
	stored := es.Store.Data.Issues[c.Key]
	es.Store.Unlock()
	if err := es.Store.Save(); err != nil {
		return nil, fmt.Errorf("saving %s: %w", repo, err)
	}
	s.edited(repo, "issue", c.Key)
	return stored, nil
}

// editPull changes a pull or merge request on its host and keeps the record the host reads back.
func (s *state) editPull(ctx context.Context, repo string, cred provider.Cred, key string, ch provider.Changes) (*model.Record, error) {
	es, ok := s.get(repo)
	if !ok {
		return nil, provider.Errorf(400, "repo not configured")
	}
	ed, ok := es.Host.(provider.PullEditor)
	if !ok {
		return nil, provider.Errorf(501, "%s has no pull requests to edit", providerName(repo))
	}
	es.Store.RLock()
	old := es.Store.Data.Pulls[key]
	es.Store.RUnlock()
	if old == nil {
		return nil, provider.Errorf(404, "not found")
	}
	fresh, err := ed.EditPull(ctx, cred, old, ch)
	if err != nil {
		return nil, err
	}
	if fresh == nil {
		return nil, fmt.Errorf("the host gave no pull request for %s", key)
	}
	c := *fresh
	c.Key = key
	es.Store.Lock()
	es.Store.Data.Upsert("pulls", &c)
	stored := es.Store.Data.Pulls[key]
	es.Store.Unlock()
	if err := es.Store.Save(); err != nil {
		return nil, fmt.Errorf("saving %s: %w", repo, err)
	}
	s.edited(repo, "pull", key)
	return stored, nil
}

// edited tells the pages open on the repo that a record changed, and syncs the repo soon: the edit's read-back carries
// the record's fields, the sync brings what it does not (the history the change made, the other side of links).
func (s *state) edited(repo, kind, key string) {
	s.events.publish(Event{Repo: repo, Type: "edit", Items: []EventItem{{Kind: kind, Key: key}}})
	s.syncSoon(repo)
}
