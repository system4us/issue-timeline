package credentials

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// Store is the secrets of one data folder. Dir holds credentials.json (the fallback, values by full name) and
// secret-origins.json (which host each plain secret is bound to, and which per-host secrets exist: the keyring cannot be
// listed). Env reads the environment (default os.Getenv); Keyring is the OS keyring (default SystemKeyring).
//
// A file that exists but cannot be read is an error, never an empty store: a write would otherwise drop every secret in
// it. The JS treats such a file as empty.
type Store struct {
	Dir     string
	Env     func(string) string
	Keyring Keyring

	// mu guards the read-modify-write of the two JSON files.
	mu sync.Mutex
}

// index is secret-origins.json, with the same JSON shape the Node server wrote.
type index struct {
	Bound  map[string]string `json:"bound"`
	Scoped []string          `json:"scoped"`
}

func (s *Store) env(name string) string {
	if s.Env == nil {
		return os.Getenv(name)
	}
	return s.Env(name)
}

func (s *Store) keyring() Keyring {
	if s.Keyring == nil {
		return SystemKeyring()
	}
	return s.Keyring
}

func (s *Store) secretsFile() string { return filepath.Join(s.Dir, "credentials.json") }
func (s *Store) indexFile() string   { return filepath.Join(s.Dir, "secret-origins.json") }

// readSecretFile reads credentials.json; a missing file is empty.
func (s *Store) readSecretFile() (map[string]string, error) {
	data := map[string]string{}
	b, err := os.ReadFile(s.secretsFile())
	if errors.Is(err, fs.ErrNotExist) {
		return data, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", s.secretsFile(), err)
	}
	if err := json.Unmarshal(b, &data); err != nil {
		return nil, fmt.Errorf("reading %s: %w", s.secretsFile(), err)
	}
	if data == nil {
		data = map[string]string{}
	}
	return data, nil
}

// readIndex reads secret-origins.json; a missing file is empty.
func (s *Store) readIndex() (index, error) {
	idx := index{Bound: map[string]string{}, Scoped: []string{}}
	b, err := os.ReadFile(s.indexFile())
	if errors.Is(err, fs.ErrNotExist) {
		return idx, nil
	}
	if err != nil {
		return idx, fmt.Errorf("reading %s: %w", s.indexFile(), err)
	}
	if err := json.Unmarshal(b, &idx); err != nil {
		return index{}, fmt.Errorf("reading %s: %w", s.indexFile(), err)
	}
	if idx.Bound == nil {
		idx.Bound = map[string]string{}
	}
	if idx.Scoped == nil {
		idx.Scoped = []string{}
	}
	return idx, nil
}

func (s *Store) saveIndex(idx index) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	return writeOwnerOnly(s.indexFile(), idx)
}

// writeOwnerOnly writes v as JSON to file through a temp file, so a crash never leaves half a file, and only its owner
// can read it.
func writeOwnerOnly(file string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

// Get is the value of a secret by its exact name, and where it came from: "env" (a plain name whose environment variable
// is set: the least safe place, so it is said when used), "keyring" or "file". ErrNotFound when it is set nowhere.
func (s *Store) Get(name string) (value, source string, err error) {
	for _, sp := range specs {
		if sp.Name == name {
			if v := s.env(sp.Env); v != "" {
				return v, "env", nil
			}
		}
	}
	if v, err := s.keyring().Get(name); err == nil && v != "" {
		return v, "keyring", nil
	}
	data, err := s.readSecretFile()
	if err != nil {
		return "", "", err
	}
	if v := data[name]; v != "" {
		return v, "file", nil
	}
	return "", "", ErrNotFound
}

// SecretFor is the secret to send to a site: the one set for that site, or else the plain one when it is bound to that
// site (a plain one is bound on its first use). origin is the site's URL. It fails when the plain secret belongs to
// another site, and returns ErrNotFound when there is none.
func (s *Store) SecretFor(base, origin string) (value, source string, err error) {
	host, err := parseOrigin(origin)
	if err != nil {
		return "", "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if v, src, err := s.Get(fullName(base, host)); err == nil {
		return v, src, nil
	} else if !errors.Is(err, ErrNotFound) {
		return "", "", err
	}
	plain, src, err := s.Get(base)
	if err != nil {
		return "", "", err
	}
	idx, err := s.readIndex()
	if err != nil {
		return "", "", err
	}
	bound := idx.Bound[base]
	if bound == "" {
		idx.Bound[base] = host
		if err := s.saveIndex(idx); err != nil {
			return "", "", err
		}
		return plain, src, nil
	}
	if bound == host {
		return plain, src, nil
	}
	return "", "", fmt.Errorf("the %s that is set belongs to %s, not to %s: run `issue-timeline credentials set %s %s`", base, bound, host, base, host)
}

// SiteSecret is the secret set for one site only ("name@origin"). It never falls back to the plain secret and never binds
// it: a site that no configured repo uses yet is checked this way, so adding a repo cannot make a plain secret belong to
// it. It returns ErrNotFound when none is set for the site.
func (s *Store) SiteSecret(base, origin string) (string, error) {
	host, err := parseOrigin(origin)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, _, err := s.Get(fullName(base, host))
	return v, err
}

// Set saves a secret: to the keyring when it is there, otherwise to the owner-only file. It returns where it went
// ("keyring" or "file"). A plain value is bound again on its first use, as it may be for another site.
func (s *Store) Set(name, value string) (where string, err error) {
	base, origin, err := SplitName(name)
	if err != nil {
		return "", err
	}
	if value == "" {
		return "", errors.New("the value is empty")
	}
	full := fullName(base, origin)

	s.mu.Lock()
	defer s.mu.Unlock()
	where = "keyring"
	if err := s.keyring().Set(full, value); err != nil {
		where = "file"
		if err := s.writeFileSecret(full, value); err != nil {
			return "", err
		}
	}
	idx, err := s.readIndex()
	if err != nil {
		return "", err
	}
	if origin != "" {
		if !contains(idx.Scoped, full) {
			idx.Scoped = append(idx.Scoped, full)
		}
	} else {
		delete(idx.Bound, base)
	}
	if err := s.saveIndex(idx); err != nil {
		return "", err
	}
	return where, nil
}

func (s *Store) writeFileSecret(full, value string) error {
	data, err := s.readSecretFile()
	if err != nil {
		return err
	}
	data[full] = value
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	return writeOwnerOnly(s.secretsFile(), data)
}

// Clear removes a secret from wherever it is kept.
func (s *Store) Clear(name string) error {
	base, origin, err := SplitName(name)
	if err != nil {
		return err
	}
	full := fullName(base, origin)

	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.keyring().Delete(full) // not being there is not an error
	data, err := s.readSecretFile()
	if err != nil {
		return err
	}
	if _, ok := data[full]; ok {
		delete(data, full)
		if err := writeOwnerOnly(s.secretsFile(), data); err != nil {
			return err
		}
	}
	idx, err := s.readIndex()
	if err != nil {
		return err
	}
	idx.Scoped = remove(idx.Scoped, full)
	if origin == "" {
		delete(idx.Bound, base)
	}
	return s.saveIndex(idx)
}

// List says where each secret is ("keyring", "file", or "" when it is not set), never its value: the plain ones, then the
// per-site ones that exist.
func (s *Store) List() (map[string]string, error) {
	out := map[string]string{}
	for _, name := range KnownNames() {
		where, err := s.whereIs(name)
		if err != nil {
			return nil, err
		}
		out[name] = where
	}
	idx, err := s.readIndex()
	if err != nil {
		return nil, err
	}
	for _, name := range idx.Scoped {
		where, err := s.whereIs(name)
		if err != nil {
			return nil, err
		}
		out[name] = where
	}
	return out, nil
}

// whereIs is where a secret is kept, without reading it into anything: "keyring", "file" or "".
func (s *Store) whereIs(name string) (string, error) {
	if v, err := s.keyring().Get(name); err == nil && v != "" {
		return "keyring", nil
	}
	data, err := s.readSecretFile()
	if err != nil {
		return "", err
	}
	if data[name] != "" {
		return "file", nil
	}
	return "", nil
}

// BoundOrigins is which site each plain secret is bound to (never the values).
func (s *Store) BoundOrigins() (map[string]string, error) {
	idx, err := s.readIndex()
	if err != nil {
		return nil, err
	}
	return idx.Bound, nil
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func remove(list []string, v string) []string {
	out := make([]string, 0, len(list))
	for _, x := range list {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}
