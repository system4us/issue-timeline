package app

import (
	"context"
	"fmt"
	"time"

	"gitlab.com/system4us/issue-timeline/internal/ci"
	"gitlab.com/system4us/issue-timeline/internal/commits"
	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/provider"
	"gitlab.com/system4us/issue-timeline/internal/store"
)

// metaPause is how long a repo's metadata reads wait when the quota is low and the host gives no reset time (as the
// Node server did).
const metaPause = 15 * time.Minute

// logf logs a line about one repo through Config.Log.
func (s *state) logf(repo, format string, a ...any) {
	s.cfg.Log("%s: "+format, append([]any{repo}, a...)...)
}

// start syncs the repos that are stale, then every poll interval, until ctx ends. Each sync runs in its own goroutine.
func (s *state) start(ctx context.Context) {
	s.mu.Lock()
	s.root = ctx
	s.mu.Unlock()
	// the event streams end with the server, so its shutdown does not wait for them
	go func() { <-ctx.Done(); s.events.close() }()
	now := time.Now()
	for _, repo := range s.repos() {
		es, ok := s.get(repo)
		if !ok {
			continue
		}
		es.Store.RLock()
		last := es.Store.Data.Meta.LastSync
		es.Store.RUnlock()
		t, err := time.Parse(time.RFC3339Nano, deref(last))
		if last == nil || err != nil || now.Sub(t) > s.interval {
			s.runSync(repo, false)
			continue
		}
		next := t.Add(s.interval).UTC().Format(time.RFC3339Nano)
		es.update(func(st *Status) { st.NextSyncAt = &next })
	}
	go s.registerHooks()
	go func() {
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				now := time.Now()
				for _, repo := range s.repos() {
					// a repo whose host sends webhooks is synced when they arrive; the poll is the safety net
					if es, ok := s.get(repo); ok && es.hookFresh(now) {
						continue
					}
					s.runSync(repo, false)
				}
			}
		}
	}()
}

// deref is the text of an optional string ("" when nil).
func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// rootContext is the context the syncs run with: the one Start was given (Background before that).
func (s *state) rootContext() context.Context {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.root
}

// runSync starts a sync of repo in the background; false when it is not configured or one is already running.
func (s *state) runSync(repo string, full bool) bool {
	es, ok := s.get(repo)
	if !ok || s.isStopped() {
		return false
	}
	started := false
	es.update(func(st *Status) {
		if st.Syncing {
			return
		}
		st.Syncing = true
		st.Progress = &provider.Progress{Phase: "Preparing"}
		started = true
	})
	if !started {
		return false
	}
	s.events.publish(Event{Repo: repo, Type: "sync-start"})
	go s.syncRepo(s.rootContext(), es, full)
	return true
}

// syncRepo is one sync of a repo: its issues and pulls (in full when due), the metadata reads, then for a code repo
// its commits and CI. An error of the main read is the status's lastError; the others are their own errors.
func (s *state) syncRepo(ctx context.Context, es *entryState, full bool) {
	repo := es.Repo
	changed := 0
	defer func() {
		es.update(func(st *Status) {
			st.Syncing = false
			next := time.Now().Add(s.interval).UTC().Format(time.RFC3339Nano)
			st.NextSyncAt = &next
			st.Progress = nil
		})
		s.events.publish(Event{Repo: repo, Type: "sync-end", Changed: changed})
		// asked for while this one ran (an edit, a webhook): read what moved meanwhile
		es.mu.Lock()
		again := es.again
		es.again = false
		es.mu.Unlock()
		if again {
			s.runSync(repo, false)
		}
	}()
	st := es.Store
	st.RLock()
	due := store.FullDue(st.Data.Meta, time.Now())
	st.RUnlock()
	if !full && due {
		full = true
	}
	setProgress := func(phase string) {
		es.update(func(s *Status) { s.Progress = &provider.Progress{Phase: phase} })
	}
	opt := provider.SyncOptions{
		Full:     full,
		Log:      func(m string) { s.logf(repo, "%s", m) },
		Progress: func(p provider.Progress) { es.update(func(s *Status) { s.Progress = &p }) },
		Now:      time.Now,
	}
	entry, err := es.Host.Sync(ctx, st, opt)
	changed = entry.Issues + entry.Pulls
	if err != nil {
		msg := err.Error()
		es.update(func(s *Status) { s.LastError = &msg })
		s.logf(repo, "sync failed: %s", msg)
		return
	}
	es.update(func(s *Status) { s.LastError = nil })

	s.refreshMeta(ctx, es)

	ch, ok := es.Host.(provider.CodeHost)
	if !ok {
		return // trackers: no commits, no CI
	}
	setProgress("Reading commits")
	fetch := func(ctx context.Context, cursor string) (model.CommitPage, error) {
		return ch.CommitPage(ctx, cursor)
	}
	_, err = commits.Sync(ctx, es.Commits, repo, fetch)
	if err != nil {
		msg := err.Error()
		es.update(func(s *Status) { s.CommitsError = &msg })
		s.logf(repo, "commits sync failed: %s", msg)
	} else {
		es.update(func(s *Status) { s.CommitsError = nil })
	}

	setProgress("Reading CI runs")
	if err := s.syncCI(ctx, repo, es, ch); err != nil {
		msg := err.Error()
		es.update(func(s *Status) { s.CIError = &msg })
		s.logf(repo, "CI sync failed: %s", msg)
	} else {
		es.update(func(s *Status) { s.CIError = nil })
	}
}

// syncCI reads the CI runs of the window (from the last sync on), and the commits that changed the CI definition, then
// saves the window.
func (s *state) syncCI(ctx context.Context, repo string, es *entryState, ch provider.CodeHost) error {
	now := time.Now()
	floor := ci.WindowFloor(now)
	since := ci.ReadFrom(now, es.CI)
	runs, err := ch.CIRuns(ctx, since)
	if err != nil {
		return err
	}
	changes, err := ch.WorkflowChanges(ctx, floor)
	if err != nil {
		return err
	}
	es.CI.Merge(repo, runs, floor, now)
	es.CI.SetChanges(changes)
	if err := es.CI.Save(); err != nil {
		return fmt.Errorf("saving CI of %s: %w", repo, err)
	}
	return nil
}

// refreshMeta reads again the metadata a host changes without the record's own update time (GitHub, Redmine), a
// batch per sync. When GitHub's quota falls under the floor, the repo's metadata reads wait for the reset. Errors are
// logged, never fatal.
func (s *state) refreshMeta(ctx context.Context, es *entryState) {
	mr, ok := es.Host.(provider.MetaRefresher)
	if !ok {
		return
	}
	es.mu.Lock()
	skip := es.metaSkip
	es.mu.Unlock()
	now := time.Now()
	if now.Before(skip) {
		return
	}
	read, remaining, resetAt, err := mr.RefreshMeta(ctx, es.Store)
	if err != nil {
		s.logf(es.Repo, "metadata refresh failed: %s", err)
		return
	}
	if read > 0 {
		s.logf(es.Repo, "metadata: %d issues read again", read)
	}
	if remaining == nil || *remaining >= s.metaFloor {
		return
	}
	until := now.Add(metaPause)
	if resetAt != nil && resetAt.After(now) {
		until = *resetAt
	}
	es.mu.Lock()
	es.metaSkip = until
	es.mu.Unlock()
	s.logf(es.Repo, "metadata paused: quota at %d", *remaining)
}
