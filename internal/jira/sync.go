package jira

import (
	"context"
	"fmt"
	"net/url"
	"sync"
	"time"

	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/provider"
	"gitlab.com/system4us/issue-timeline/internal/store"
)

const (
	// pageSize is the search's page.
	pageSize = 100
	// syncMargin re-reads a little before the last sync: clocks and eventual consistency.
	syncMargin = 2 * time.Minute
	// detailConcurrency is how many single-issue reads run at once in a sync.
	detailConcurrency = 3
	// jqlWindow is how far back the JQL starts from the last sync. JQL reads its times in the account's zone, not in
	// UTC: a day covers any zone, and the exact cut is made here on each issue's own updated time.
	jqlWindow = 24 * time.Hour
	// fieldList is what a single-issue read asks for (the start date's id is added when the site has one).
	fieldList = "duedate,summary,description,status,issuetype,priority,labels,assignee,reporter,creator,created,updated," +
		"resolutiondate,fixVersions,parent,subtasks,issuelinks,comment,project"
)

// Sync reads the project into the store: in full on the first run (or when opt.Full, or when the store holds another
// repo), later only the issues updated since the last sync, each one's changelog and comments with it. A full read does
// not drop issues the credentials no longer list.
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
	startedAt := t0.UTC().Format("2006-01-02T15:04:05.000Z")

	c, err := r.client(ctx)
	if err != nil {
		return store.SyncEntry{}, err
	}

	var doFull bool
	var since time.Time
	st.View(func(d *store.Data) {
		doFull = opt.Full || d.Meta.FullSyncAt == nil || d.Meta.Repo == nil || *d.Meta.Repo != r.key
		if !doFull && d.Meta.LastSync != nil {
			since, _ = time.Parse(time.RFC3339Nano, *d.Meta.LastSync)
		}
	})
	hasSince := !doFull && !since.IsZero()
	st.Update(func(d *store.Data) {
		if d.Meta.Repo != nil && *d.Meta.Repo != r.key {
			d.Issues = map[string]*model.Record{}
		}
	})

	done, err := doneStatusNames(ctx, c)
	if err != nil {
		return store.SyncEntry{}, err
	}
	startField := startFieldID(ctx, c)

	// The list, with the search endpoint's opaque pages (nextPageToken), read one after another.
	jql := fmt.Sprintf(`project = "%s"`, r.projectKey)
	if hasSince {
		jql += fmt.Sprintf(` AND updated >= "%s"`, jqlTime(since.Add(-jqlWindow)))
	}
	jql += " ORDER BY updated DESC"
	var keys []string
	token := ""
	for {
		q := url.Values{"jql": {jql}, "maxResults": {fmt.Sprint(pageSize)}, "fields": {"updated"}}
		if token != "" {
			q.Set("nextPageToken", token)
		}
		var page struct {
			Issues []struct {
				Key    string `json:"key"`
				Fields struct {
					Updated *string `json:"updated"`
				} `json:"fields"`
			} `json:"issues"`
			NextPageToken string `json:"nextPageToken"`
			IsLast        bool   `json:"isLast"`
		}
		if err := c.get(ctx, "/rest/api/3/search/jql", q, &page); err != nil {
			return store.SyncEntry{}, err
		}
		// The exact cut: an issue counts when it was updated since the last sync (less the margin), whatever JQL's zone.
		for _, it := range page.Issues {
			if !hasSince || !parseJiraTime(deref(it.Fields.Updated)).Before(since.Add(-syncMargin)) {
				keys = append(keys, it.Key)
			}
		}
		progress(provider.Progress{Phase: "Listing issues", Done: len(keys)})
		if page.IsLast || page.NextPageToken == "" || len(page.Issues) == 0 {
			break
		}
		token = page.NextPageToken
	}

	// The changelog and the comments come with the single issue: one request per issue, a few at a time.
	fields := fieldList
	if startField != "" {
		fields += "," + startField
	}
	raws := make([]jiraIssue, len(keys))
	for i := 0; i < len(keys); i += detailConcurrency {
		end := min(i+detailConcurrency, len(keys))
		errs := make([]error, end-i)
		var wg sync.WaitGroup
		for j := i; j < end; j++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs[j-i] = c.get(ctx, "/rest/api/3/issue/"+url.PathEscape(keys[j]), url.Values{"fields": {fields}, "expand": {"changelog"}}, &raws[j])
			}()
		}
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				return store.SyncEntry{}, err
			}
		}
		progress(provider.Progress{Phase: "Reading issues", Done: end, Total: model.Ptr(len(keys))})
	}

	n := jiraNorm{done: done, origin: r.origin, startField: startField}
	records := make([]*model.Record, 0, len(raws))
	for _, raw := range raws {
		rec, err := normalizeIssue(raw, n)
		if err != nil {
			return store.SyncEntry{}, fmt.Errorf("reading %s: %w", raw.Key, err)
		}
		records = append(records, rec)
	}

	var entry store.SyncEntry
	st.Update(func(d *store.Data) {
		d.Upsert("issues", records...)
		repo := r.key
		d.Meta.Repo = &repo
		d.Meta.LastSync = &startedAt
		if doFull {
			d.Meta.FullSyncAt = &startedAt
		}
		d.Meta.Totals = &store.Totals{Issues: len(keys)}
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
	logf(fmt.Sprintf("sync %s (%s): %d issues in %d ms", kind, r.projectKey, entry.Issues, entry.Ms))
	return entry, nil
}

// doneStatusNames are the names of the statuses whose category is "done": what a closed issue is.
func doneStatusNames(ctx context.Context, c client) (map[string]bool, error) {
	var list []struct {
		Name           string `json:"name"`
		StatusCategory *struct {
			Key string `json:"key"`
		} `json:"statusCategory"`
	}
	if err := c.get(ctx, "/rest/api/3/status", nil, &list); err != nil {
		return nil, err
	}
	done := map[string]bool{}
	for _, s := range list {
		if s.StatusCategory != nil && s.StatusCategory.Key == "done" {
			done[s.Name] = true
		}
	}
	return done, nil
}

// startFieldID is the id of the site's "Start date" field, a date field (Jira Software's own), or "" when the site has
// none. Jira has no built-in start date, so the field is found by name, once per sync. A failure to list the fields
// leaves none.
func startFieldID(ctx context.Context, c client) string {
	var fields []struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Schema *struct {
			Type string `json:"type"`
		} `json:"schema"`
	}
	if err := c.get(ctx, "/rest/api/3/field", nil, &fields); err != nil {
		return ""
	}
	for _, f := range fields {
		if f.Name == "Start date" && f.Schema != nil && f.Schema.Type == "date" {
			return f.ID
		}
	}
	return ""
}

// jqlTime is the time JQL takes ("yyyy-MM-dd HH:mm").
func jqlTime(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04")
}
