package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/provider"
	"gitlab.com/system4us/issue-timeline/internal/store"
)

// The sync reads the project's issues and merge requests into the store. A full read is the first one, and one after
// a repo change or the full-sync interval; an incremental one reads what changed since the last sync, from two minutes
// before it (GitLab's clocks and the sync's are not the same).

const (
	syncMargin     = 2 * time.Minute
	issueWorkers   = 3
	pullWorkers    = 2
	linkWorkers    = 3
	maxSyncEntries = 50
	hierarchyPages = 500
	hierarchyQuery = `query ($path: ID!, $after: String) { project(fullPath: $path) { workItems(first: 100, after: $after, state: all) {
  pageInfo { hasNextPage endCursor } nodes { iid workItemType { name } widgets { __typename ... on WorkItemWidgetHierarchy {
    parent { iid title webUrl workItemType { name } } children(first: 100) { nodes { iid } } } } } } } }`
	defaultColor    = "b6c5d2"
	mergedState     = "merged"
	approvedDecided = "APPROVED"
)

type glLabel struct {
	Name  string  `json:"name"`
	Color *string `json:"color"`
}

type glRef struct {
	IID int `json:"iid"`
}

type glApprovals struct {
	ApprovedBy []struct {
		User       glUser  `json:"user"`
		ApprovedAt *string `json:"approved_at"`
	} `json:"approved_by"`
}

type glChanges struct {
	Changes []struct {
		Diff *string `json:"diff"`
	} `json:"changes"`
}

// Sync reads the repo into the store and saves it. It updates meta (repo, lastSync, fullSyncAt, totals, syncs). Nothing
// is held locked while the host is read.
func (r *Repo) Sync(ctx context.Context, st *store.Store, opt provider.SyncOptions) (store.SyncEntry, error) {
	logf := opt.Log
	if logf == nil {
		logf = func(string) {}
	}
	now := opt.Now
	if now == nil {
		now = time.Now
	}
	tok, err := r.serverToken(ctx)
	if err != nil {
		return store.SyncEntry{}, err
	}
	c := r.newClient(tok, true, logf)
	t0 := now()
	startedAt := isoMillis(t0)

	var meta store.Meta
	var open []string // the open issues' keys: their links are read again on an incremental sync
	st.View(func(d *store.Data) {
		meta = d.Meta
		for k, rec := range d.Issues {
			if rec.State == "OPEN" {
				open = append(open, k)
			}
		}
	})
	repoChanged := meta.Repo == nil || *meta.Repo != r.key
	doFull := opt.Full || meta.FullSyncAt == nil || repoChanged
	q := url.Values{"order_by": {"updated_at"}, "sort": {"desc"}}
	if !doFull {
		last, perr := time.Parse(time.RFC3339Nano, deref(meta.LastSync))
		if perr != nil {
			doFull = true
		} else {
			q.Set("updated_after", isoMillis(last.Add(-syncMargin)))
		}
	}
	progress := opt.Progress
	if progress == nil {
		progress = func(provider.Progress) {}
	}
	progress(provider.Progress{Phase: "Listing issues and MRs", Done: 0})

	mrq := url.Values{"state": {"all"}}
	for k, v := range q {
		mrq[k] = v
	}
	var rawIssues []glIssue
	var rawMRs []glMergeRequest
	var labels []glLabel
	err = runAll(ctx,
		func(ctx context.Context) (err error) {
			rawIssues, err = listPages[glIssue](ctx, c, r.projectPath("issues"), q)
			return err
		},
		func(ctx context.Context) (err error) {
			rawMRs, err = listPages[glMergeRequest](ctx, c, r.projectPath("merge_requests"), mrq)
			return err
		},
		func(ctx context.Context) (err error) {
			labels, err = listPages[glLabel](ctx, c, r.projectPath("labels"), nil)
			return err
		},
	)
	if err != nil {
		return store.SyncEntry{}, err
	}
	colors := labelColors(labels)
	total := len(rawIssues) + len(rawMRs)
	pg := &progressCounter{fn: progress, total: total}
	pg.report()

	issues := make([]*model.Record, len(rawIssues))
	err = forEachLimit(ctx, issueWorkers, len(rawIssues), func(ctx context.Context, i int) error {
		rec, err := r.loadIssue(ctx, c, rawIssues[i], colors)
		if err != nil {
			return err
		}
		issues[i] = rec
		pg.tick()
		return nil
	})
	if err != nil {
		return store.SyncEntry{}, err
	}
	pulls := make([]*model.Record, len(rawMRs))
	err = forEachLimit(ctx, pullWorkers, len(rawMRs), func(ctx context.Context, i int) error {
		rec, err := r.loadMergeRequest(ctx, c, rawMRs[i], colors)
		if err != nil {
			return err
		}
		pulls[i] = rec
		pg.tick()
		return nil
	})
	if err != nil {
		return store.SyncEntry{}, err
	}

	// GitLab's links are not in the issue's own record, and adding one does not always move its updated_at: on an
	// incremental sync the links of the open issues that did not come back are read again, so a new blocker shows up.
	linkRead := map[string]relations{}
	if !doFull {
		came := map[string]bool{}
		for _, ri := range rawIssues {
			came[strconv.Itoa(ri.IID)] = true
		}
		var todo []string
		for _, k := range open {
			if !came[k] {
				if _, err := provider.PathID(k, "an issue number"); err == nil {
					todo = append(todo, k)
				}
			}
		}
		results := make([]relations, len(todo))
		err = forEachLimit(ctx, linkWorkers, len(todo), func(ctx context.Context, i int) error {
			links, err := listOptional[glLink](ctx, c, r.projectPath("issues/"+todo[i]+"/links"), nil)
			if err != nil {
				return err
			}
			results[i] = relationsOf(links)
			return nil
		})
		if err != nil {
			return store.SyncEntry{}, err
		}
		for i, k := range todo {
			linkRead[k] = results[i]
		}
	}

	// Parents, children and epics come from the work items, not from the issue records: read them for every stored issue.
	// A failure leaves the hierarchy as it is, and the sync goes on.
	hier, herr := r.hierarchy(ctx, c)
	if herr != nil {
		logf(fmt.Sprintf("GitLab work items unavailable, hierarchy not updated: %v", herr))
		hier = nil
	}

	entry := store.SyncEntry{At: startedAt, Full: doFull, Issues: len(issues), Pulls: len(pulls)}
	st.Update(func(d *store.Data) {
		if d.Meta.Repo != nil && *d.Meta.Repo != r.key {
			d.Issues = map[string]*model.Record{}
			d.Pulls = map[string]*model.Record{}
		}
		for k, rel := range linkRead {
			old, ok := d.Issues[k]
			if !ok || old.State != "OPEN" {
				continue
			}
			c := *old
			c.BlockedBy, c.Blocking = rel.blockedBy, rel.blocking
			d.Issues[k] = &c
		}
		d.Upsert("issues", issues...)
		d.Upsert("pulls", pulls...)
		if hier != nil {
			applyHierarchy(d.Issues, hier)
		}
		key := r.key
		d.Meta.Repo = &key
		d.Meta.LastSync = &startedAt
		if doFull {
			d.Meta.FullSyncAt = &startedAt
		}
		d.Meta.Totals = &store.Totals{Issues: len(d.Issues), Pulls: len(d.Pulls)}
		entry.Ms = now().Sub(t0).Milliseconds()
		syncs := append(append([]store.SyncEntry{}, d.Meta.Syncs...), entry)
		if len(syncs) > maxSyncEntries {
			syncs = syncs[len(syncs)-maxSyncEntries:]
		}
		d.Meta.Syncs = syncs
	})
	if err := st.Save(); err != nil {
		return entry, fmt.Errorf("saving the GitLab store: %w", err)
	}
	kind := "incremental"
	if doFull {
		kind = "full"
	}
	logf(fmt.Sprintf("GitLab %s: %d issues, %d merge requests", kind, entry.Issues, entry.Pulls))
	return entry, nil
}

// loadIssue reads one issue with its notes, events, links.
func (r *Repo) loadIssue(ctx context.Context, c *client, raw glIssue, colors map[string]string) (*model.Record, error) {
	base := r.projectPath("issues/" + strconv.Itoa(raw.IID))
	var notes []glNote
	var states []glStateEvent
	var labelEvs []glLabelEvent
	var milestoneEvs []glMilestoneEvent
	var links []glLink
	err := runAll(ctx,
		func(ctx context.Context) (err error) {
			notes, err = listOptional[glNote](ctx, c, base+"/notes", nil)
			return err
		},
		func(ctx context.Context) (err error) {
			states, err = listOptional[glStateEvent](ctx, c, base+"/resource_state_events", nil)
			return err
		},
		func(ctx context.Context) (err error) {
			labelEvs, err = listOptional[glLabelEvent](ctx, c, base+"/resource_label_events", nil)
			return err
		},
		func(ctx context.Context) (err error) {
			milestoneEvs, err = listOptional[glMilestoneEvent](ctx, c, base+"/resource_milestone_events", nil)
			return err
		},
		func(ctx context.Context) (err error) {
			links, err = listOptional[glLink](ctx, c, base+"/links", nil)
			return err
		},
	)
	if err != nil {
		return nil, err
	}
	rel := relationsOf(links)
	comments := normalizeNotes(notes)
	return &model.Record{
		Kind: "issue", ID: strconv.Itoa(raw.ID), Key: strconv.Itoa(raw.IID), Number: model.Ptr(raw.IID),
		Title: raw.Title, Body: deref(raw.Description), URL: raw.WebURL, Author: userName(raw.Author),
		CreatedAt: raw.CreatedAt, UpdatedAt: raw.UpdatedAt, ClosedAt: raw.ClosedAt, DueDate: raw.DueDate,
		State: glState(raw.State), Labels: normalizeLabels(raw.Labels, colors), Assignees: assigneesOf(raw.Assignees, raw.Assignee),
		Milestone: milestoneOf(raw.Milestone), CommentsCount: commentCount(raw.UserNotesCount, comments), Comments: comments,
		Events: mappedEvents(states, labelEvs, milestoneEvs), Confidential: model.Ptr(raw.Confidential),
		BlockedBy: rel.blockedBy, Blocking: rel.blocking,
	}, nil
}

// loadMergeRequest reads one merge request with its notes, events, commits, approvals, the issues it closes, and its diff
// counts. The approvals and the changes are optional: when the host does not give them, the record has none.
func (r *Repo) loadMergeRequest(ctx context.Context, c *client, raw glMergeRequest, colors map[string]string) (*model.Record, error) {
	base := r.projectPath("merge_requests/" + strconv.Itoa(raw.IID))
	var notes []glNote
	var states []glStateEvent
	var labelEvs []glLabelEvent
	var commits []glCommit
	var approvals *glApprovals
	var closes []glRef
	var changes *glChanges
	err := runAll(ctx,
		func(ctx context.Context) (err error) {
			notes, err = listOptional[glNote](ctx, c, base+"/notes", nil)
			return err
		},
		func(ctx context.Context) (err error) {
			states, err = listOptional[glStateEvent](ctx, c, base+"/resource_state_events", nil)
			return err
		},
		func(ctx context.Context) (err error) {
			labelEvs, err = listOptional[glLabelEvent](ctx, c, base+"/resource_label_events", nil)
			return err
		},
		func(ctx context.Context) (err error) {
			commits, err = listOptional[glCommit](ctx, c, base+"/commits", nil)
			return err
		},
		func(ctx context.Context) error {
			approvals, _ = getOne[glApprovals](ctx, c, base+"/approvals", nil) // optional: nil when the host gives none
			return nil
		},
		func(ctx context.Context) (err error) {
			closes, err = listOptional[glRef](ctx, c, base+"/closes_issues", nil)
			return err
		},
		func(ctx context.Context) error {
			changes, _ = getOne[glChanges](ctx, c, base+"/changes", nil)
			return nil
		},
	)
	if err != nil {
		return nil, err
	}
	var diffs []struct {
		Diff *string `json:"diff"`
	}
	if changes != nil {
		diffs = changes.Changes
	}
	additions, deletions := 0, 0
	for _, f := range diffs {
		if f.Diff != nil {
			additions += countLines(*f.Diff, "+", "+++")
			deletions += countLines(*f.Diff, "-", "---")
		}
	}
	states = append([]glStateEvent{}, states...)
	if raw.MergedAt != nil && *raw.MergedAt != "" && !hasMergedEvent(states) {
		by := raw.MergeUser
		if by == nil {
			by = raw.MergedBy
		}
		states = append(states, glStateEvent{State: mergedState, CreatedAt: *raw.MergedAt, User: by})
	}
	reviews := []model.Review{}
	if approvals != nil {
		for _, a := range approvals.ApprovedBy {
			reviews = append(reviews, model.Review{Author: userName(&a.User), State: approvedDecided, At: a.ApprovedAt, Body: ""})
		}
	}
	var reviewDecision *string
	if len(reviews) > 0 {
		reviewDecision = model.Ptr(approvedDecided)
	}
	closesRefs := []model.Ref{}
	for _, cl := range closes {
		if cl.IID > 0 {
			closesRefs = append(closesRefs, model.RefOf(cl.IID))
		}
	}
	pullCommits := []model.PullCommit{}
	for _, cm := range commits {
		pullCommits = append(pullCommits, model.PullCommit{
			Oid: cm.ID, Message: deref(firstOf(cm.Title, cm.Message)), At: deref(firstOf(cm.CommittedDate, cm.CreatedAt)),
			Author: firstOf(userName(cm.Author), cm.AuthorName),
		})
	}
	notesOut := normalizeNotes(notes)
	return &model.Record{
		Kind: "pull", ID: strconv.Itoa(raw.ID), Key: strconv.Itoa(raw.IID), Number: model.Ptr(raw.IID),
		Title: raw.Title, Body: deref(raw.Description), URL: raw.WebURL, Author: userName(raw.Author),
		CreatedAt: raw.CreatedAt, UpdatedAt: raw.UpdatedAt, ClosedAt: raw.ClosedAt,
		State: glState(raw.State), Labels: normalizeLabels(raw.Labels, colors), Assignees: assigneesOf(raw.Assignees, raw.Assignee),
		Milestone: milestoneOf(raw.Milestone), CommentsCount: commentCount(raw.UserNotesCount, notesOut), Comments: notesOut,
		Events:  mappedEvents(states, labelEvs, nil),
		IsDraft: model.Ptr(isDraft(&raw)), MergedAt: raw.MergedAt, MergedBy: userName(firstUser(raw.MergeUser, raw.MergedBy)),
		ReviewDecision: reviewDecision, Base: raw.TargetBranch, Head: raw.SourceBranch,
		Additions: model.Ptr(additions), Deletions: model.Ptr(deletions), ChangedFiles: model.Ptr(len(diffs)),
		Closes: closesRefs, MergeCommitSha: firstOf(raw.MergeCommitSHA, raw.SquashCommitSHA),
		CommitsCount: model.Ptr(len(commits)), Commits: pullCommits, Reviews: reviews,
	}, nil
}

func hasMergedEvent(states []glStateEvent) bool {
	for _, e := range states {
		if e.State == mergedState {
			return true
		}
	}
	return false
}

// commentCount is the host's note count when it gives one, else the notes kept.
func commentCount(n *int, notes []model.Comment) int {
	if n != nil {
		return *n
	}
	return len(notes)
}

func firstUser(a, b *glUser) *glUser {
	if a != nil {
		return a
	}
	return b
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// hierEntry is one work item of the hierarchy: its type, its parent (one level up) and its children.
type hierEntry struct {
	typ      *string
	parent   *hierParent
	children []int
}

type hierParent struct {
	iid   int
	title string
	url   string
	typ   *string
}

// hierarchy reads the hierarchy of the project's work items (GitLab's issues, tasks and the epics above them), one
// GraphQL page of 100 at a time. An error when the server has no work items.
func (r *Repo) hierarchy(ctx context.Context, c *client) (map[int]hierEntry, error) {
	out := map[int]hierEntry{}
	var after *string
	for pages := 0; pages < hierarchyPages; pages++ {
		data, err := r.graphQL(ctx, c, hierarchyQuery, map[string]any{"path": r.project, "after": after})
		if err != nil {
			return nil, err
		}
		var body struct {
			Project *struct {
				WorkItems *struct {
					PageInfo struct {
						HasNextPage bool    `json:"hasNextPage"`
						EndCursor   *string `json:"endCursor"`
					} `json:"pageInfo"`
					Nodes []struct {
						IID          string `json:"iid"`
						WorkItemType *struct {
							Name *string `json:"name"`
						} `json:"workItemType"`
						Widgets []struct {
							Typename string `json:"__typename"`
							Parent   *struct {
								IID          string `json:"iid"`
								Title        string `json:"title"`
								WebURL       string `json:"webUrl"`
								WorkItemType *struct {
									Name *string `json:"name"`
								} `json:"workItemType"`
							} `json:"parent"`
							Children *struct {
								Nodes []struct {
									IID string `json:"iid"`
								} `json:"nodes"`
							} `json:"children"`
						} `json:"widgets"`
					} `json:"nodes"`
				} `json:"workItems"`
			} `json:"project"`
		}
		if err := json.Unmarshal(data, &body); err != nil {
			return nil, fmt.Errorf("reading the work items: %w", err)
		}
		if body.Project == nil || body.Project.WorkItems == nil {
			break
		}
		items := body.Project.WorkItems
		for _, n := range items.Nodes {
			iid, err := strconv.Atoi(n.IID)
			if err != nil {
				continue
			}
			var entry hierEntry
			if n.WorkItemType != nil {
				entry.typ = n.WorkItemType.Name
			}
			for _, w := range n.Widgets {
				if w.Typename != "WorkItemWidgetHierarchy" {
					continue
				}
				if p := w.Parent; p != nil {
					pid, _ := strconv.Atoi(p.IID)
					hp := &hierParent{iid: pid, title: p.Title, url: p.WebURL}
					if p.WorkItemType != nil {
						hp.typ = p.WorkItemType.Name
					}
					entry.parent = hp
				}
				if w.Children != nil {
					for _, ch := range w.Children.Nodes {
						if cid, err := strconv.Atoi(ch.IID); err == nil {
							entry.children = append(entry.children, cid)
						}
					}
				}
				break
			}
			out[iid] = entry
		}
		if !items.PageInfo.HasNextPage || items.PageInfo.EndCursor == nil {
			break
		}
		after = items.PageInfo.EndCursor
	}
	return out, nil
}

// applyHierarchy writes the hierarchy onto the stored issues, copy-on-write: a record whose parent, epic or children
// change is replaced by a copy. The parent is `parent` when it is an issue of this project, `parentEpic` when it is an
// epic (or anything else outside the issues), and `subIssues` are the children that are issues here.
func applyHierarchy(issues map[string]*model.Record, h map[int]hierEntry) {
	for key, rec := range issues {
		var entry hierEntry
		have := false
		if rec.Number != nil {
			entry, have = h[*rec.Number]
		}
		var p *hierParent
		if have {
			p = entry.parent
		}
		var wantParent *model.Ref
		var wantEpic *model.ParentEpic
		if p != nil {
			// an issue of this project is the parent unless it is an epic; anything else is the parent epic
			_, isIssueHere := issues[strconv.Itoa(p.iid)]
			if isIssueHere && (p.typ == nil || *p.typ != "Epic") {
				ref := model.RefOf(p.iid)
				wantParent = &ref
			} else {
				wantEpic = &model.ParentEpic{IID: p.iid, Title: p.title, URL: p.url, Type: p.typ}
			}
		}
		var wantSubs []model.Ref
		if have {
			for _, child := range entry.children {
				if _, ok := issues[strconv.Itoa(child)]; ok {
					wantSubs = append(wantSubs, model.RefOf(child))
				}
			}
		}
		if sameRefPtr(rec.Parent, wantParent) && sameEpic(rec.ParentEpic, wantEpic) && sameRefs(rec.SubIssues, wantSubs) {
			continue
		}
		c := *rec
		c.Parent = wantParent
		c.ParentEpic = wantEpic
		c.SubIssues = wantSubs
		issues[key] = &c
	}
}

func sameRefPtr(a, b *model.Ref) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func sameRefs(a, b []model.Ref) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sameEpic(a, b *model.ParentEpic) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.IID == b.IID && a.Title == b.Title && a.URL == b.URL && sameStrPtr(a.Type, b.Type)
}

func sameStrPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// graphQL posts a query to the site's GraphQL endpoint and returns its data.
// A query may be sent again after a 5xx; a mutation may not (it may have changed the work item already).
func (r *Repo) graphQL(ctx context.Context, c *client, query string, vars map[string]any) (json.RawMessage, error) {
	isMutation := strings.HasPrefix(strings.TrimSpace(query), "mutation")
	res, _, err := c.writeWith(ctx, "POST", r.origin+"/api/graphql", map[string]any{"query": query, "variables": vars}, false, !isMutation)
	if err != nil {
		return nil, err
	}
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(res.body, &env); err != nil {
		return nil, fmt.Errorf("reading the GraphQL answer: %w", err)
	}
	if len(env.Errors) > 0 {
		return nil, fmt.Errorf("GitLab GraphQL: %s", env.Errors[0].Message)
	}
	return env.Data, nil
}

// progressCounter counts the records read, from the workers, and reports each step.
type progressCounter struct {
	mu    sync.Mutex
	fn    func(provider.Progress)
	done  int
	total int
}

func (p *progressCounter) report() {
	p.mu.Lock()
	defer p.mu.Unlock()
	t := p.total
	p.fn(provider.Progress{Phase: "Downloading issues and MRs", Done: p.done, Total: &t})
}

func (p *progressCounter) tick() {
	p.mu.Lock()
	p.done++
	done, t := p.done, p.total
	p.mu.Unlock()
	p.fn(provider.Progress{Phase: "Downloading issues and MRs", Done: done, Total: &t})
}

// runAll runs every function at once and returns the first error; the others are cancelled.
func runAll(ctx context.Context, fns ...func(context.Context) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	var once sync.Once
	var first error
	for _, fn := range fns {
		wg.Add(1)
		go func(fn func(context.Context) error) {
			defer wg.Done()
			if err := fn(ctx); err != nil {
				once.Do(func() { first = err; cancel() })
			}
		}(fn)
	}
	wg.Wait()
	if first != nil {
		return first
	}
	return ctx.Err()
}

// forEachLimit runs fn for 0..n-1 with at most limit at a time, and stops at the first error.
func forEachLimit(ctx context.Context, limit, n int, fn func(ctx context.Context, i int) error) error {
	if n == 0 {
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan int)
	var wg sync.WaitGroup
	var once sync.Once
	var first error
	for w := 0; w < min(limit, n); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				if err := fn(ctx, i); err != nil {
					once.Do(func() { first = err; cancel() })
				}
			}
		}()
	}
feed:
	for i := 0; i < n; i++ {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	if first != nil {
		return first
	}
	return ctx.Err()
}
