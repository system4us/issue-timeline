package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"gitlab.com/system4us/issue-timeline/internal/app"
	"gitlab.com/system4us/issue-timeline/internal/auth"
	"gitlab.com/system4us/issue-timeline/internal/credentials"
	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/provider"
	"gitlab.com/system4us/issue-timeline/internal/store"
)

type fakeHost struct {
	key string
}

func (f *fakeHost) Key() string      { return f.key }
func (f *fakeHost) Provider() string { return "github" }
func (f *fakeHost) Origin() string   { return "github" }
func (f *fakeHost) Exists(context.Context) error {
	return nil
}
func (f *fakeHost) Sync(_ context.Context, st *store.Store, _ provider.SyncOptions) (store.SyncEntry, error) {
	now := time.Now().UTC()
	oldDate := now.Add(-30 * 24 * time.Hour).Format(time.RFC3339)
	freshDate := now.Format(time.RFC3339)
	pastDue := now.Add(-48 * time.Hour).Format("2006-01-02")
	epic := model.Ref("1")
	p2 := model.Ref("2")
	st.Update(func(d *store.Data) {
		d.Upsert("issues",
			&model.Record{
				Kind: "issue", ID: "i1", Key: "1", Number: model.Ptr(1), Title: "Parent epic",
				State: "open", CreatedAt: oldDate, UpdatedAt: freshDate,
				SubIssues: []model.Ref{"2", "3"},
			},
			&model.Record{
				Kind: "issue", ID: "i2", Key: "2", Number: model.Ptr(2), Title: "Blocked child overdue",
				Body: "Depends on #3", State: "open", CreatedAt: oldDate, UpdatedAt: oldDate, DueDate: &pastDue,
				Parent: &epic, BlockedBy: []model.Ref{"3"},
				Comments: []model.Comment{
					{ID: "c1", Author: model.Ptr("alice"), CreatedAt: freshDate, Body: "public note"},
					{ID: "c2", Author: model.Ptr("bob"), CreatedAt: freshDate, Body: "internal secret", Private: true},
				},
				Events: []model.Event{
					{
						Type:   "labeled",
						At:     freshDate,
						Actor:  model.Ptr("alice"),
						Fields: map[string]json.RawMessage{"label": json.RawMessage(`"p1"`)},
					},
				},
			},
			&model.Record{
				Kind: "issue", ID: "i3", Key: "3", Number: model.Ptr(3), Title: "Secret blocker issue",
				State: "open", CreatedAt: oldDate, UpdatedAt: freshDate, Confidential: model.Ptr(true),
				Parent: &epic, Blocking: []model.Ref{p2},
			},
		)
		d.Upsert("pulls",
			&model.Record{
				Kind: "pull", ID: "p10", Key: "10", Number: model.Ptr(10), Title: "Fix #2",
				Body: "Closes #2", State: "closed", MergedAt: &freshDate,
				CreatedAt: oldDate, UpdatedAt: freshDate, Closes: []model.Ref{p2},
			},
		)
		d.Meta.LastSync = &freshDate
		d.Meta.FullSyncAt = &freshDate
	})
	_ = st.Save()
	return store.SyncEntry{Issues: 3, Pulls: 1}, nil
}
func (f *fakeHost) Refresh(context.Context, string, *model.Record) (*model.Record, error) {
	return nil, nil
}
func (f *fakeHost) WhoAmI(context.Context, string) (provider.User, error) {
	return provider.User{ID: 1, Login: "viewer"}, nil
}
func (f *fakeHost) RoleOf(_ context.Context, cred provider.Cred, _ provider.User) (provider.Role, error) {
	if cred.Token == "guest-tok" {
		return provider.RoleViewer, nil
	}
	if cred.Token == "admin-tok" {
		return provider.RoleAdmin, nil
	}
	return provider.RoleNone, nil
}
func (f *fakeHost) VisibilityOf(_ context.Context, cred provider.Cred, _ provider.User, _ provider.Role, _ []*model.Record) (provider.Visibility, error) {
	if cred.Token == "guest-tok" {
		return provider.Visibility{
			Issues:         map[string]bool{"1": true, "2": true}, // hides #3
			HideCode:       true,                                  // hides PR #10 and commits
			HiddenComments: map[string]map[string]bool{"2": {"c2": true}},
		}, nil
	}
	return provider.Visibility{}, nil
}

type whoKey struct{}

func connectMCP(t *testing.T, url, bearer string) (*sdkmcp.ClientSession, func()) {
	t.Helper()
	hc := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if bearer != "" {
				req.Header.Set("Authorization", "Bearer "+bearer)
			}
			return http.DefaultTransport.RoundTrip(req)
		}),
	}
	c := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	cs, err := c.Connect(context.Background(), &sdkmcp.StreamableClientTransport{
		Endpoint:             url,
		HTTPClient:           hc,
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return cs, func() { _ = cs.Close() }
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func callJSON[T any](t *testing.T, cs *sdkmcp.ClientSession, name string, args any) T {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s error: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("%s returned tool error: %+v", name, res.Content)
	}
	tc, ok := res.Content[0].(*sdkmcp.TextContent)
	if !ok {
		t.Fatalf("%s content not text: %T", name, res.Content[0])
	}
	var out T
	if err := json.Unmarshal([]byte(tc.Text), &out); err != nil {
		t.Fatalf("%s unmarshal %s: %v", name, tc.Text, err)
	}
	return out
}

func TestReadOnlyMCPToolsAndVisibility(t *testing.T) {
	dir := t.TempDir()
	h := &fakeHost{key: "acme/app"}
	a, err := app.New(app.Config{
		StoreDir: dir, PollInterval: time.Hour, Env: func(string) string { return "" },
		Secrets:  &credentials.Store{Dir: dir, Env: func(string) string { return "" }},
		Log:      t.Logf,
		Hosts:    func(string) (provider.Host, error) { return h, nil },
		OwnToken: func(context.Context, string) (string, error) { return "srv", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Stop()

	if _, _, err := a.AddRepo(context.Background(), "github", "acme/app", "read"); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if d, err := a.Data("acme/app", nil); err == nil && !d.Status.Syncing && len(d.Issues) == 3 {
			break
		}
	}

	ss := auth.NewSessions(auth.SessionOptions{})
	adminSess := ss.Get(ss.Create("github", "github", "admin-tok", provider.User{ID: 1, Login: "admin"}))
	guestSess := ss.Get(ss.Create("github", "github", "guest-tok", provider.User{ID: 2, Login: "guest"}))

	mcpHandler := NewHandler(a, func(ctx context.Context) (app.Who, bool) {
		w, ok := ctx.Value(whoKey{}).(app.Who)
		return w, ok
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "Bearer admin":
			r = r.WithContext(context.WithValue(r.Context(), whoKey{}, app.Who{Session: adminSess}))
		case "Bearer guest":
			r = r.WithContext(context.WithValue(r.Context(), whoKey{}, app.Who{Session: guestSess}))
		default:
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		mcpHandler.ServeHTTP(w, r)
	}))
	defer srv.Close()

	adminCS, closeAdmin := connectMCP(t, srv.URL, "admin")
	defer closeAdmin()

	// Verify all tools are marked read-only.
	tools, err := adminCS.ListTools(context.Background(), nil)
	if err != nil || len(tools.Tools) != 7 {
		t.Fatalf("ListTools = %d, err=%v", len(tools.Tools), err)
	}
	for _, tl := range tools.Tools {
		if tl.Annotations == nil || !tl.Annotations.ReadOnlyHint {
			t.Fatalf("tool %s missing ReadOnlyHint=true", tl.Name)
		}
	}

	repos := callJSON[listReposOut](t, adminCS, "list_repos", map[string]any{})
	if len(repos.Repos) != 1 || repos.Repos[0].Repo != "acme/app" {
		t.Fatalf("list_repos = %+v", repos)
	}

	srch := callJSON[searchItemsOut](t, adminCS, "search_items", map[string]any{"repo": "acme/app", "query": "Blocked"})
	if srch.TotalMatched != 1 || srch.Items[0].Key != "2" || srch.Notice == "" {
		t.Fatalf("search_items = %+v", srch)
	}

	item := callJSON[getItemOut](t, adminCS, "get_item", map[string]any{"repo": "acme/app", "key": "2", "includeEvents": true})
	if item.TotalComments != 2 || len(item.Relations.LinkedPulls) != 1 || item.Relations.LinkedPulls[0] != "10" {
		t.Fatalf("get_item(admin) = %+v", item)
	}
	if len(item.Events) != 1 || item.Events[0].Type != "labeled" || item.Events[0].Fields["label"] != "p1" {
		t.Fatalf("get_item(admin) events = %+v", item.Events)
	}

	deps := callJSON[getDependenciesOut](t, adminCS, "get_dependencies", map[string]any{"repo": "acme/app", "key": "2"})
	if len(deps.TransitiveBlockers) != 1 || deps.TransitiveBlockers[0].Key != "3" {
		t.Fatalf("get_dependencies(admin) = %+v", deps)
	}

	tree := callJSON[getTreeOut](t, adminCS, "get_tree", map[string]any{"repo": "acme/app", "rootKey": "1"})
	if len(tree.Edges) != 2 {
		t.Fatalf("get_tree(admin) = %+v", tree)
	}

	code := callJSON[getLinkedCodeOut](t, adminCS, "get_linked_code", map[string]any{"repo": "acme/app", "key": "2"})
	if len(code.LinkedPulls) != 1 || code.LinkedPulls[0].Key != "10" {
		t.Fatalf("get_linked_code(admin) = %+v", code)
	}

	ov := callJSON[repoOverviewOut](t, adminCS, "repo_overview", map[string]any{"repo": "acme/app"})
	if ov.TotalIssues != 3 || ov.OpenIssues != 3 || ov.OverdueOpenIssues != 1 || ov.StaleOpenIssues != 1 || ov.BlockedOpenIssues != 1 || ov.MergedPulls != 1 {
		t.Fatalf("repo_overview(admin) = %+v", ov)
	}

	// Guest sees #1 and #2, public comment c1 only, NO #3, and NO PR #10.
	guestCS, closeGuest := connectMCP(t, srv.URL, "guest")
	defer closeGuest()

	gItem := callJSON[getItemOut](t, guestCS, "get_item", map[string]any{"repo": "acme/app", "key": "2"})
	if gItem.TotalComments != 1 || gItem.Comments[0].ID != "c1" {
		t.Fatalf("guest should not see private comment c2: %+v", gItem.Comments)
	}
	res3, err := guestCS.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "get_item", Arguments: map[string]any{"repo": "acme/app", "key": "3"}})
	if err != nil || !res3.IsError {
		t.Fatalf("guest must not read hidden issue #3: err=%v isErr=%v", err, res3.IsError)
	}
	resCode, err := guestCS.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "get_linked_code", Arguments: map[string]any{"repo": "acme/app", "key": "2"}})
	if err != nil || !resCode.IsError {
		t.Fatalf("guest with HideCode must be refused get_linked_code: err=%v isErr=%v", err, resCode.IsError)
	}
}
