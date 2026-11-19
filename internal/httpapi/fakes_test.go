package httpapi

import (
	"context"
	"strings"
	"testing"
	"time"

	"gitlab.com/system4us/issue-timeline/internal/app"
	"gitlab.com/system4us/issue-timeline/internal/credentials"
	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/provider"
	"gitlab.com/system4us/issue-timeline/internal/store"
)

// fakeHost is a host that answers from memory: one record of each kind, and the edits echo the change. A tracker
// (Redmine, Jira) is this type alone; a code host is a fakeCode, which adds the commits, diffs and CI of a code host.
type fakeHost struct {
	key, prov, origin string
}

// fakeCode is a code host (GitHub, GitLab): a fakeHost that also reads commits, merges and CI.
type fakeCode struct {
	*fakeHost
}

func (h *fakeHost) Key() string      { return h.key }
func (h *fakeHost) Provider() string { return h.prov }
func (h *fakeHost) Origin() string   { return h.origin }

func (h *fakeHost) Exists(ctx context.Context) error { return nil }

func (h *fakeHost) Sync(ctx context.Context, st *store.Store, opt provider.SyncOptions) (store.SyncEntry, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	key := h.key
	st.Update(func(d *store.Data) {
		d.Meta.Repo = &key
		d.Meta.LastSync = &now
		d.Meta.FullSyncAt = &now
		d.Upsert("issues", fakeIssue(now))
		d.Upsert("pulls", fakePull(now))
	})
	return store.SyncEntry{At: now, Full: true, Issues: 1, Pulls: 1}, st.Save()
}

func (h *fakeHost) Refresh(ctx context.Context, kind string, rec *model.Record) (*model.Record, error) {
	c := *rec
	return &c, nil
}

func (h *fakeHost) EditIssue(ctx context.Context, cred provider.Cred, old *model.Record, ch provider.Changes) (*model.Record, []string, error) {
	c := *old
	ch.Get("title", &c.Title)
	return &c, nil, nil
}

func (h *fakeHost) CreateIssue(ctx context.Context, cred provider.Cred, ch provider.Changes) (*model.Record, error) {
	r := fakeIssue(time.Now().UTC().Format(time.RFC3339))
	r.Key = "2"
	ch.Get("title", &r.Title)
	return r, nil
}

func (h *fakeHost) EditOptions(ctx context.Context, cred provider.Cred) (provider.EditOptions, error) {
	return provider.EditOptions{Labels: []map[string]any{{"name": "bug", "color": "d73a4a"}}}, nil
}

func (h *fakeHost) EditPull(ctx context.Context, cred provider.Cred, old *model.Record, ch provider.Changes) (*model.Record, error) {
	c := *old
	ch.Get("title", &c.Title)
	return &c, nil
}

func (h *fakeCode) CommitPage(ctx context.Context, cursor string) (model.CommitPage, error) {
	return model.CommitPage{}, nil
}

func (h *fakeCode) CommitFiles(ctx context.Context, sha string) (*model.CommitInfo, []model.HostFile, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	patch := "@@ -1 +1 @@\n-a\n+b\n"
	return &model.CommitInfo{Title: "Fake commit", Author: model.Ptr("octo"), At: &now},
		[]model.HostFile{{Path: "main.go", Status: "modified", Additions: model.Ptr(1), Deletions: model.Ptr(1), Patch: &patch}}, nil
}

func (h *fakeCode) MergeMethod(ctx context.Context, rec *model.Record) (string, error) {
	return "squash", nil
}

func (h *fakeCode) CIRuns(ctx context.Context, since time.Time) ([]model.CIRun, error) {
	return nil, nil
}

func (h *fakeCode) WorkflowChanges(ctx context.Context, since time.Time) ([]model.WorkflowChange, error) {
	return nil, nil
}

func fakeIssue(now string) *model.Record {
	return &model.Record{
		Kind: "issue", ID: "1001", Key: "1", Number: model.Ptr(1), Title: "Fake issue",
		URL: "https://github.example.com/owner/name/issues/1", Author: model.Ptr("octo"),
		CreatedAt: now, UpdatedAt: now, State: "OPEN",
		Labels: []model.Label{}, Assignees: []string{}, Comments: []model.Comment{}, Events: []model.Event{},
	}
}

func fakePull(now string) *model.Record {
	return &model.Record{
		Kind: "pull", ID: "2002", Key: "2", Number: model.Ptr(2), Title: "Fake pull",
		URL: "https://github.example.com/owner/name/pull/2", Author: model.Ptr("octo"),
		CreatedAt: now, UpdatedAt: now, State: "MERGED", MergedAt: &now, Base: "main", Head: "feature",
		Labels: []model.Label{}, Assignees: []string{}, Comments: []model.Comment{}, Events: []model.Event{},
		// What a sync of a merged GitHub pull keeps: the draft flag, the review decision, the size and its reviews.
		IsDraft: model.Ptr(false), MergedBy: model.Ptr("octo"), ReviewDecision: model.Ptr("APPROVED"),
		Additions: model.Ptr(3), Deletions: model.Ptr(1), ChangedFiles: model.Ptr(1), CommitsCount: model.Ptr(1),
		Closes:  []model.Ref{},
		Reviews: []model.Review{{Author: model.Ptr("octo"), State: "APPROVED", At: &now}},
	}
}

// fakeHosts builds the host of a repo key: GitHub for owner/name, GitLab, Redmine and Jira by their prefix.
func fakeHosts(repoKey string) (provider.Host, error) {
	switch {
	case strings.HasPrefix(repoKey, "redmine:"):
		return &fakeHost{key: repoKey, prov: "redmine", origin: "https://redmine.example.com"}, nil
	case strings.HasPrefix(repoKey, "jira:"):
		return &fakeHost{key: repoKey, prov: "jira", origin: "https://jira.example.com"}, nil
	case strings.HasPrefix(repoKey, "gitlab:"):
		return &fakeCode{&fakeHost{key: repoKey, prov: "gitlab", origin: "https://gitlab.example.com"}}, nil
	}
	return &fakeCode{&fakeHost{key: repoKey, prov: "github", origin: "github"}}, nil
}

// memKeyring is an in-memory keyring, so no test reaches the OS keyring.
type memKeyring map[string]string

func (m memKeyring) Get(name string) (string, error) {
	if v, ok := m[name]; ok {
		return v, nil
	}
	return "", credentials.ErrNotFound
}

func (m memKeyring) Set(name, value string) error { m[name] = value; return nil }
func (m memKeyring) Delete(name string) error     { delete(m, name); return nil }

// buildApp is the running app on a temporary data folder, with fake hosts.
func buildApp(t *testing.T) *app.App {
	t.Helper()
	dir := t.TempDir()
	secrets := &credentials.Store{Dir: dir, Env: func(string) string { return "" }, Keyring: memKeyring{}}
	a, err := app.New(app.Config{
		StoreDir:     dir,
		PollInterval: time.Hour,
		Env:          func(string) string { return "" },
		Secrets:      secrets,
		Log:          t.Logf,
		Hosts:        fakeHosts,
		OwnToken:     func(context.Context, string) (string, error) { return "local-token", nil },
	})
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	t.Cleanup(a.Stop) // no sync asked for by an edit starts after the test
	return a
}
