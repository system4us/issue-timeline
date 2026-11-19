package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"gitlab.com/system4us/issue-timeline/internal/auth"
	"gitlab.com/system4us/issue-timeline/internal/derive"
	"gitlab.com/system4us/issue-timeline/internal/provider"
)

func TestLoadsNodeDataFolder(t *testing.T) {
	dir := t.TempDir()
	writeNodeFiles(t, dir)
	a := newTestApp(t, dir, fixture{role: provider.RoleEditor}, nil)

	if got, want := a.Repos(), []string{codeRepo, trackerRepo}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Repos() = %v, want %v", got, want)
	}
	if got := a.Links()[trackerRepo]; !reflect.DeepEqual(got, []string{codeRepo}) {
		t.Fatalf("Links() = %v", a.Links())
	}
	if got := a.Mode(codeRepo); got != "write" {
		t.Fatalf("Mode(acme/app) = %q, want write", got)
	}
	if got := a.Mode(trackerRepo); got != "read" {
		t.Fatalf("Mode(tracker) = %q, want the read default", got)
	}
	d, err := a.Data(codeRepo, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Issues) != 2 || len(d.Pulls) != 1 {
		t.Fatalf("payload has %d issues and %d pulls, want 2 and 1", len(d.Issues), len(d.Pulls))
	}
	if d.Mode != "write" || d.Meta.LastSync == nil {
		t.Fatalf("payload mode %q, lastSync %v", d.Mode, d.Meta.LastSync)
	}
	rec, ok := a.Record(codeRepo, nil, "issue", "1")
	if !ok || len(rec.Comments) != 2 {
		t.Fatalf("issue 1 not read with its two notes: %v", ok)
	}
}

func TestStoreFileNames(t *testing.T) {
	s := &state{storeDir: "/data"}
	if got, want := s.storeFileFor("acme/app"), filepath.Join("/data", "acme__app.json"); got != want {
		t.Fatalf("GitHub store file = %s, want %s", got, want)
	}
	sum := sha256.Sum256([]byte(trackerRepo))
	want := filepath.Join("/data", "redmine-"+hex.EncodeToString(sum[:])[:16]+".json")
	if got := s.storeFileFor(trackerRepo); got != want {
		t.Fatalf("Redmine store file = %s, want %s", got, want)
	}
	if got := commitsFileOf("/data/acme__app.json"); got != "/data/acme__app.commits.json" {
		t.Fatalf("commits file = %s", got)
	}
	if got := ciFileOf("/data/acme__app.json"); got != "/data/acme__app.ci.json" {
		t.Fatalf("CI file = %s", got)
	}
}

func TestSyncUpdatesStatusAndStores(t *testing.T) {
	dir := t.TempDir()
	writeRepos(t, dir, codeRepo)
	a := newTestApp(t, dir, fixture{role: provider.RoleEditor}, nil)
	if !a.RunSync(codeRepo, false) {
		t.Fatal("RunSync did not start a sync of a configured repo")
	}
	waitIdle(t, a, codeRepo)
	d, err := a.Data(codeRepo, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status.LastError != nil || d.Status.CommitsError != nil || d.Status.CIError != nil {
		t.Fatalf("status has errors: %+v", d.Status)
	}
	if d.Status.NextSyncAt == nil {
		t.Fatal("no next sync time after a sync")
	}
	if d.Meta.LastSync == nil {
		t.Fatal("meta.lastSync not set by the sync")
	}
	list, err := a.Commits(codeRepo, nil, CommitQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if list.Total != 1 || list.Items[0].Title != "fix first issue" {
		t.Fatalf("commits after sync: %+v", list)
	}
	for _, name := range []string{"acme__app.json", "acme__app.commits.json", "acme__app.ci.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("sync did not write %s: %v", name, err)
		}
	}
}

func TestSyncErrorIsStatus(t *testing.T) {
	dir := t.TempDir()
	writeRepos(t, dir, codeRepo)
	a := newTestApp(t, dir, fixture{syncErr: os.ErrPermission}, nil)
	a.RunSync(codeRepo, false)
	waitIdle(t, a, codeRepo)
	d, err := a.Data(codeRepo, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status.LastError == nil || d.Status.Syncing {
		t.Fatalf("a failed sync must set lastError and stop: %+v", d.Status)
	}
}

func TestRunSyncRefusesWhileRunning(t *testing.T) {
	dir := t.TempDir()
	writeRepos(t, dir, codeRepo)
	a := newTestApp(t, dir, fixture{}, nil)
	if !a.RunSync(codeRepo, false) {
		t.Fatal("first sync did not start")
	}
	if a.RunSync(codeRepo, false) {
		t.Fatal("a second sync started while one was running")
	}
	if a.RunSync("acme/unknown", false) {
		t.Fatal("a sync of an unknown repo started")
	}
	waitIdle(t, a, codeRepo)
}

func TestSessionWithoutRoleSeesNoRepo(t *testing.T) {
	dir := t.TempDir()
	writeNodeFiles(t, dir)
	ctx := context.Background()

	a := newTestApp(t, dir, fixture{role: provider.RoleNone}, nil)
	sessions := auth.NewSessions(auth.SessionOptions{})
	sess := sessions.Get(sessions.Create("github", "github", "tok", provider.User{ID: 1, Login: "ann"}))
	if sess == nil {
		t.Fatal("session not created")
	}
	who := Who{Session: sess}
	if got := a.VisibleRepos(ctx, who); len(got) != 0 {
		t.Fatalf("a person with no role sees %v", got)
	}
	if role, err := a.RoleIn(ctx, codeRepo, who); err != nil || role != provider.RoleNone {
		t.Fatalf("RoleIn = %q, %v", role, err)
	}
	if _, refusal, _ := a.Writer(ctx, codeRepo, who); refusal == "" {
		t.Fatal("a person with no role may write")
	}

	// the same person as an editor, on another app: the repo is theirs to see and to edit
	bdir := t.TempDir()
	writeNodeFiles(t, bdir)
	b := newTestApp(t, bdir, fixture{role: provider.RoleEditor}, nil)
	sess2 := sessions.Get(sessions.Create("github", "github", "tok2", provider.User{ID: 2, Login: "bob"}))
	who2 := Who{Session: sess2}
	// the tracker has no sign-in, so an editor sees the code repo only
	if got := b.VisibleRepos(ctx, who2); !reflect.DeepEqual(got, []string{codeRepo}) {
		t.Fatalf("an editor sees %v", got)
	}
	cred, refusal, err := b.Writer(ctx, codeRepo, who2)
	if err != nil || refusal != "" || cred.Token != "tok2" {
		t.Fatalf("editor writer = %+v, %q, %v", cred, refusal, err)
	}
}

func TestViewerCannotWrite(t *testing.T) {
	dir := t.TempDir()
	writeNodeFiles(t, dir)
	a := newTestApp(t, dir, fixture{role: provider.RoleViewer}, nil)
	sessions := auth.NewSessions(auth.SessionOptions{})
	sess := sessions.Get(sessions.Create("github", "github", "tok", provider.User{ID: 1}))
	_, refusal, err := a.Writer(context.Background(), codeRepo, Who{Session: sess})
	if err != nil || refusal == "" {
		t.Fatalf("a viewer may write: refusal %q, err %v", refusal, err)
	}
}

func TestLocalIdentitySeesEverything(t *testing.T) {
	dir := t.TempDir()
	writeNodeFiles(t, dir)
	a := newTestApp(t, dir, fixture{}, nil)
	ctx := context.Background()
	if got := a.VisibleRepos(ctx, Who{Local: true}); len(got) != 2 {
		t.Fatalf("this machine sees %v", got)
	}
	if role, _ := a.RoleIn(ctx, codeRepo, Who{Local: true}); role != provider.RoleAdmin {
		t.Fatalf("this machine's role is %q", role)
	}
	if got := a.VisibleRepos(ctx, Who{}); len(got) != 0 {
		t.Fatalf("a request with no identity sees %v", got)
	}
}

func TestSignInOriginsFromConfiguredRepos(t *testing.T) {
	dir := t.TempDir()
	writeNodeFiles(t, dir)
	a := newTestApp(t, dir, fixture{}, nil)
	if got := a.SignInOrigins("github"); !reflect.DeepEqual(got, []string{"github"}) {
		t.Fatalf("SignInOrigins(github) = %v", got)
	}
	if got := a.SignInOrigins("gitlab"); len(got) != 0 {
		t.Fatalf("SignInOrigins(gitlab) = %v, want none", got)
	}
	if _, ok := a.Accounts("github", "github"); !ok {
		t.Fatal("the GitHub host does not sign people in")
	}
	if _, ok := a.Accounts("redmine", "https://other.example.com"); ok {
		t.Fatal("an origin that is not configured has accounts")
	}
}

func TestAddRepoSavesAndSetsMode(t *testing.T) {
	dir := t.TempDir()
	a := newTestApp(t, dir, fixture{}, nil)
	repo, added, err := a.AddRepo(context.Background(), "github", "https://github.com/acme/new.git", "write")
	if err != nil {
		t.Fatal(err)
	}
	if repo != newRepo || !added {
		t.Fatalf("AddRepo = %q, %v", repo, added)
	}
	if a.Mode(newRepo) != "write" {
		t.Fatalf("mode = %q", a.Mode(newRepo))
	}
	b, err := os.ReadFile(filepath.Join(dir, "repos.json"))
	if err != nil {
		t.Fatal(err)
	}
	var list []string
	if err := json.Unmarshal(b, &list); err != nil || !reflect.DeepEqual(list, []string{newRepo}) {
		t.Fatalf("repos.json = %s", b)
	}
	if _, again, err := a.AddRepo(context.Background(), "github", newRepo, ""); err != nil || again {
		t.Fatalf("a repo added twice: added %v, err %v", again, err)
	}
	if _, _, err := a.AddRepo(context.Background(), "github", "not a repo", ""); err == nil {
		t.Fatal("a bad repo was added")
	}
	waitIdle(t, a, newRepo)
}

func TestSetLinksValidation(t *testing.T) {
	dir := t.TempDir()
	writeNodeFiles(t, dir)
	a := newTestApp(t, dir, fixture{}, nil)
	if err := a.SetLinks(codeRepo, nil); err == nil {
		t.Fatal("a code repo was accepted as a tracker")
	}
	if err := a.SetLinks(trackerRepo, []string{"acme/unknown"}); err == nil {
		t.Fatal("an unconfigured code repo was linked")
	}
	if err := a.SetLinks(trackerRepo, []string{codeRepo, codeRepo}); err != nil {
		t.Fatalf("duplicate code repos should be one: %v", err)
	}
	if err := a.SetLinks(trackerRepo, nil); err != nil {
		t.Fatal(err)
	}
	if len(a.Links()[trackerRepo]) != 0 {
		t.Fatal("an empty list did not unlink")
	}
}

func TestLinkedTrackerSeesPullsAndCommits(t *testing.T) {
	dir := t.TempDir()
	writeNodeFiles(t, dir)
	a := newTestApp(t, dir, fixture{}, nil)
	ctx := context.Background()
	linked := a.VisibleLinked(ctx, trackerRepo, Who{Local: true})
	if !reflect.DeepEqual(linked, []string{codeRepo}) {
		t.Fatalf("linked = %v", linked)
	}
	d, err := a.Data(trackerRepo, linked)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Pulls) != 1 || d.Pulls[0].Record.Key != "acme/app#5" {
		t.Fatalf("linked pulls: %+v", d.Pulls)
	}
	if list, err := a.Commits(trackerRepo, nil, CommitQuery{}); err != nil || list.Total != 0 {
		t.Fatalf("a tracker with no code repo linked has commits: %+v, %v", list, err)
	}
	rec, ok := a.Record(trackerRepo, linked, "pull", "acme/app#5")
	if !ok || rec.Repo != codeRepo {
		t.Fatalf("linked pull not found: %v", ok)
	}
}

func TestRefreshThrottles(t *testing.T) {
	dir := t.TempDir()
	writeNodeFiles(t, dir)
	a := newTestApp(t, dir, fixture{}, nil)
	ctx := context.Background()
	first, err := a.Refresh(ctx, codeRepo, "issue", "1")
	if err != nil {
		t.Fatal(err)
	}
	if first.Throttled || !first.Changed || !reflect.DeepEqual(first.Fields, []string{"title"}) {
		t.Fatalf("first refresh: %+v", first)
	}
	if first.Record.Milestone == nil || first.Record.Milestone.DueOn == nil {
		t.Fatal("the version's due date was lost by the read")
	}
	second, err := a.Refresh(ctx, codeRepo, "issue", "1")
	if err != nil {
		t.Fatal(err)
	}
	if !second.Throttled || second.Changed {
		t.Fatalf("a second read in a row was not throttled: %+v", second)
	}
	if _, err := a.Refresh(ctx, codeRepo, "issue", "404"); err == nil {
		t.Fatal("a missing issue was read")
	}
}

func TestConcurrentReadsAndSyncs(t *testing.T) {
	dir := t.TempDir()
	writeNodeFiles(t, dir)
	a := newTestApp(t, dir, fixture{role: provider.RoleEditor}, nil)
	ctx := context.Background()
	linked := a.VisibleLinked(ctx, trackerRepo, Who{Local: true})
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 15; i++ {
				if _, err := a.Data(codeRepo, nil); err != nil {
					t.Error(err)
				}
				if _, err := a.Data(trackerRepo, linked); err != nil {
					t.Error(err)
				}
				a.RunSync(codeRepo, i%5 == 0)
				a.Search(codeRepo, nil, "first", provider.Visibility{})
				a.Search(trackerRepo, linked, "fix", provider.Visibility{})
				a.Record(codeRepo, nil, "issue", "1")
				a.Commits(codeRepo, nil, CommitQuery{Limit: 10})
				if _, err := a.Commits(trackerRepo, linked, CommitQuery{PR: "app#5"}); err != nil {
					t.Error(err)
				}
				a.Visibility(ctx, codeRepo, Who{Local: true})
			}
		}()
	}
	wg.Wait()
	waitIdle(t, a, codeRepo)
}

func TestDerivedSearchFindsIssues(t *testing.T) {
	dir := t.TempDir()
	writeNodeFiles(t, dir)
	a := newTestApp(t, dir, fixture{}, nil)
	rs := a.Search(codeRepo, nil, "first", provider.Visibility{})
	if len(rs) == 0 {
		t.Fatal("search found nothing")
	}
	var _ []derive.SearchResult = rs
}
