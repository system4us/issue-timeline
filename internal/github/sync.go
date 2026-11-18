package github

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/provider"
	"gitlab.com/system4us/issue-timeline/internal/store"
)

// syncMargin re-reads a little before the last sync: clocks and eventual consistency.
const syncMargin = 2 * time.Minute

// syncSyncs is how many syncs meta.syncs keeps.
const syncSyncs = 50

// Each state is its own chain of cursors (newest-updated first), so the chains can be read at the same time. A page
// costs GitHub several seconds, and the pages of one chain cannot overlap, but those of different chains can.
var (
	issueStates = [][]string{{"OPEN"}, {"CLOSED"}}
	pullStates  = [][]string{{"OPEN"}, {"MERGED"}, {"CLOSED"}}
)

// syncEnv is what a sync reads with: the host, the server's token and the repo.
type syncEnv struct {
	api         *api
	endpoint    string
	token       string
	owner, name string
}

// Sync reads the repo into the store: every issue and pull request, with their history. The first run (or a full one)
// walks everything; a later one walks newest-updated first and stops at the first page older than the last sync. It
// updates meta (repo, lastSync, fullSyncAt, totals, syncs) and saves the store.
func (r *Repo) Sync(ctx context.Context, st *store.Store, opt provider.SyncOptions) (store.SyncEntry, error) {
	entry, err := r.sync(ctx, st, opt)
	return entry, asProvider(err)
}

func (r *Repo) sync(ctx context.Context, st *store.Store, opt provider.SyncOptions) (store.SyncEntry, error) {
	now := opt.Now
	if now == nil {
		now = time.Now
	}
	t0 := now()
	startedAt := t0.UTC().Format(isoMillis)
	repo := r.Key()

	// the store's meta decides whether this is a full read; a store of another repo starts again
	var doFull bool
	var since time.Time
	hasSince := false
	st.Update(func(d *store.Data) {
		m := &d.Meta
		doFull = opt.Full || m.FullSyncAt == nil || m.Repo == nil || *m.Repo != repo
		if m.Repo != nil && *m.Repo != repo {
			d.Issues = map[string]*model.Record{}
			d.Pulls = map[string]*model.Record{}
		}
		if !doFull && m.LastSync != nil {
			if t, err := time.Parse(time.RFC3339, *m.LastSync); err == nil {
				since, hasSince = t, true
			}
		}
	})

	tok, err := r.serverToken(ctx)
	if err != nil {
		return store.SyncEntry{}, err
	}
	env := &syncEnv{api: r.api, endpoint: r.o.Endpoints.GraphQL, token: tok, owner: r.owner, name: r.name}
	var sinceP *time.Time
	if hasSince {
		sinceP = &since
	}
	progress := newCounter(opt.Progress)

	var issues, pulls []*model.Record
	var issuesTotal, pullsTotal int
	err = runAll(ctx, []func(context.Context) error{
		func(ctx context.Context) error {
			nodes, total, err := readChains[issueNode](ctx, env, issueStates, issuesQuery, "issues", "Issue", sinceP, func(n *issueNode) string { return n.UpdatedAt }, env.completeIssue, progress.inc)
			if err != nil {
				return fmt.Errorf("reading the issues of %s: %w", repo, err)
			}
			for _, n := range nodes {
				issues = append(issues, normalizeIssue(n))
			}
			issuesTotal = total
			return nil
		},
		func(ctx context.Context) error {
			nodes, total, err := readChains[pullNode](ctx, env, pullStates, pullsQuery, "pullRequests", "PullRequest", sinceP, func(n *pullNode) string { return n.UpdatedAt }, env.completePull, progress.inc)
			if err != nil {
				return fmt.Errorf("reading the pull requests of %s: %w", repo, err)
			}
			for _, n := range nodes {
				pulls = append(pulls, normalizePull(n))
			}
			pullsTotal = total
			return nil
		},
	})
	if err != nil {
		return store.SyncEntry{}, err
	}

	entry := store.SyncEntry{At: startedAt, Full: doFull, Issues: len(issues), Pulls: len(pulls), Ms: now().Sub(t0).Milliseconds()}
	st.Update(func(d *store.Data) {
		d.Upsert("issues", issues...)
		d.Upsert("pulls", pulls...)
		m := &d.Meta
		repoKey := repo
		m.Repo = &repoKey
		last := startedAt
		m.LastSync = &last
		if doFull {
			full := startedAt
			m.FullSyncAt = &full
		}
		m.Totals = &store.Totals{Issues: issuesTotal, Pulls: pullsTotal}
		syncs := append(append([]store.SyncEntry{}, m.Syncs...), entry)
		if len(syncs) > syncSyncs {
			syncs = syncs[len(syncs)-syncSyncs:]
		}
		m.Syncs = syncs
	})
	if err := st.Save(); err != nil {
		return store.SyncEntry{}, fmt.Errorf("saving %s: %w", repo, err)
	}
	if opt.Log != nil {
		kind := "incremental"
		if doFull {
			kind = "full"
		}
		opt.Log(fmt.Sprintf("sync %s: %d issues, %d pulls in %d ms", kind, entry.Issues, entry.Pulls, entry.Ms))
	}
	return entry, nil
}

// counter reports how many items a sync has read, from any of its goroutines.
type counter struct {
	mu       sync.Mutex
	done     int
	progress func(provider.Progress)
}

func newCounter(progress func(provider.Progress)) *counter {
	return &counter{progress: progress}
}

func (c *counter) inc() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.done++
	if c.progress != nil {
		c.progress(provider.Progress{Phase: "Issues and pull requests", Done: c.done, Total: nil})
	}
}

// runAll runs the functions at the same time and waits for all of them. The first error cancels the others and is
// returned.
func runAll(ctx context.Context, fns []func(context.Context) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wg    sync.WaitGroup
		once  sync.Once
		first error
	)
	for _, fn := range fns {
		wg.Add(1)
		go func(fn func(context.Context) error) {
			defer wg.Done()
			if err := fn(ctx); err != nil {
				once.Do(func() {
					first = err
					cancel()
				})
			}
		}(fn)
	}
	wg.Wait()
	return first
}

// chainOut is what one chain read: its nodes (newest first) and the total of its state.
type chainOut[N any] struct {
	nodes []*N
	total int
}

// readChains reads every state's chain at the same time, and joins them.
func readChains[N any](ctx context.Context, env *syncEnv, states [][]string, query, root, typename string, since *time.Time, updated func(*N) string, complete func(context.Context, *N) error, onItem func()) ([]*N, int, error) {
	outs := make([]chainOut[N], len(states))
	fns := make([]func(context.Context) error, len(states))
	for i, st := range states {
		fns[i] = func(ctx context.Context) error {
			nodes, total, err := walkChain(ctx, env, query, root, st, since, updated, complete, onItem)
			outs[i] = chainOut[N]{nodes: nodes, total: total}
			return err
		}
	}
	if err := runAll(ctx, fns); err != nil {
		return nil, 0, err
	}
	var all []*N
	total := 0
	for _, o := range outs {
		all = append(all, o.nodes...)
		total += o.total
	}
	return all, total, nil
}

// walkChain reads one state's pages, newest updated first. It stops at the first node updated before since (less the
// margin), or at the last page. It returns the nodes (completed) and the total the last page gave.
func walkChain[N any](ctx context.Context, env *syncEnv, query, root string, states []string, since *time.Time, updated func(*N) string, complete func(context.Context, *N) error, onItem func()) ([]*N, int, error) {
	var out []*N
	var after any // nil for the first page: null in GraphQL
	for {
		raw, err := env.api.graphql(ctx, env.endpoint, env.token, query, map[string]any{"owner": env.owner, "name": env.name, "after": after, "states": states})
		if err != nil {
			return nil, 0, err
		}
		var data struct {
			Repository map[string]json.RawMessage `json:"repository"`
		}
		if err := json.Unmarshal(raw, &data); err != nil {
			return nil, 0, fmt.Errorf("reading the page: %w", err)
		}
		if data.Repository == nil {
			return nil, 0, fmt.Errorf("%w: GitHub repository %s/%s, or no access to it", provider.ErrNotFound, env.owner, env.name)
		}
		var page conn[N]
		if rawPage := data.Repository[root]; hasData(rawPage) {
			if err := json.Unmarshal(rawPage, &page); err != nil {
				return nil, 0, fmt.Errorf("reading the page: %w", err)
			}
		}
		reachedOld := false
		for _, n := range page.Nodes {
			if n == nil {
				continue
			}
			if since != nil && olderThan(updated(n), *since) {
				reachedOld = true // newest first: everything after this one is older too
				break
			}
			if err := complete(ctx, n); err != nil {
				return nil, 0, err
			}
			out = append(out, n)
			onItem()
		}
		if reachedOld || !page.PageInfo.HasNextPage {
			return out, page.TotalCount, nil
		}
		after = page.PageInfo.EndCursor
	}
}

// olderThan reports whether a node updated at updatedAt is older than since (less the margin). A time that does not
// parse is not older: it is read.
func olderThan(updatedAt string, since time.Time) bool {
	t, err := time.Parse(time.RFC3339, updatedAt)
	return err == nil && t.Before(since.Add(-syncMargin))
}

// drainConn reads the rest of a connection a node had more of than one page of.
func drainConn[T any](ctx context.Context, env *syncEnv, typename, id, name, args, sel string, c *conn[T]) error {
	if c == nil || !c.PageInfo.HasNextPage {
		return nil
	}
	after := c.PageInfo.EndCursor
	for more := true; more; {
		q := fmt.Sprintf(drainQuery, typename, name, argsSuffix(args), sel)
		raw, err := env.api.graphql(ctx, env.endpoint, env.token, q, map[string]any{"id": id, "after": after})
		if err != nil {
			return err
		}
		var data struct {
			Node map[string]json.RawMessage `json:"node"`
		}
		if err := json.Unmarshal(raw, &data); err != nil {
			return fmt.Errorf("reading more of %s: %w", name, err)
		}
		if data.Node == nil {
			return fmt.Errorf("reading more of %s: GitHub did not return the node", name)
		}
		var page conn[T]
		if err := json.Unmarshal(data.Node[name], &page); err != nil {
			return fmt.Errorf("reading more of %s: %w", name, err)
		}
		c.Nodes = append(c.Nodes, page.Nodes...)
		more = page.PageInfo.HasNextPage
		after = page.PageInfo.EndCursor
	}
	return nil
}

// completeIssue reads the connections of an issue that had more than one page.
func (e *syncEnv) completeIssue(ctx context.Context, n *issueNode) error {
	if err := drainConn(ctx, e, "Issue", n.ID, "comments", "", commentSel, n.Comments); err != nil {
		return err
	}
	return drainConn(ctx, e, "Issue", n.ID, "timelineItems", issueItemArgs, issueEventSel, n.Timeline)
}

// completePull reads the connections of a pull request that had more than one page.
func (e *syncEnv) completePull(ctx context.Context, n *pullNode) error {
	if err := drainConn(ctx, e, "PullRequest", n.ID, "comments", "", commentSel, n.Comments); err != nil {
		return err
	}
	if err := drainConn(ctx, e, "PullRequest", n.ID, "timelineItems", pullItemArgs, pullEventSel, n.Timeline); err != nil {
		return err
	}
	if err := drainConn(ctx, e, "PullRequest", n.ID, "reviews", "", reviewSel, n.Reviews); err != nil {
		return err
	}
	return drainConn(ctx, e, "PullRequest", n.ID, "commits", "", commitSel, n.Commits)
}
