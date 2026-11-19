package gitlab

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/provider"
)

func TestCommitPageReadsOnePageAndTheNextNumber(t *testing.T) {
	f := newFakeHost(t)
	f.route("GET", projPath+"/repository/commits", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") != "2" {
			t.Errorf("page %q", r.URL.Query().Get("page"))
		}
		w.Header().Set("X-Next-Page", "3")
		jsonReply(200, []map[string]any{{"id": "abc1234", "title": "Fix", "author_name": "bea", "committed_date": "2026-01-02T00:00:00Z"}})(w, r)
	})
	r := testRepo(t, f)
	page, err := r.CommitPage(context.Background(), "2")
	mustOK(t, err)
	if page.Next != "3" || len(page.Items) != 1 || page.Items[0].Sha != "abc1234" || page.Items[0].Title != "Fix" || *page.Items[0].Author != "bea" {
		t.Fatalf("page %+v", page)
	}
	if _, err := r.CommitPage(context.Background(), "two"); errorStatus(err) != 400 {
		t.Fatalf("a cursor is a page number: %v", err)
	}
}

func TestCommitFilesNormalizesEachFileStatus(t *testing.T) {
	f := newFakeHost(t)
	f.route("GET", projPath+"/repository/commits/abc1234", jsonReply(200, map[string]any{"id": "abc1234", "title": "Move", "author_name": "bea", "committed_date": "2026-01-02T00:00:00Z"}))
	f.route("GET", projPath+"/repository/commits/abc1234/diff", jsonReply(200, []map[string]any{
		{"new_path": "a.go", "new_file": true, "diff": "+x"},
		{"new_path": "b.go", "old_path": "old.go", "renamed_file": true, "diff": ""},
		{"new_path": "c.go", "deleted_file": true, "too_large": true},
		{"new_path": "d.go", "diff": "+y"},
	}))
	r := testRepo(t, f)
	info, files, err := r.CommitFiles(context.Background(), "abc1234")
	mustOK(t, err)
	if info == nil || info.Title != "Move" || *info.Author != "bea" {
		t.Fatalf("info %+v", info)
	}
	want := []string{"added", "renamed", "deleted", "modified"}
	for i, s := range want {
		if files[i].Status != s {
			t.Errorf("file %d: %s, want %s", i, files[i].Status, s)
		}
	}
	if files[1].OldPath == nil || *files[1].OldPath != "old.go" || files[0].OldPath != nil || !files[2].TooLarge {
		t.Fatalf("old paths and sizes: %+v", files)
	}
}

func TestCommitFilesRefusesAShaThatIsNotHex(t *testing.T) {
	f := newFakeHost(t)
	r := testRepo(t, f)
	if _, _, err := r.CommitFiles(context.Background(), "../../user"); errorStatus(err) != 400 {
		t.Fatalf("a commit id is hex: %v", err)
	}
	if len(f.allCalls()) != 0 {
		t.Fatal("no request for a refused commit id")
	}
}

func TestMergeMethodFromTheMergeRequest(t *testing.T) {
	cases := []struct {
		name string
		mr   map[string]any
		want string
	}{
		{"squash", map[string]any{"state": "merged", "squash_commit_sha": "s1"}, "squash"},
		{"merge commit", map[string]any{"state": "merged", "merge_commit_sha": "m1"}, "merge commit"},
		{"rebase", map[string]any{"state": "merged"}, "rebase"},
		{"not merged", map[string]any{"state": "opened"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeHost(t)
			f.route("GET", projPath+"/merge_requests/7", jsonReply(200, tc.mr))
			r := testRepo(t, f)
			got, err := r.MergeMethod(context.Background(), &model.Record{Key: "7"})
			mustOK(t, err)
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCIRunsArePipelinesCategorizedByTheServer(t *testing.T) {
	f := newFakeHost(t)
	f.route("GET", projPath+"/pipelines", jsonReply(200, []map[string]any{
		{"id": 1, "web_url": "u1", "ref": "feature/x", "sha": "h7", "status": "success", "source": "merge_request_event", "created_at": "2026-01-01T10:00:00Z"},
		{"id": 2, "web_url": "u2", "ref": "main", "sha": "m1", "status": "failed", "source": "push", "created_at": "2026-01-01T09:00:00Z"},
		{"id": 3, "web_url": "u3", "ref": "pages-redesign", "sha": "h8", "status": "failed", "source": "merge_request_event", "created_at": "2026-01-01T08:00:00Z"},
	}))
	r := testRepo(t, f)
	since := time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC)
	runs, err := r.CIRuns(context.Background(), since)
	mustOK(t, err)
	if len(runs) != 3 {
		t.Fatalf("runs %d", len(runs))
	}
	if runs[0].Event != "pull_request" || runs[0].Category != "ci" || runs[0].Status != "success" || *runs[0].Conclusion != "success" {
		t.Fatalf("a merge request pipeline is the MR's CI: %+v", runs[0])
	}
	if runs[2].Category != "ci" || runs[2].Branch == nil || *runs[2].Branch != "pages-redesign" || runs[2].Path != "" {
		t.Fatalf("a branch name never decides the category: %+v", runs[2])
	}
	q, _ := url.ParseQuery(f.callsTo("GET", projPath+"/pipelines")[0].query)
	if q.Get("updated_after") != "2025-12-01T00:00:00.000Z" || q.Get("order_by") != "updated_at" {
		t.Fatalf("query %v", q)
	}
}

func TestWorkflowChangesAreTheCommitsOfTheCIFile(t *testing.T) {
	f := newFakeHost(t)
	f.route("GET", projPath+"/repository/commits", jsonReply(200, []map[string]any{{"id": "c1", "title": "CI", "author_name": "ana", "committed_date": "2026-01-01T00:00:00Z", "web_url": "w1"}}))
	r := testRepo(t, f)
	ch, err := r.WorkflowChanges(context.Background(), time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC))
	mustOK(t, err)
	if len(ch) != 1 || ch[0].Sha != "c1" || ch[0].URL == nil || *ch[0].URL != "w1" {
		t.Fatalf("changes %+v", ch)
	}
	if q := f.callsTo("GET", projPath+"/repository/commits")[0].query; !strings.Contains(q, "path=.gitlab-ci.yml") {
		t.Fatalf("the CI file: %s", q)
	}
}

func TestExistsIsNotFoundForAProjectTheTokenCannotSee(t *testing.T) {
	f := newFakeHost(t)
	f.route("GET", projPath, jsonReply(200, map[string]any{"id": 1}))
	r := testRepo(t, f)
	mustOK(t, r.Exists(context.Background()))
	f2 := newFakeHost(t)
	r2 := testRepo(t, f2)
	if err := r2.Exists(context.Background()); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("a 404 is not found: %v", err)
	}
}

func TestIdentityNamesTheAccountAndItsTokenSource(t *testing.T) {
	f := newFakeHost(t)
	f.route("GET", "/api/v4/user", jsonReply(200, map[string]any{"id": 9, "username": "ana"}))
	hostName := strings.TrimPrefix(f.srv.URL, "http://")
	login, source, err := Identity(context.Background(), f.srv.URL, Options{Token: staticToken("t"), HTTP: f.srv.Client()})
	mustOK(t, err)
	if login != "ana" || source != "glab session" {
		t.Fatalf("login %q source %q", login, source)
	}
	envName := "GITLAB_TOKEN_" + strings.ToUpper(sanitizeEnvHost(hostName))
	t.Setenv(envName, "from-env")
	login, source, err = Identity(context.Background(), f.srv.URL, Options{Token: staticToken("from-env"), HTTP: f.srv.Client()})
	mustOK(t, err)
	if login != "ana" || source != envName {
		t.Fatalf("the source names the variable: %q", source)
	}
	if _, _, err := Identity(context.Background(), "ftp://h", Options{Token: staticToken("t")}); err == nil {
		t.Fatal("an origin must be http or https")
	}
}

func TestA401DropsTheServersCachedToken(t *testing.T) {
	f := newFakeHost(t)
	f.route("GET", projPath+"/repository/commits", jsonReply(401, map[string]string{"message": "401"}))
	host := strings.TrimPrefix(f.srv.URL, "http://")
	defaultTokens.mu.Lock()
	defaultTokens.entries[host] = tokenEntry{token: "old", until: time.Now().Add(time.Minute)}
	defaultTokens.mu.Unlock()
	t.Cleanup(func() { defaultTokens.forget(host) })
	r := testRepo(t, f)
	if _, err := r.CommitPage(context.Background(), ""); errorStatus(err) != 401 {
		t.Fatalf("a refused token: %v", err)
	}
	defaultTokens.mu.Lock()
	_, cached := defaultTokens.entries[host]
	defaultTokens.mu.Unlock()
	if cached {
		t.Fatal("the cached server token is dropped, so the next request reads it again")
	}
}

func TestAPersonsRefusedTokenDoesNotDropTheServersToken(t *testing.T) {
	f := newFakeHost(t)
	f.route("GET", "/api/v4/user", jsonReply(401, map[string]string{"message": "401"}))
	host := strings.TrimPrefix(f.srv.URL, "http://")
	defaultTokens.mu.Lock()
	defaultTokens.entries[host] = tokenEntry{token: "server", until: time.Now().Add(time.Minute)}
	defaultTokens.mu.Unlock()
	t.Cleanup(func() { defaultTokens.forget(host) })
	r := testRepo(t, f)
	if _, err := r.WhoAmI(context.Background(), "person"); errorStatus(err) != 401 {
		t.Fatalf("a refused person: %v", err)
	}
	defaultTokens.mu.Lock()
	_, cached := defaultTokens.entries[host]
	defaultTokens.mu.Unlock()
	if !cached {
		t.Fatal("the server's own token stays cached")
	}
}
