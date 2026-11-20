package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"gitlab.com/system4us/issue-timeline/internal/ci"
	"gitlab.com/system4us/issue-timeline/internal/commits"
	"gitlab.com/system4us/issue-timeline/internal/derive"
	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/provider"
	"gitlab.com/system4us/issue-timeline/internal/store"
)

// editOptionsTTL is how long the edit form's lists are kept for the server's own identity.
const editOptionsTTL = 5 * time.Minute

// data is the payload of a repo for /api/data: the derived view of its store, its linked pulls for a tracker, the CI
// summary, and the sync status.
func (s *state) data(repo string, linked []string) (Data, error) {
	es, ok := s.get(repo)
	if !ok {
		return Data{}, provider.Errorf(400, "repo not configured")
	}
	if linked == nil {
		linked = []string{}
	}
	es.Store.RLock()
	src := &store.Data{Meta: es.Store.Data.Meta, Issues: es.Store.Data.Issues, Pulls: es.Store.Data.Pulls}
	var opts derive.LinkOptions
	if len(linked) > 0 {
		src.Pulls = s.linkedPulls(linked)
		opts = derive.LinkOptions{
			Native:   model.Ptr(false),
			KeyLinks: derive.DeriveKeyLinks(src.Issues, pullRecords(src.Pulls), derive.KeyPatternFor(repo)),
		}
	}
	payload := derive.BuildPayload(src, s.vocab, opts)
	var runs []model.CIRun
	var changes []model.WorkflowChange
	if es.CI != nil {
		runs = es.CI.Runs()
		changes = es.CI.Changes()
	}
	summary := ci.Summarize(runs, es.Store.Data.Pulls)
	es.Store.RUnlock()

	if summary.ByPr == nil {
		summary.ByPr = map[string]ci.PullCI{}
	}
	if summary.Other == nil {
		summary.Other = map[string]ci.OtherCI{}
	}
	if changes == nil {
		changes = []model.WorkflowChange{}
	}
	return Data{
		Payload: payload,
		Linked:  linked,
		Mode:    s.mode(repo),
		CI:      CI{Summary: summary, Changes: changes},
		Status:  es.statusCopy(),
	}, nil
}

// search finds the matches of q among the repo's issues and pulls (and the linked pulls).
func (s *state) search(repo string, linked []string, q string, v provider.Visibility) []derive.SearchResult {
	es, ok := s.get(repo)
	if !ok {
		return nil
	}
	es.Store.RLock()
	recs := make([]*model.Record, 0, len(es.Store.Data.Issues)+len(es.Store.Data.Pulls))
	for _, r := range es.Store.Data.Issues {
		recs = append(recs, r)
	}
	for _, r := range es.Store.Data.Pulls {
		recs = append(recs, r)
	}
	if len(linked) > 0 {
		recs = append(recs, pullRecords(s.linkedPulls(linked))...)
	}
	es.Store.RUnlock()
	// what the person may not see is taken out before the search: a hidden issue cannot match, a hidden note cannot
	// be the snippet
	visible := recs[:0:0]
	for _, r := range recs {
		if r.Kind == "pull" && v.HideCode {
			continue
		}
		if vr, ok := visibleRecord(r, v); ok {
			visible = append(visible, vr)
		}
	}
	return derive.Search(visible, q, 0)
}

// record is one issue, or one pull (a "label#N" key is a pull of a linked code repo).
func (s *state) record(repo string, linked []string, kind, key string) (*model.Record, bool) {
	if kind == "pull" {
		hit, ok := s.findPull(repo, key, linked)
		return hit.record, ok
	}
	es, ok := s.get(repo)
	if !ok {
		return nil, false
	}
	es.Store.RLock()
	rec := es.Store.Data.Issues[key]
	es.Store.RUnlock()
	return rec, rec != nil
}

// bagOf is the records of one kind in a store's data.
func bagOf(d *store.Data, kind string) map[string]*model.Record {
	return d.Bag(kind)
}

// throttled is true when the record was read less than the throttle ago: opening many in a row costs one read each.
func (s *state) throttled(rec *model.Record) bool {
	if rec.ReadAt == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339Nano, rec.ReadAt)
	return err == nil && time.Since(t) < s.throttle
}

// refresh reads one record again from its host, when it was not read in the last throttle period, and keeps it.
func (s *state) refresh(ctx context.Context, repo, kind, key string) (Refreshed, error) {
	es, ok := s.get(repo)
	if !ok {
		return Refreshed{}, provider.Errorf(400, "repo not configured")
	}
	if providerOf(repo) == "jira" {
		return Refreshed{}, provider.Errorf(501, "reading one Jira issue again is not built yet")
	}
	if kind != "pull" {
		kind = "issue"
	}
	bagKind := "issues"
	if kind == "pull" {
		bagKind = "pulls"
	}
	es.Store.RLock()
	rec := bagOf(es.Store.Data, bagKind)[key]
	es.Store.RUnlock()
	if rec == nil {
		return Refreshed{}, provider.Errorf(404, "not found")
	}
	if s.throttled(rec) {
		return Refreshed{Record: rec, Throttled: true}, nil
	}
	fresh, err := es.Host.Refresh(ctx, kind, rec)
	if errors.Is(err, provider.ErrNotFound) {
		return Refreshed{}, provider.Errorf(404, "%s does not show this record any more", providerName(repo))
	}
	if err != nil {
		return Refreshed{}, err
	}
	if fresh == nil {
		return Refreshed{}, fmt.Errorf("the host gave no %s for %s", kind, key)
	}
	// A version's own date is not in the host's milestone of the record: the one the store has stays while the title is
	// the same.
	if fresh.Milestone != nil && rec.Milestone != nil && rec.Milestone.Title == fresh.Milestone.Title {
		m := *fresh.Milestone
		if rec.Milestone.DueOn != nil {
			m.DueOn = rec.Milestone.DueOn
		}
		c := *fresh
		c.Milestone = &m
		fresh = &c
	}
	fields := changedFields(rec, fresh)
	c := *fresh
	c.Key = key
	es.Store.Lock()
	es.Store.Data.Upsert(bagKind, &c)
	es.Store.Unlock()
	if err := es.Store.Save(); err != nil {
		return Refreshed{}, fmt.Errorf("saving %s: %w", repo, err)
	}
	es.Store.RLock()
	stored := bagOf(es.Store.Data, bagKind)[key]
	es.Store.RUnlock()
	return Refreshed{Record: stored, Changed: len(fields) > 0, Fields: fields}, nil
}

// providerName is the name of a repo's host, for the messages that name it.
func providerName(repo string) string {
	switch providerOf(repo) {
	case "redmine":
		return "Redmine"
	case "jira":
		return "Jira"
	case "gitlab":
		return "GitLab"
	}
	return "GitHub"
}

// changedFields are the top-level fields of a record that differ between two copies (compared as JSON, the read time
// aside), sorted by name.
func changedFields(before, after *model.Record) []string {
	a := jsonFields(before)
	b := jsonFields(after)
	names := map[string]bool{}
	for k := range a {
		names[k] = true
	}
	for k := range b {
		names[k] = true
	}
	out := []string{}
	for k := range names {
		if k == "readAt" {
			continue
		}
		if string(orNull(a[k])) != string(orNull(b[k])) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func jsonFields(r *model.Record) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	b, err := json.Marshal(r)
	if err != nil {
		return out
	}
	_ = json.Unmarshal(b, &out)
	return out
}

func orNull(v json.RawMessage) json.RawMessage {
	if v == nil {
		return json.RawMessage("null")
	}
	return v
}

// commits is the list of the commits of a repo, filtered and paged. A tracker shows the commits of its linked code
// repos, with the pull request of the "pr" key (its own commits, even those never on the default branch) added.
func (s *state) commits(repo string, linked []string, q CommitQuery) (CommitList, error) {
	es, ok := s.get(repo)
	if !ok {
		return CommitList{}, provider.Errorf(400, "repo not configured")
	}
	f := commitFilter{
		q:      strings.ToLower(strings.TrimSpace(q.Q)),
		author: strings.ToLower(strings.TrimSpace(q.Author)),
		pr:     q.PR,
		issue:  q.Issue,
	}
	limit := q.Limit
	if limit == 0 {
		limit = 100
	}
	limit = min(500, max(1, limit))
	offset := max(0, q.Offset)

	if len(linked) > 0 {
		items := s.linkedCommits(repo, linked)
		known := map[string]bool{}
		for _, c := range items {
			known[c.Sha] = true
		}
		var own []CommitRow
		if f.pr != "" {
			if hit, ok := s.findPull(repo, f.pr, linked); ok {
				var pcs []model.Commit
				for _, c := range hit.record.Commits {
					if !known[c.Oid] {
						at := c.At
						pcs = append(pcs, model.Commit{Sha: c.Oid, Title: c.Message, Author: c.Author, At: &at})
					}
				}
				own = s.linkCommitsOf(repo, hit.codeRepo, pcs)
				for i := range own {
					own[i].PR = strPtr(f.pr)
				}
			}
		}
		all := append(items, own...)
		matches := []CommitRow{}
		for _, c := range all {
			c.InHistory = known[c.Sha]
			if f.match(c) {
				matches = append(matches, c)
			}
		}
		return CommitList{Total: len(matches), Synced: len(items), LastSync: s.linkedLastSync(linked), Items: page(matches, offset, limit)}, nil
	}

	if es.Commits == nil { // a tracker with no code repo linked: no commits
		return CommitList{Items: []CommitRow{}}, nil
	}
	items := es.Commits.Items()
	known := map[string]bool{}
	for _, c := range items {
		known[c.Sha] = true
	}
	es.Store.RLock()
	pulls := es.Store.Data.Pulls
	issues := es.Store.Data.Issues
	var own []model.Commit
	if p, ok := pulls[f.pr]; ok && f.pr != "" {
		for _, c := range p.Commits {
			if !known[c.Oid] {
				at := c.At
				own = append(own, model.Commit{Sha: c.Oid, Title: c.Message, Author: c.Author, At: &at})
			}
		}
	}
	all := append(append([]model.Commit{}, items...), own...)
	links := derive.BuildCommitLinks(all, pulls, issues)
	es.Store.RUnlock()

	matches := []CommitRow{}
	for _, c := range all {
		row := rowOf(c, links[c.Sha])
		row.InHistory = known[c.Sha]
		if f.match(row) {
			matches = append(matches, row)
		}
	}
	return CommitList{Total: len(matches), Synced: len(items), LastSync: es.Commits.LastSync(), Items: page(matches, offset, limit)}, nil
}

// commitFilter is the commit list's filter, with the query already trimmed and lowercased.
type commitFilter struct {
	q, author, pr, issue string
}

// match is true when a row passes the filter: its title or sha has the query, its author the author, its pull request
// is pr, and it names the issue.
func (f commitFilter) match(c CommitRow) bool {
	if f.q != "" && !strings.Contains(strings.ToLower(c.Title), f.q) && !strings.HasPrefix(c.Sha, f.q) {
		return false
	}
	if f.author != "" && !strings.Contains(strings.ToLower(deref(c.Author)), f.author) {
		return false
	}
	if f.pr != "" && deref(c.PR) != f.pr {
		return false
	}
	if f.issue != "" && !containsString(c.Issues, f.issue) {
		return false
	}
	return true
}

func containsString(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// rowOf is a stored commit with what it belongs to (no issues is an empty list, never nil).
func rowOf(c model.Commit, link derive.CommitLink) CommitRow {
	issues := link.Issues
	if issues == nil {
		issues = []string{}
	}
	return CommitRow{Commit: c, PR: link.PR, Issues: issues}
}

// page is the rows from offset, at most limit (never nil).
func page(rows []CommitRow, offset, limit int) []CommitRow {
	if offset >= len(rows) {
		return []CommitRow{}
	}
	end := min(len(rows), offset+limit)
	return append([]CommitRow{}, rows[offset:end]...)
}

// commit is one commit with its files: the diff cache, filled from the host the first time. Any commit of the repo
// works, not only the default branch's. A linked tracker's commit is read from the code repo that has it.
func (s *state) commit(ctx context.Context, repo string, linked []string, sha string) (CommitDetail, error) {
	sha = strings.ToLower(sha)
	codeRepo := repo
	if isTracker(repo) {
		if len(linked) == 0 {
			return CommitDetail{}, provider.Errorf(404, "a tracker has no commits until a code repo is linked to it")
		}
		codeRepo = linked[0]
		for _, c := range linked {
			if es, ok := s.get(c); ok && es.Commits != nil && es.Commits.Has(sha) {
				codeRepo = c
				break
			}
		}
	}
	ec, ok := s.get(codeRepo)
	if !ok || ec.Commits == nil {
		return CommitDetail{}, provider.Errorf(400, "repo not configured")
	}
	known, isKnown := ec.Commits.Get(sha)
	if !isKnown && !commits.FullSHA.MatchString(sha) {
		return CommitDetail{}, provider.Errorf(404, "not a full commit sha of this repo")
	}
	dir := commits.DiffDirFor(s.storeDir, codeRepo)
	cached, err := commits.ReadCachedDiff(dir, sha)
	if err != nil {
		return CommitDetail{}, err
	}
	if cached == nil {
		ch, ok := ec.Host.(provider.CodeHost)
		if !ok {
			return CommitDetail{}, provider.Errorf(501, "%s has no commits to read", codeRepo)
		}
		info, files, err := ch.CommitFiles(ctx, sha)
		if err != nil {
			return CommitDetail{}, err
		}
		// A sha the repo does not know that the host does not show is not a commit: it is not cached, so a later
		// request asks the host again.
		if !isKnown && info == nil && len(files) == 0 {
			return CommitDetail{}, fmt.Errorf("commit %s of %s: %w", sha, codeRepo, provider.ErrNotFound)
		}
		out, truncated := commits.FinishFiles(files)
		cached = &commits.CachedDiff{Info: info, Files: out, Truncated: truncated}
		if err := commits.WriteCachedDiff(dir, sha, cached); err != nil {
			return CommitDetail{}, fmt.Errorf("caching the diff of %s: %w", sha, err)
		}
	}

	var title string
	var author, at *string
	var additions, deletions *int
	switch {
	case isKnown:
		title, author, at, additions, deletions = known.Title, known.Author, known.At, known.Additions, known.Deletions
	case cached.Info != nil:
		title, author, at = cached.Info.Title, cached.Info.Author, cached.Info.At
	}
	c := model.Commit{Sha: sha, Title: title, Author: author, At: at, Additions: additions, Deletions: deletions}

	var row CommitRow
	if isTracker(repo) {
		row = s.linkCommitsOf(repo, codeRepo, []model.Commit{{Sha: sha, Title: title}})[0]
		row.Repo = ""
		row.Commit = c
		row.InHistory = isKnown
	} else {
		es, _ := s.get(repo)
		es.Store.RLock()
		link := derive.BuildCommitLinks([]model.Commit{{Sha: sha, Title: title}}, es.Store.Data.Pulls, es.Store.Data.Issues)[sha]
		es.Store.RUnlock()
		row = rowOf(c, link)
		row.InHistory = isKnown
	}
	files := cached.Files
	if files == nil {
		files = []commits.File{}
	}
	return CommitDetail{CommitRow: row, Files: files, Truncated: cached.Truncated}, nil
}

// mergeMethod is how a merged pull landed ("" when it is not merged or the host cannot tell). Asked of the host once
// per pull, then kept in memory.
func (s *state) mergeMethod(ctx context.Context, repo string, linked []string, key string) (string, error) {
	hit, ok := s.findPull(repo, key, linked)
	if !ok {
		return "", provider.Errorf(404, "not found")
	}
	if hit.record.MergedAt == nil {
		return "", nil
	}
	ck := hit.codeRepo + "#" + strconv.Itoa(hit.number)
	s.mergeMu.Lock()
	method, ok := s.merges[ck]
	s.mergeMu.Unlock()
	if ok {
		return method, nil
	}
	ec, ok := s.get(hit.codeRepo)
	if !ok {
		return "", provider.Errorf(400, "repo not configured")
	}
	ch, ok := ec.Host.(provider.CodeHost)
	if !ok {
		return "", provider.Errorf(501, "%s has no merge methods", hit.codeRepo)
	}
	rec := *hit.record
	if rec.Number == nil {
		n := hit.number
		rec.Number = &n
	}
	method, err := ch.MergeMethod(ctx, &rec)
	if err != nil {
		return "", err
	}
	if method != "" {
		s.mergeMu.Lock()
		s.merges[ck] = method
		s.mergeMu.Unlock()
	}
	return method, nil
}

// editOptions are the lists of a repo's edit form, read with cred. The lists are kept five minutes only when cached is
// true (the server's own identity: what a person sees in the form depends on who they are).
func (s *state) editOptions(ctx context.Context, repo string, cred provider.Cred, cached bool) (provider.EditOptions, error) {
	es, ok := s.get(repo)
	if !ok {
		return provider.EditOptions{}, provider.Errorf(400, "repo not configured")
	}
	ed, ok := es.Host.(provider.Editor)
	if !ok {
		return provider.EditOptions{}, provider.Errorf(501, "edit options are not built for this repo")
	}
	if cached {
		s.optsMu.Lock()
		hit, ok := s.opts[repo]
		s.optsMu.Unlock()
		if ok && time.Since(hit.at) < editOptionsTTL {
			return hit.value, nil
		}
	}
	value, err := ed.EditOptions(ctx, cred)
	if err != nil {
		return provider.EditOptions{}, err
	}
	if cached {
		s.optsMu.Lock()
		s.opts[repo] = editOptsHit{at: time.Now(), value: value}
		s.optsMu.Unlock()
	}
	return value, nil
}
