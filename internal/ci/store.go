// Package ci keeps the CI runs of a code repo's window (CI_DAYS back) in one JSON file (<store>.ci.json), with the
// commits that changed the CI definition, and summarizes them for the timeline: the state of each pull request's last
// CI run, and the other workflows (Dependabot, Pages, releases) counted apart. The HTTP calls to the hosts live in
// internal/github and internal/gitlab.
package ci

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"sync"
	"time"

	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/store"
)

// Meta is what the file keeps about the runs: which repo they are, and when they were last read.
type Meta struct {
	Repo     *string `json:"repo"`
	LastSync *string `json:"lastSync"`
}

// file is the JSON of the CI store: { meta, runs: [newest first], changes }.
type file struct {
	Meta    Meta                   `json:"meta"`
	Runs    []model.CIRun          `json:"runs"`
	Changes []model.WorkflowChange `json:"changes"`
}

// Store is the CI window of one repo. Syncs write it while requests read it, so every access takes the lock.
type Store struct {
	file    string
	mu      sync.RWMutex
	saveMu  sync.Mutex // one write to disk at a time
	meta    Meta
	runs    []model.CIRun
	changes []model.WorkflowChange
}

// Open reads the CI file; a missing file is an empty window.
func Open(name string) (*Store, error) {
	s := &Store{file: name}
	b, err := os.ReadFile(name)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", name, err)
	}
	var f file
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("reading %s: %w", name, err)
	}
	s.meta = f.Meta
	s.runs = f.Runs
	s.changes = f.Changes
	return s, nil
}

// Runs returns a copy of the runs, newest first.
func (s *Store) Runs() []model.CIRun {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.CIRun, len(s.runs))
	copy(out, s.runs)
	return out
}

// Changes returns a copy of the commits that changed the CI definition, newest first.
func (s *Store) Changes() []model.WorkflowChange {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.WorkflowChange, len(s.changes))
	copy(out, s.changes)
	return out
}

// SetChanges replaces the commits that changed the CI definition.
func (s *Store) SetChanges(c []model.WorkflowChange) {
	cp := make([]model.WorkflowChange, len(c))
	copy(cp, c)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.changes = cp
}

// Meta returns a copy of what the file keeps about the runs.
func (s *Store) Meta() Meta {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return copyMeta(s.meta)
}

func copyMeta(m Meta) Meta {
	out := Meta{}
	if m.Repo != nil {
		r := *m.Repo
		out.Repo = &r
	}
	if m.LastSync != nil {
		l := *m.LastSync
		out.LastSync = &l
	}
	return out
}

// Merge adds the runs read now (replacing the same ids), keeps the rest of the window (from floor on), and drops what
// fell out of it. A store that holds another repo starts again, its changes with it. at is when the runs were read.
func (s *Store) Merge(repo string, fresh []model.CIRun, floor time.Time, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.meta.Repo == nil || *s.meta.Repo != repo {
		s.runs = nil
		s.changes = nil
	}
	seen := make(map[string]bool, len(fresh))
	for _, r := range fresh {
		seen[r.ID] = true
	}
	runs := make([]model.CIRun, 0, len(fresh)+len(s.runs))
	for _, r := range fresh {
		if !atTime(r.At).Before(floor) {
			runs = append(runs, r)
		}
	}
	for _, r := range s.runs {
		if !seen[r.ID] && !atTime(r.At).Before(floor) {
			runs = append(runs, r)
		}
	}
	sort.SliceStable(runs, func(i, j int) bool { return atTime(runs[i].At).After(atTime(runs[j].At)) })
	s.runs = runs
	r := repo
	last := formatTime(at)
	s.meta = Meta{Repo: &r, LastSync: &last}
}

// Save writes the window atomically. It takes its own read lock, so the caller holds none.
func (s *Store) Save() error {
	s.mu.RLock()
	f := file{Meta: copyMeta(s.meta), Runs: nonNilRuns(s.runs), Changes: nonNilChanges(s.changes)}
	b, err := json.Marshal(f)
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	return store.WriteAtomic(s.file, b, 0o644)
}

func nonNilRuns(r []model.CIRun) []model.CIRun {
	if r == nil {
		return []model.CIRun{}
	}
	return r
}

func nonNilChanges(c []model.WorkflowChange) []model.WorkflowChange {
	if c == nil {
		return []model.WorkflowChange{}
	}
	return c
}

// formatTime writes a time the way JS's toISOString does: UTC with milliseconds.
func formatTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

// atTime reads an RFC 3339 time (what the hosts give); a value that does not parse is the zero time, so it falls out of
// any window.
func atTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
