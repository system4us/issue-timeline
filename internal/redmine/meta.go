package redmine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"time"

	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/provider"
	"gitlab.com/system4us/issue-timeline/internal/store"
)

// metaBatch is how many issues one metadata refresh reads: META_BATCH, 20 by default.
var metaBatch = envInt("META_BATCH", 20)

func envInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v > 0 {
		return v
	}
	return def
}

// Refresh reads one issue again: the metadata Redmine gives now, merged onto rec. Redmine has no pull requests, so kind
// "pull" is not supported. provider.ErrNotFound when Redmine no longer shows the issue.
func (r *Repo) Refresh(ctx context.Context, kind string, rec *model.Record) (*model.Record, error) {
	if kind == "pull" {
		return nil, provider.ErrNotSupported
	}
	id, err := pathIssueID(rec.Key)
	if err != nil {
		return nil, err
	}
	c, err := r.serverClient(ctx)
	if err != nil {
		return nil, err
	}
	n, err := r.readContext(ctx, c)
	if err != nil {
		return nil, err
	}
	fresh, err := readMeta(ctx, c, id, n)
	if err != nil {
		return nil, err
	}
	out := *rec
	applyMeta(&out, fresh)
	out.ReadAt = time.Now().UTC().Format(time.RFC3339Nano)
	return &out, nil
}

// RefreshMeta reads again the metadata of the least recently read issues, a batch per sync. Redmine has no quota, so the
// remaining count and the reset time are nil. An issue Redmine no longer shows is skipped.
func (r *Repo) RefreshMeta(ctx context.Context, st *store.Store) (int, *int, *time.Time, error) {
	var batch []*model.Record
	st.View(func(d *store.Data) { batch = pickStale(d.Issues, metaBatch) })
	if len(batch) == 0 {
		return 0, nil, nil, nil
	}
	c, err := r.serverClient(ctx)
	if err != nil {
		return 0, nil, nil, err
	}
	n, err := r.readContext(ctx, c)
	if err != nil {
		return 0, nil, nil, err
	}
	read := 0
	var loopErr error
	for _, rec := range batch {
		id, err := pathIssueID(rec.Key)
		if err != nil {
			continue
		}
		fresh, err := readMeta(ctx, c, id, n)
		if errors.Is(err, provider.ErrNotFound) {
			continue
		}
		if err != nil {
			loopErr = err
			break
		}
		stamp := time.Now().UTC().Format(time.RFC3339Nano)
		applied := false
		st.Update(func(d *store.Data) {
			cur, ok := d.Issues[rec.Key]
			if !ok {
				return
			}
			next := *cur
			applyMeta(&next, fresh)
			next.ReadAt = stamp
			d.Issues[rec.Key] = &next
			applied = true
		})
		if applied {
			read++
		}
	}
	if read > 0 {
		if err := st.Save(); err != nil {
			return read, nil, nil, err
		}
	}
	if loopErr != nil {
		return read, nil, nil, loopErr
	}
	return read, nil, nil, nil
}

// readContext reads what a metadata read needs once per batch: the closed statuses and the custom field definitions.
func (r *Repo) readContext(ctx context.Context, c client) (normCtx, error) {
	closed, err := statuses(ctx, c)
	if err != nil {
		return normCtx{}, err
	}
	return normCtx{closed: closed, origin: r.origin, defs: customFieldDefs(ctx, c)}, nil
}

// readMeta reads one issue with its relations, children and watchers: what the metadata refresh keeps. A 404 is
// provider.ErrNotFound.
func readMeta(ctx context.Context, c client, id int, n normCtx) (*model.Record, error) {
	raw, err := fetchIssue(ctx, c, id, metaInclude)
	if err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf("Redmine does not show this issue any more: %w", provider.ErrNotFound)
		}
		return nil, err
	}
	return normalizeIssue(raw, n), nil
}

// pickStale is the issues to read again: the least recently read first (never read comes first), at most n.
func pickStale(issues map[string]*model.Record, n int) []*model.Record {
	var list []*model.Record
	for _, r := range issues {
		if r.Kind == "issue" {
			list = append(list, r)
		}
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].ReadAt != list[j].ReadAt {
			return list[i].ReadAt < list[j].ReadAt
		}
		return list[i].Key < list[j].Key
	})
	if len(list) > n {
		list = list[:n]
	}
	return list
}

// applyMeta copies the metadata a read gives onto dst (the comments, events and attachments are not metadata and stay).
// A milestone keeps the due date the store has when it is the same version: a read does not carry a version's date.
// dst is a copy the caller owns; src is not kept.
func applyMeta(dst, src *model.Record) {
	dst.State = src.State
	dst.ClosedAt = src.ClosedAt
	dst.StateReason = src.StateReason
	dst.Tracker = src.Tracker
	dst.Priority = src.Priority
	dst.AssigneeID = src.AssigneeID
	dst.Assignees = src.Assignees
	dst.VersionID = src.VersionID
	dst.Milestone = withDueOf(dst.Milestone, src.Milestone)
	dst.CategoryID = src.CategoryID
	dst.DoneRatio = src.DoneRatio
	dst.EstimatedHours = src.EstimatedHours
	dst.Parent = src.Parent
	dst.ParentID = src.ParentID
	dst.SubIssues = src.SubIssues
	dst.BlockedBy = src.BlockedBy
	dst.Blocking = src.Blocking
	dst.Relations = src.Relations
	dst.IsPrivate = src.IsPrivate
	dst.Watchers = src.Watchers
	dst.Labels = src.Labels
	dst.StartDate = src.StartDate
	dst.DueDate = src.DueDate
	dst.CustomFields = src.CustomFields
}

// withDueOf is the fresh milestone, with the due date of the old one when both name the same version and the old one
// has a date. The fresh one is copied, never changed.
func withDueOf(old, fresh *model.Milestone) *model.Milestone {
	if fresh == nil {
		return nil
	}
	m := *fresh
	if old != nil && old.Title == m.Title && old.DueOn != nil {
		m.DueOn = old.DueOn
	}
	return &m
}
