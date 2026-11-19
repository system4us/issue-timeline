package app

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"gitlab.com/system4us/issue-timeline/internal/gitlab"
	"gitlab.com/system4us/issue-timeline/internal/jira"
	"gitlab.com/system4us/issue-timeline/internal/provider"
	"gitlab.com/system4us/issue-timeline/internal/redmine"
	"gitlab.com/system4us/issue-timeline/internal/store"
)

// Each repo is "read" or "write" (repo-modes.js): a repo added is read until it is switched on purpose. The modes live
// in repo-modes.json, the same JSON the Node server wrote.

func validMode(m string) bool { return m == "read" || m == "write" }

const modeHelp = "mode must be one of: read, write"

// repoModes is the mode of each repo, guarded by its own mutex, and the file they are saved to.
type repoModes struct {
	mu   sync.Mutex
	file string
	m    map[string]string
}

// loadModes reads repo-modes.json; a missing or unreadable file is an empty map, and a value that is not a mode is
// dropped.
func loadModes(file string) *repoModes {
	r := &repoModes{file: file, m: map[string]string{}}
	var raw map[string]any
	_ = readJSONFile(file, &raw)
	for repo, v := range raw {
		if m, ok := v.(string); ok && validMode(m) {
			r.m[repo] = m
		}
	}
	return r
}

// get is a repo's mode. A repo without one is "read": the safe default.
func (r *repoModes) get(repo string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if m, ok := r.m[repo]; ok {
		return m
	}
	return "read"
}

// set saves a repo's mode on disk too.
func (r *repoModes) set(repo, mode string) error {
	if !validMode(mode) {
		return provider.Errorf(400, "%s", modeHelp)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m[repo] = mode
	b, err := json.Marshal(r.m)
	if err != nil {
		return err
	}
	return store.WriteAtomic(r.file, b, 0o644)
}

// remove drops a repo's mode from disk when present.
func (r *repoModes) remove(repo string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.m[repo]; !ok {
		return nil
	}
	delete(r.m, repo)
	b, err := json.Marshal(r.m)
	if err != nil {
		return err
	}
	return store.WriteAtomic(r.file, b, 0o644)
}

func (s *state) mode(repo string) string { return s.modes.get(repo) }

func (s *state) setMode(repo, mode string) error {
	if !validMode(mode) {
		return provider.Errorf(400, "%s", modeHelp)
	}
	if err := s.modes.set(repo, mode); err != nil {
		return fmt.Errorf("saving the mode of %s: %w", repo, err)
	}
	return nil
}

// repos is the configured repos, in the order they were added.
func (s *state) repos() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string{}, s.order...)
}

func (s *state) entry(repo string) (*Entry, bool) {
	es, ok := s.get(repo)
	if !ok {
		return nil, false
	}
	return &es.Entry, true
}

// linkedOf is the code repos linked to a tracker (configured ones only), in the order they were linked.
func (s *state) linkedOf(tracker string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string{}, s.linked[tracker]...)
}

// links is a copy of every tracker's code repos.
func (s *state) links() map[string][]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string][]string, len(s.linked))
	for k, v := range s.linked {
		out[k] = append([]string{}, v...)
	}
	return out
}

// setLinks links a tracker to code repos. The checks are the POST /api/links route's: a tracker and code repos that are
// configured, no two of them with the same name. An empty list unlinks.
func (s *state) setLinks(tracker string, code []string) error {
	if !s.configured(tracker) || isCodeRepo(tracker) {
		return provider.Errorf(400, "the tracker must be a configured Jira or Redmine repo")
	}
	seen := map[string]bool{}
	list := []string{}
	for _, c := range code {
		if !seen[c] {
			seen[c] = true
			list = append(list, c)
		}
	}
	for _, c := range list {
		if !s.configured(c) || !isCodeRepo(c) {
			return provider.Errorf(400, "each code repo must be a configured GitHub or GitLab repo")
		}
	}
	labels := map[string]bool{}
	for _, c := range list {
		labels[codeLabel(c)] = true
	}
	if len(labels) != len(list) {
		return provider.Errorf(400, "two linked code repos share a name")
	}
	s.mu.Lock()
	if len(list) > 0 {
		s.linked[tracker] = list
	} else {
		delete(s.linked, tracker)
	}
	s.mu.Unlock()
	return s.saveLinks()
}

// saveLinks writes links.json with the current links.
func (s *state) saveLinks() error {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	b, err := json.Marshal(s.links())
	if err != nil {
		return err
	}
	return store.WriteAtomic(s.linksFile, b, 0o644)
}

// saveRepos writes repos.json with the current list.
func (s *state) saveRepos() error {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	b, err := json.Marshal(s.repos())
	if err != nil {
		return err
	}
	return store.WriteAtomic(s.reposFile, b, 0o644)
}

// signInOrigins are the sites of the configured repos of one provider: the only ones a person may sign in to.
func (s *state) signInOrigins(providerName string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, r := range s.repos() {
		es, ok := s.get(r)
		if !ok || es.Host.Provider() != providerName {
			continue
		}
		if o := es.Host.Origin(); !seen[o] {
			seen[o] = true
			out = append(out, o)
		}
	}
	return out
}

// accounts is the host of a provider at an origin, if one of the configured repos has it and it signs people in.
func (s *state) accounts(providerName, origin string) (provider.Accounts, bool) {
	for _, r := range s.repos() {
		es, ok := s.get(r)
		if !ok || es.Host.Provider() != providerName || es.Host.Origin() != origin {
			continue
		}
		if acc, ok := es.Host.(provider.Accounts); ok {
			return acc, true
		}
	}
	return nil, false
}

var githubPrefix = regexp.MustCompile(`^https?://[^/]+/`)

// siteOf is the site (origin) of a Redmine or Jira repo: the one whose secrets a plain secret may belong to. Other hosts
// have none, so "" for them.
func siteOf(repo string) string {
	switch providerOf(repo) {
	case "redmine":
		origin, _, _ := redmine.ParseRepo(repo)
		return origin
	case "jira":
		origin, _, _ := jira.ParseRepo(repo)
		return origin
	}
	return ""
}

// siteUsed is true when a configured repo already uses the site of repo.
func (s *state) siteUsed(repo string) bool {
	origin := siteOf(repo)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for key := range s.entries {
		if siteOf(key) == origin {
			return true
		}
	}
	return false
}

// repoKeyFromInput is the repo key of what the add-repo form sent (the POST /api/repos route's parsing).
func repoKeyFromInput(providerName, input string) (string, error) {
	source := strings.TrimSpace(input)
	switch providerName {
	case "jira":
		return jira.KeyFromURL(source)
	case "redmine":
		return redmine.KeyFromURL(source)
	case "gitlab":
		origin, _, project, _, err := gitlab.ParseRepo(source)
		if err != nil {
			return "", err
		}
		return "gitlab:" + origin + "/" + project, nil
	case "", "github":
		repo := githubPrefix.ReplaceAllString(source, "")
		repo = strings.TrimSuffix(repo, ".git")
		repo = strings.TrimRight(repo, "/")
		if !githubRepoKey.MatchString(repo) {
			return "", fmt.Errorf("Use the owner/repo format")
		}
		return repo, nil
	}
	return "", fmt.Errorf("Unsupported provider")
}

// addRepo adds a repo after its host confirms it exists and the server's credential can read it. A repo that is already
// configured keeps its place; its mode is set when mode is given.
func (s *state) addRepo(ctx context.Context, providerName, input, mode string) (string, bool, error) {
	if mode != "" && !validMode(mode) {
		return "", false, provider.Errorf(400, "%s", modeHelp)
	}
	repo, err := repoKeyFromInput(providerName, input)
	if err != nil {
		return "", false, provider.Errorf(400, "%s", err.Error())
	}
	if !validRepoKey(repo) {
		return "", false, provider.Errorf(400, "not a valid repo: %s", repo)
	}
	if s.configured(repo) {
		if mode != "" {
			if err := s.setMode(repo, mode); err != nil {
				return "", false, err
			}
		}
		return repo, false, nil
	}
	if mode == "" {
		mode = "read"
	}
	// A site that no configured repo uses yet is checked with its own secret only: adding a repo must not bind the plain
	// secret to it (only a sync of a configured repo does).
	siteOnly := siteOf(repo) != "" && !s.siteUsed(repo)
	host, err := s.buildHostFor(repo, siteOnly)
	if err != nil {
		return "", false, provider.Errorf(400, "%s", err.Error())
	}
	if err := host.Exists(ctx); err != nil {
		return "", false, provider.Errorf(400, "%s", err.Error())
	}
	if siteOnly {
		if host, err = s.buildHost(repo); err != nil {
			return "", false, provider.Errorf(400, "%s", err.Error())
		}
	}
	s.mu.RLock()
	first := len(s.order) == 0
	s.mu.RUnlock()
	file := s.storeFileFor(repo)
	if first && s.env("STORE_FILE") != "" {
		file = s.env("STORE_FILE")
	}
	es, err := openEntry(repo, host, file)
	if err != nil {
		return "", false, err
	}
	// The check above ran without the lock, so a concurrent add of the same repo may have won: then this one is not new.
	s.mu.Lock()
	if _, dup := s.entries[repo]; dup {
		s.mu.Unlock()
		if mode != "" {
			if err := s.setMode(repo, mode); err != nil {
				return "", false, err
			}
		}
		return repo, false, nil
	}
	s.entries[repo] = es
	s.order = append(s.order, repo)
	s.mu.Unlock()
	if err := s.saveRepos(); err != nil {
		return "", false, fmt.Errorf("saving repos.json: %w", err)
	}
	if err := s.setMode(repo, mode); err != nil {
		return "", false, err
	}
	s.runSync(repo, false)
	return repo, true, nil
}

// removeRepo removes a configured repo, its mode and any links naming it, and saves repos/links on disk.
func (s *state) removeRepo(repo string) ([]string, error) {
	s.mu.Lock()
	if _, ok := s.entries[repo]; !ok {
		s.mu.Unlock()
		return nil, provider.Errorf(404, "not configured: %s", repo)
	}
	delete(s.entries, repo)
	order := make([]string, 0, len(s.order))
	for _, r := range s.order {
		if r != repo {
			order = append(order, r)
		}
	}
	s.order = order
	linksChanged := false
	if _, ok := s.linked[repo]; ok {
		delete(s.linked, repo)
		linksChanged = true
	}
	for tracker, list := range s.linked {
		kept := list[:0]
		changed := false
		for _, c := range list {
			if c == repo {
				changed = true
				continue
			}
			kept = append(kept, c)
		}
		if changed {
			linksChanged = true
			if len(kept) == 0 {
				delete(s.linked, tracker)
			} else {
				s.linked[tracker] = append([]string{}, kept...)
			}
		}
	}
	s.mu.Unlock()
	if err := s.saveRepos(); err != nil {
		return nil, fmt.Errorf("saving repos.json: %w", err)
	}
	if linksChanged {
		if err := s.saveLinks(); err != nil {
			return nil, fmt.Errorf("saving links.json: %w", err)
		}
	}
	if err := s.modes.remove(repo); err != nil {
		return nil, fmt.Errorf("removing the mode of %s: %w", repo, err)
	}
	return s.repos(), nil
}
