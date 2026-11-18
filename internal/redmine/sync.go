package redmine

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/provider"
	"gitlab.com/system4us/issue-timeline/internal/store"
)

// Sync reads the project into the store. It is a full read on the first run (or when opt.Full, or when the store holds
// another repo); later runs read only the issues updated since the last sync, each one's journals one by one. A full
// read drops the issues the key no longer lists: deleted, or hidden from it.
func (r *Repo) Sync(ctx context.Context, st *store.Store, opt provider.SyncOptions) (store.SyncEntry, error) {
	now := opt.Now
	if now == nil {
		now = time.Now
	}
	logf := opt.Log
	if logf == nil {
		logf = func(string) {}
	}
	progress := opt.Progress
	if progress == nil {
		progress = func(provider.Progress) {}
	}

	t0 := now()
	startedAt := t0.UTC().Format(timeLayout)

	c, err := r.serverClient(ctx)
	if err != nil {
		return store.SyncEntry{}, err
	}
	proj, err := r.project(ctx, c)
	if err != nil {
		return store.SyncEntry{}, fmt.Errorf("reading project %s: %w", r.identifier, err)
	}

	var doFull bool
	var since time.Time
	st.View(func(d *store.Data) {
		doFull = opt.Full || d.Meta.FullSyncAt == nil || d.Meta.Repo == nil || *d.Meta.Repo != r.key
		if !doFull && d.Meta.LastSync != nil {
			since, _ = time.Parse(time.RFC3339Nano, *d.Meta.LastSync)
		}
	})
	st.Update(func(d *store.Data) {
		if d.Meta.Repo != nil && *d.Meta.Repo != r.key {
			d.Issues = map[string]*model.Record{}
		}
	})

	closed, err := statuses(ctx, c)
	if err != nil {
		return store.SyncEntry{}, err
	}
	defs := customFieldDefs(ctx, c)

	// The list: every issue of the project, any status. Incremental runs ask only for what changed, with a margin.
	var updatedSince string
	if !doFull && !since.IsZero() {
		updatedSince = ">=" + rmTime(since.Add(-syncMargin))
	}
	ids, err := listIssues(ctx, c, proj.ID, updatedSince, func(done int, total *int) {
		progress(provider.Progress{Phase: "Listing issues", Done: done, Total: total})
	})
	if err != nil {
		return store.SyncEntry{}, err
	}

	// The journals, relations and children come with the single issue only: one request per issue, a few at a time.
	raws := make([]rmIssue, len(ids))
	versionDue := map[int]*string{}
	for i := 0; i < len(ids); i += detailConcurrency {
		end := min(i+detailConcurrency, len(ids))
		errs := make([]error, end-i)
		var wg sync.WaitGroup
		for j := i; j < end; j++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				raws[j], errs[j-i] = fetchIssue(ctx, c, ids[j], fullInclude)
			}()
		}
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				return store.SyncEntry{}, err
			}
		}
		// A version's due date is the milestone's due date: read once per version, not once per issue.
		for j := i; j < end; j++ {
			if v := raws[j].FixedVersion; v != nil {
				if _, seen := versionDue[v.ID]; !seen {
					versionDue[v.ID] = versionDueDate(ctx, c, v.ID)
				}
			}
		}
		progress(provider.Progress{Phase: "Reading issues", Done: end, Total: model.Ptr(len(ids))})
	}

	n := normCtx{closed: closed, origin: r.origin, versionDue: versionDue, defs: defs}
	records := make([]*model.Record, 0, len(raws))
	for _, raw := range raws {
		records = append(records, normalizeIssue(raw, n))
	}

	var entry store.SyncEntry
	removed := 0
	st.Update(func(d *store.Data) {
		d.Upsert("issues", records...)
		if doFull {
			present := make(map[string]bool, len(ids))
			for _, id := range ids {
				present[strconv.Itoa(id)] = true
			}
			for k := range d.Issues {
				if !present[k] {
					delete(d.Issues, k)
					removed++
				}
			}
		}
		repo := r.key
		d.Meta.Repo = &repo
		d.Meta.LastSync = &startedAt
		if doFull {
			d.Meta.FullSyncAt = &startedAt
		}
		d.Meta.Totals = &store.Totals{Issues: len(ids)}
		entry = store.SyncEntry{At: startedAt, Full: doFull, Issues: len(records), Pulls: 0, Ms: now().Sub(t0).Milliseconds()}
		d.Meta.Syncs = append(d.Meta.Syncs, entry)
		if len(d.Meta.Syncs) > 50 {
			d.Meta.Syncs = d.Meta.Syncs[len(d.Meta.Syncs)-50:]
		}
	})
	if err := st.Save(); err != nil {
		return entry, err
	}
	kind := "incremental"
	if doFull {
		kind = "full"
	}
	extra := ""
	if removed > 0 {
		extra = fmt.Sprintf(", %d no longer in Redmine", removed)
	}
	logf(fmt.Sprintf("sync %s (%s): %d issues in %d ms%s", kind, r.identifier, entry.Issues, entry.Ms, extra))
	return entry, nil
}

// versionDueDate is a version's due date (Redmine 5 has due_date; older ones effective_date), nil when it cannot be read.
func versionDueDate(ctx context.Context, c client, id int) *string {
	var d struct {
		Version struct {
			DueDate       *string `json:"due_date"`
			EffectiveDate *string `json:"effective_date"`
		} `json:"version"`
	}
	if err := c.do(ctx, http.MethodGet, "/versions/"+strconv.Itoa(id)+".json", nil, "", nil, &d); err != nil {
		return nil
	}
	if d.Version.DueDate != nil {
		return d.Version.DueDate
	}
	return d.Version.EffectiveDate
}
