// Package store keeps one repo's records in one JSON file: { meta, issues: {key: Record}, pulls: {key: Record} }, the
// same file the Node server wrote. Writes are atomic (temp file + rename) and serialized.
//
// Concurrency: a Store is shared by the sync of its repo and by the API's requests. Every read of Data takes RLock (or
// uses View), every change takes Lock (or uses Update); Save takes its own snapshot under RLock.
package store

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"gitlab.com/system4us/issue-timeline/internal/model"
)

// Schema is the shape of the records. A store written with another one is read again in full on its next sync. Bump it
// when a record gets a field no sync filled before. 3: comments carry Private (Redmine private notes, GitLab internal
// notes), and GitLab issues carry Confidential.
const Schema = 3

// SyncEntry is one sync, as meta.syncs keeps it.
type SyncEntry struct {
	At     string `json:"at"`
	Full   bool   `json:"full"`
	Issues int    `json:"issues"`
	Pulls  int    `json:"pulls"`
	Ms     int64  `json:"ms"`
}

type Totals struct {
	Issues int `json:"issues"`
	Pulls  int `json:"pulls"`
}

type Meta struct {
	Repo       *string     `json:"repo"`
	LastSync   *string     `json:"lastSync"`
	FullSyncAt *string     `json:"fullSyncAt"`
	Schema     int         `json:"schema"`
	Syncs      []SyncEntry `json:"syncs"`
	Totals     *Totals     `json:"totals,omitempty"`
	// Extra keeps the meta fields this struct does not name (a host's own: url, labels...).
	Extra map[string]json.RawMessage `json:"-"`
}

type Data struct {
	Meta   Meta                     `json:"meta"`
	Issues map[string]*model.Record `json:"issues"`
	Pulls  map[string]*model.Record `json:"pulls"`
}

// Bag is the records of one kind: "issues" or "pulls".
func (d *Data) Bag(kind string) map[string]*model.Record {
	if kind == "pulls" || kind == "pull" {
		return d.Pulls
	}
	return d.Issues
}

type Store struct {
	File string
	mu   sync.RWMutex
	wmu  sync.Mutex // one write to disk at a time
	Data *Data
}

func empty() *Data {
	return &Data{Meta: Meta{Schema: Schema, Syncs: []SyncEntry{}}, Issues: map[string]*model.Record{}, Pulls: map[string]*model.Record{}}
}

// Open reads the file; a missing file is an empty store.
func Open(file string) (*Store, error) {
	s := &Store{File: file, Data: empty()}
	b, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	d := empty()
	if err := json.Unmarshal(b, d); err != nil {
		return nil, err
	}
	if d.Issues == nil {
		d.Issues = map[string]*model.Record{}
	}
	if d.Pulls == nil {
		d.Pulls = map[string]*model.Record{}
	}
	if d.Meta.Syncs == nil {
		d.Meta.Syncs = []SyncEntry{}
	}
	if d.Meta.Schema != Schema { // a file without one is the first version
		d.Meta.FullSyncAt = nil
		d.Meta.Schema = Schema
	}
	// stores written before the keys were text have records with only a number: the key is that number
	for _, bag := range []map[string]*model.Record{d.Issues, d.Pulls} {
		for k, r := range bag {
			if r.Key == "" {
				if r.Number != nil {
					r.Key = strconv.Itoa(*r.Number)
				} else {
					r.Key = k
				}
			}
		}
	}
	s.Data = d
	return s, nil
}

// View runs fn with the data, for reading only.
func (s *Store) View(fn func(d *Data)) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	fn(s.Data)
}

// Update runs fn with the data, to change it.
func (s *Store) Update(fn func(d *Data)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(s.Data)
}

func (s *Store) RLock()   { s.mu.RLock() }
func (s *Store) RUnlock() { s.mu.RUnlock() }
func (s *Store) Lock()    { s.mu.Lock() }
func (s *Store) Unlock()  { s.mu.Unlock() }

// Upsert inserts or replaces records by key, stamping ReadAt; returns how many were new. The caller holds Lock (or
// calls it inside Update).
func (d *Data) Upsert(kind string, items ...*model.Record) int {
	bag := d.Bag(kind)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	added := 0
	for _, it := range items {
		it.ReadAt = now
		if _, ok := bag[it.Key]; !ok {
			added++
		}
		bag[it.Key] = it
	}
	return added
}

// Save writes the store atomically.
func (s *Store) Save() error {
	s.mu.RLock()
	b, err := json.Marshal(s.Data)
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return WriteAtomic(s.File, b, 0o644)
}

// WriteAtomic writes b to file through a temp file and a rename.
func WriteAtomic(file string, b []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

// FullEvery is how old the last full read may be before the next sync is a full one (FULL_SYNC_HOURS, a day).
func FullEvery() time.Duration {
	if h, err := strconv.ParseFloat(os.Getenv("FULL_SYNC_HOURS"), 64); err == nil && h > 0 {
		return time.Duration(h * float64(time.Hour))
	}
	return 24 * time.Hour
}

// FullDue reports whether the store is due a full read.
func FullDue(m Meta, now time.Time) bool {
	if m.FullSyncAt == nil {
		return true
	}
	t, err := time.Parse(time.RFC3339Nano, *m.FullSyncAt)
	return err != nil || now.Sub(t) > FullEvery()
}

type metaAlias Meta

func (m Meta) MarshalJSON() ([]byte, error) {
	b, err := json.Marshal(metaAlias(m))
	if err != nil || len(m.Extra) == 0 {
		return b, err
	}
	var out map[string]json.RawMessage
	_ = json.Unmarshal(b, &out)
	for k, v := range m.Extra {
		if _, ok := out[k]; !ok {
			out[k] = v
		}
	}
	return json.Marshal(out)
}

func (m *Meta) UnmarshalJSON(b []byte) error {
	var a metaAlias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	var all map[string]json.RawMessage
	_ = json.Unmarshal(b, &all)
	for _, k := range []string{"repo", "lastSync", "fullSyncAt", "schema", "syncs", "totals"} {
		delete(all, k)
	}
	if len(all) > 0 {
		a.Extra = all
	}
	*m = Meta(a)
	return nil
}
