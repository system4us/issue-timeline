package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/provider"
	"gitlab.com/system4us/issue-timeline/internal/store"
)

// The fake hosts of the tests: no network, no gh, glab or keyring. base is a tracker-like host (Host only); signedHost
// adds the sign-in and the edits; codeHost adds commits, CI and merge methods.

type base struct {
	key, prov, origin string
	syncErr           error
}

func (b *base) Key() string      { return b.key }
func (b *base) Provider() string { return b.prov }
func (b *base) Origin() string   { return b.origin }

func (b *base) Exists(context.Context) error { return nil }

func (b *base) Sync(_ context.Context, st *store.Store, _ provider.SyncOptions) (store.SyncEntry, error) {
	if b.syncErr != nil {
		return store.SyncEntry{}, b.syncErr
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	repo := b.key
	st.Update(func(d *store.Data) {
		d.Meta.Repo = &repo
		d.Meta.LastSync = &now
		d.Meta.FullSyncAt = &now
	})
	return store.SyncEntry{At: now}, st.Save()
}

func (b *base) Refresh(_ context.Context, _ string, rec *model.Record) (*model.Record, error) {
	c := *rec
	c.Title = rec.Title + " (read)"
	return &c, nil
}

// tracker is a Host without sign-in (like Jira, or Redmine without its roles here).
type tracker struct{ base }

// signedHost signs people in: its role and visibility are what the fixture says, and it edits issues.
type signedHost struct {
	base
	role    provider.Role
	roleErr error
	vis     provider.Visibility
}

func (h *signedHost) WhoAmI(context.Context, string) (provider.User, error) {
	return provider.User{ID: 1, Login: "ann"}, nil
}

func (h *signedHost) RoleOf(context.Context, provider.Cred, provider.User) (provider.Role, error) {
	return h.role, h.roleErr
}

func (h *signedHost) VisibilityOf(context.Context, provider.Cred, provider.User, provider.Role, []*model.Record) (provider.Visibility, error) {
	return h.vis, nil
}

// EditIssue applies the title change and returns the record as the host reads it back (no links: the other side is
// the app's job).
func (h *signedHost) EditIssue(_ context.Context, _ provider.Cred, old *model.Record, ch provider.Changes) (*model.Record, []string, error) {
	c := *old
	if ch.Has("title") {
		ch.Get("title", &c.Title)
	}
	return &c, nil, nil
}

func (h *signedHost) CreateIssue(_ context.Context, _ provider.Cred, ch provider.Changes) (*model.Record, error) {
	var title string
	ch.Get("title", &title)
	n := 9
	return &model.Record{Kind: "issue", ID: "I_9", Number: &n, Key: "9", Title: title, State: "OPEN"}, nil
}

func (h *signedHost) EditOptions(context.Context, provider.Cred) (provider.EditOptions, error) {
	return provider.EditOptions{Labels: []map[string]any{{"name": "bug"}}}, nil
}

// codeHost is a code repo: one commit, no CI.
type codeHost struct{ signedHost }

func (c *codeHost) CommitPage(_ context.Context, cursor string) (model.CommitPage, error) {
	if cursor != "" {
		return model.CommitPage{}, nil
	}
	return model.CommitPage{Items: []model.Commit{{
		Sha: strings.Repeat("a", 40), Title: "fix first issue", Author: strPtr("ann"), At: strPtr("2026-10-04T10:00:00Z"),
	}}}, nil
}

func (c *codeHost) CommitFiles(context.Context, string) (*model.CommitInfo, []model.HostFile, error) {
	return &model.CommitInfo{Title: "fix first issue"}, []model.HostFile{{Path: "a.go", Status: "modified"}}, nil
}

func (c *codeHost) MergeMethod(context.Context, *model.Record) (string, error) { return "squash", nil }

func (c *codeHost) CIRuns(context.Context, time.Time) ([]model.CIRun, error) { return nil, nil }

func (c *codeHost) WorkflowChanges(context.Context, time.Time) ([]model.WorkflowChange, error) {
	return nil, nil
}

// fixture is what the fake hosts answer.
type fixture struct {
	role    provider.Role
	roleErr error
	vis     provider.Visibility
	syncErr error
}

// Repo keys the tests use: acme/app is a code repo (with commits), acme/new is one added by a test, and the Redmine
// tracker is linked to acme/app.
const (
	codeRepo    = "acme/app"
	newRepo     = "acme/new"
	trackerRepo = "redmine:https://rm.example.com/backend"
)

func hostFor(repo string, fx fixture) provider.Host {
	if isTracker(repo) {
		return &tracker{base{key: repo, prov: "redmine", origin: "https://rm.example.com", syncErr: fx.syncErr}}
	}
	b := base{key: repo, prov: "github", origin: "github", syncErr: fx.syncErr}
	sh := signedHost{base: b, role: fx.role, roleErr: fx.roleErr, vis: fx.vis}
	if repo == codeRepo || repo == newRepo {
		return &codeHost{sh}
	}
	return &sh
}

// nodeStore is a store file as the Node server wrote it: issue 1 with a public and a private note, issue 2, and pull 5
// (merged, with one commit that names issue 1).
const nodeStore = `{
 "meta": {"repo": "acme/app", "lastSync": "2026-11-01T10:00:00.000Z", "fullSyncAt": "2026-11-01T10:00:00.000Z", "schema": 3,
   "syncs": [{"at": "2026-11-01T10:00:00.000Z", "full": true, "issues": 2, "pulls": 1, "ms": 5}]},
 "issues": {
   "1": {"kind": "issue", "id": "I_1", "key": "1", "number": 1, "title": "First issue", "body": "see the pull", "url": "https://github.com/acme/app/issues/1",
     "author": "ann", "createdAt": "2026-10-01T10:00:00Z", "updatedAt": "2026-10-02T10:00:00Z", "closedAt": null, "state": "OPEN",
     "labels": [{"name": "bug", "color": "ff0000"}], "assignees": [], "milestone": {"title": "v1", "dueOn": "2026-12-01T00:00:00Z"},
     "commentsCount": 2,
     "comments": [{"id": "c1", "author": "ann", "createdAt": "2026-10-01T11:00:00Z", "body": "public note"},
                  {"id": "c2", "author": "bob", "createdAt": "2026-10-01T12:00:00Z", "body": "private note", "private": true}],
     "events": [], "blockedBy": [], "blocking": [], "subIssues": []},
   "2": {"kind": "issue", "id": "I_2", "key": "2", "number": 2, "title": "Second issue", "body": "", "url": "https://github.com/acme/app/issues/2",
     "author": "bob", "createdAt": "2026-10-01T10:00:00Z", "updatedAt": "2026-10-02T10:00:00Z", "closedAt": null, "state": "OPEN",
     "labels": [], "assignees": [], "milestone": null, "commentsCount": 0, "comments": [], "events": [],
     "blockedBy": [], "blocking": [], "subIssues": []}
 },
 "pulls": {
   "5": {"kind": "pull", "id": "PR_5", "key": "5", "number": 5, "title": "Fix the first issue", "body": "", "url": "https://github.com/acme/app/pull/5",
     "author": "ann", "createdAt": "2026-10-03T08:00:00Z", "updatedAt": "2026-10-03T10:00:00Z", "closedAt": "2026-10-03T10:00:00Z",
     "state": "MERGED", "mergedAt": "2026-10-03T10:00:00Z", "labels": [], "assignees": [], "milestone": null,
     "commentsCount": 0, "comments": [], "events": [], "base": "main", "head": "fix",
     "commits": [{"oid": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "message": "fix first\n\nFixes #1", "at": "2026-10-03T09:00:00Z", "author": "ann"}]}
 }
}`

// writeNodeFiles writes the data folder of a Node server: repos.json, links.json, repo-modes.json, and the store of
// acme/app (acme__app.json).
func writeNodeFiles(t *testing.T, dir string) {
	t.Helper()
	files := map[string]string{
		"repos.json":      `["acme/app", "` + trackerRepo + `", "not a repo", "acme/app"]`,
		"links.json":      `{"` + trackerRepo + `": ["acme/app"]}`,
		"repo-modes.json": `{"acme/app": "write", "` + trackerRepo + `": "bogus"}`,
		"acme__app.json":  nodeStore,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// newTestApp is an App over dir with the fake hosts of fx. Every sync is waited for when the test ends.
func newTestApp(t *testing.T, dir string, fx fixture, env map[string]string) *App {
	t.Helper()
	if env == nil {
		env = map[string]string{}
	}
	cfg := Config{
		StoreDir: dir,
		Env:      func(k string) string { return env[k] },
		Log:      func(string, ...any) {},
		Hosts: func(repo string) (provider.Host, error) {
			return hostFor(repo, fx), nil
		},
	}
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		a.Stop() // no sync asked for by an edit or a webhook starts after the test
		for _, r := range a.Repos() {
			waitIdle(t, a, r)
		}
	})
	return a
}

// waitIdle waits until a repo's sync is over (its status says so).
func waitIdle(t *testing.T, a *App, repo string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		d, err := a.Data(repo, nil)
		if err != nil {
			t.Errorf("data of %s: %v", repo, err)
			return
		}
		if !d.Status.Syncing {
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("sync of %s did not finish", repo)
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// jsonOf is v as JSON, for the changes of an edit.
func jsonOf(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// writeRepos writes a repos.json listing the given repos (and no stores), for a test that starts from nothing.
func writeRepos(t *testing.T, dir string, repos ...string) {
	t.Helper()
	b, err := json.Marshal(repos)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "repos.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}
