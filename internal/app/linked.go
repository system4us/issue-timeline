package app

import (
	"sort"
	"strconv"
	"strings"

	"gitlab.com/system4us/issue-timeline/internal/derive"
	"gitlab.com/system4us/issue-timeline/internal/model"
)

// The links between a tracker and the code repos linked to it: their pulls keyed "label#N", their commits, and the
// tracker's issues the commits and pulls name by key. Each function takes the store locks it reads under, one store at
// a time, and holds none across another call.

// linkedPulls are the pulls of a tracker's linked code repos (those in linked), keyed "label#N", each with its repo.
func (s *state) linkedPulls(linked []string) map[string]*model.Record {
	out := map[string]*model.Record{}
	for _, code := range linked {
		es, ok := s.get(code)
		if !ok {
			continue
		}
		label := codeLabel(code)
		es.Store.RLock()
		for _, p := range es.Store.Data.Pulls {
			if p == nil {
				continue
			}
			c := *p
			c.Key = label + "#" + p.Key
			c.Repo = code
			out[c.Key] = &c
		}
		es.Store.RUnlock()
	}
	return out
}

// pullHit is a pull found by findPull: the record (for a linked pull, a copy keyed "label#N"), the code repo it
// belongs to, and its number there.
type pullHit struct {
	record   *model.Record
	codeRepo string
	number   int
}

// findPull finds a pull by its key: a plain key is a pull of the repo asked for; "label#N" is a pull of one of the
// tracker's linked code repos.
func (s *state) findPull(repo, key string, linked []string) (pullHit, bool) {
	if hash := strings.LastIndex(key, "#"); hash > 0 {
		label := key[:hash]
		code := ""
		for _, c := range linked {
			if codeLabel(c) == label {
				code = c
				break
			}
		}
		if code == "" {
			return pullHit{}, false
		}
		number, err := strconv.Atoi(key[hash+1:])
		if err != nil {
			return pullHit{}, false
		}
		es, ok := s.get(code)
		if !ok {
			return pullHit{}, false
		}
		es.Store.RLock()
		rec := es.Store.Data.Pulls[strconv.Itoa(number)]
		es.Store.RUnlock()
		if rec == nil {
			return pullHit{}, false
		}
		c := *rec
		c.Key = key
		c.Repo = code
		return pullHit{record: &c, codeRepo: code, number: number}, true
	}
	es, ok := s.get(repo)
	if !ok {
		return pullHit{}, false
	}
	es.Store.RLock()
	rec := es.Store.Data.Pulls[key]
	es.Store.RUnlock()
	if rec == nil {
		return pullHit{}, false
	}
	number, _ := strconv.Atoi(key)
	return pullHit{record: rec, codeRepo: repo, number: number}, true
}

// linkCommitsOf is the commits of a code repo as a tracker linked to it sees them: its PR as "label#N", and the
// tracker's issues its message names by key. The code repo's own "#N" links only to its own pulls.
func (s *state) linkCommitsOf(tracker, code string, cs []model.Commit) []CommitRow {
	ec, _ := s.get(code)
	et, _ := s.get(tracker)
	label := codeLabel(code)

	ec.Store.RLock()
	native := derive.BuildCommitLinks(cs, ec.Store.Data.Pulls, map[string]*model.Record{})
	ec.Store.RUnlock()

	pattern := derive.KeyPatternFor(tracker)
	trackerKeys := map[string]bool{}
	if pattern != nil {
		et.Store.RLock()
		for _, c := range cs {
			for _, k := range pattern.FindAll(c.Title) {
				if _, ok := et.Store.Data.Issues[k]; ok {
					trackerKeys[k] = true
				}
			}
		}
		et.Store.RUnlock()
	}

	rows := make([]CommitRow, 0, len(cs))
	for _, c := range cs {
		var keys []string
		if pattern != nil {
			for _, k := range pattern.FindAll(c.Title) {
				if trackerKeys[k] {
					keys = append(keys, k)
				}
			}
		}
		var pr *string
		if link, ok := native[c.Sha]; ok && link.PR != nil {
			p := label + "#" + *link.PR
			pr = &p
		}
		rows = append(rows, CommitRow{Commit: c, Repo: code, PR: pr, Issues: uniqueSortedKeys(keys)})
	}
	return rows
}

// linkedCommits are every commit of a tracker's linked code repos, once each (the first repo that has it wins).
func (s *state) linkedCommits(tracker string, linked []string) []CommitRow {
	seen := map[string]bool{}
	out := []CommitRow{}
	for _, code := range linked {
		es, ok := s.get(code)
		if !ok || es.Commits == nil {
			continue
		}
		for _, row := range s.linkCommitsOf(tracker, code, es.Commits.Items()) {
			if !seen[row.Sha] {
				seen[row.Sha] = true
				out = append(out, row)
			}
		}
	}
	return out
}

// linkedLastSync is the latest commit sync of the linked code repos, nil when none has synced.
func (s *state) linkedLastSync(linked []string) *string {
	var latest *string
	for _, code := range linked {
		es, ok := s.get(code)
		if !ok || es.Commits == nil {
			continue
		}
		if t := es.Commits.LastSync(); t != nil && (latest == nil || *t > *latest) {
			latest = t
		}
	}
	return latest
}

// uniqueSortedKeys is the keys once each, in ByKey order (never nil).
func uniqueSortedKeys(keys []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, k := range keys {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return derive.ByKey(out[i], out[j]) < 0 })
	return out
}

// pullRecords is the values of a pull map.
func pullRecords(m map[string]*model.Record) []*model.Record {
	out := make([]*model.Record, 0, len(m))
	for _, p := range m {
		out = append(out, p)
	}
	return out
}
