// Package commits keeps the commits of a code repo's default branch in one JSON file (<store>.commits.json), the diffs
// of single commits in a cache of their own, and the pure helpers the hosts share for them: finishFiles, countLines,
// the merge method of a GitHub pull request. The HTTP calls to the hosts live in internal/github and internal/gitlab.
package commits

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"sync"
	"time"

	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/store"
)

// FullSHA is a full commit id: 40 lowercase hex digits (FULL_SHA in commits.js).
var FullSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// meta is what the file keeps about the history: which repo it is, and when it was last read.
type meta struct {
	Repo     *string `json:"repo"`
	LastSync *string `json:"lastSync"`
}

// file is the JSON of the commit store: { meta, items: [newest first] }.
type file struct {
	Meta  meta           `json:"meta"`
	Items []model.Commit `json:"items"`
}

// Store is the commit history of one repo. Syncs write it while requests read it, so every access takes the lock.
type Store struct {
	file   string
	mu     sync.RWMutex
	saveMu sync.Mutex // one write to disk at a time
	meta   meta
	items  []model.Commit // newest first
	bySha  map[string]model.Commit
}

// Open reads the commit file; a missing file is an empty history.
func Open(name string) (*Store, error) {
	s := &Store{file: name, bySha: map[string]model.Commit{}}
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
	s.items = f.Items
	for _, c := range s.items {
		s.bySha[c.Sha] = c
	}
	return s, nil
}

// Items returns a copy of the commits, newest first.
func (s *Store) Items() []model.Commit {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.Commit, len(s.items))
	copy(out, s.items)
	return out
}

// Has reports whether the commit is stored.
func (s *Store) Has(sha string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.bySha[sha]
	return ok
}

// Get returns the stored commit with that id.
func (s *Store) Get(sha string) (model.Commit, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.bySha[sha]
	return c, ok
}

// LastSync is when the history was last read, nil when never.
func (s *Store) LastSync() *string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.meta.LastSync == nil {
		return nil
	}
	v := *s.meta.LastSync
	return &v
}

// ownsLocked reports whether the store holds the history of repo. The caller holds the lock.
func (s *Store) ownsLocked(repo string) bool {
	return s.meta.Repo != nil && *s.meta.Repo == repo
}

// merge puts the commits that are not known yet on top (they are newer) and records when the history was read. A store
// that holds another repo starts again.
func (s *Store) merge(newer []model.Commit, repo string, at time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.ownsLocked(repo) {
		s.items = nil
		s.bySha = map[string]model.Commit{}
	}
	fresh := make([]model.Commit, 0, len(newer))
	for _, c := range newer {
		if _, ok := s.bySha[c.Sha]; !ok {
			fresh = append(fresh, c)
		}
	}
	items := make([]model.Commit, 0, len(fresh)+len(s.items))
	items = append(items, fresh...)
	items = append(items, s.items...)
	s.items = items
	s.bySha = make(map[string]model.Commit, len(items))
	for _, c := range items {
		s.bySha[c.Sha] = c
	}
	r := repo
	last := formatTime(at)
	s.meta = meta{Repo: &r, LastSync: &last}
	return len(fresh)
}

// Save writes the history atomically. It takes its own read lock, so the caller holds none.
func (s *Store) Save() error {
	s.mu.RLock()
	items := s.items
	if items == nil {
		items = []model.Commit{}
	}
	b, err := json.Marshal(file{Meta: s.meta, Items: items})
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	return store.WriteAtomic(s.file, b, 0o644)
}

// formatTime writes a time the way JS's toISOString does: UTC with milliseconds.
func formatTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

// Sync reads the history from the newest commit back until it reaches one already stored: a first sync (or a sync of
// another repo) walks everything, the later ones read one page. fetchPage(ctx, cursor) returns the page after cursor
// ("" for the first) with its items newest first and its Next cursor ("" at the end). The result is saved, and added
// is how many commits were new.
func Sync(ctx context.Context, st *Store, repo string, fetchPage func(ctx context.Context, cursor string) (model.CommitPage, error)) (added int, err error) {
	st.mu.RLock()
	own := st.ownsLocked(repo)
	st.mu.RUnlock()

	var fresh []model.Commit
	cursor := ""
	for {
		page, err := fetchPage(ctx, cursor)
		if err != nil {
			return 0, fmt.Errorf("reading the commit history of %s: %w", repo, err)
		}
		reached := false
		for _, c := range page.Items {
			if own && st.Has(c.Sha) {
				reached = true
				break
			}
			fresh = append(fresh, c)
		}
		if reached || page.Next == "" {
			break
		}
		if page.Next == cursor {
			return 0, fmt.Errorf("reading the commit history of %s: the host gave the same page twice", repo)
		}
		cursor = page.Next
	}
	added = st.merge(fresh, repo, time.Now())
	if err := st.Save(); err != nil {
		return added, fmt.Errorf("saving the commits of %s: %w", repo, err)
	}
	return added, nil
}
