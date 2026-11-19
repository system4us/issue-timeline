package ci

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"gitlab.com/system4us/issue-timeline/internal/model"
)

func sp(s string) *string { return &s }

// run builds a run the way the hosts normalize it; conclusion "" is a run still going.
func run(id, sha, category, conclusion, url, at string) model.CIRun {
	r := model.CIRun{ID: id, Sha: sha, Category: category, URL: url, At: at, Status: "completed", Event: "push"}
	if conclusion != "" {
		r.Conclusion = sp(conclusion)
	}
	return r
}

func pull(number string, commits ...string) *model.Record {
	r := &model.Record{Kind: "pull", Key: number}
	for _, c := range commits {
		r.Commits = append(r.Commits, model.PullCommit{Oid: c})
	}
	return r
}

func TestGitLabMergeRequestPipelineIsTheMergeRequestsCI(t *testing.T) {
	// The GitLab pipeline of an MR is built by the gitlab package; here the normalized run it gives.
	mr := model.CIRun{ID: "1", URL: "u", Name: "pipeline", Event: "pull_request", Branch: sp("pages-redesign"), Sha: "h7", Status: "failed", Conclusion: sp("failed"), At: "2026-01-01T10:00:00Z"}
	if got := Categorize(mr); got != "ci" {
		t.Fatalf("an MR pipeline on a branch named pages-redesign must be CI, got %s", got)
	}
	mr.Category = "ci"
	s := Summarize([]model.CIRun{mr}, map[string]*model.Record{"7": pull("7", "h7")})
	if s.ByPr["7"].State != "failed" {
		t.Fatalf("state = %s", s.ByPr["7"].State)
	}
}

func TestCategories(t *testing.T) {
	cases := []struct {
		run  model.CIRun
		want string
	}{
		{model.CIRun{Name: "CI", Path: ".github/workflows/ci.yml", Event: "pull_request"}, "ci"},
		{model.CIRun{Name: "CI", Path: ".github/workflows/ci.yml", Event: "push"}, "ci"},
		{model.CIRun{Name: "npm_and_yarn in /site", Path: "dynamic/dependabot/dependabot-updates", Event: "dynamic", Actor: "dependabot[bot]"}, "dependabot"},
		{model.CIRun{Name: "pages build and deployment", Path: "dynamic/pages/pages-build-deployment", Event: "dynamic"}, "pages"},
		{model.CIRun{Name: "Release", Path: ".github/workflows/release.yml", Event: "push"}, "other"},
		{model.CIRun{Name: "Tag", Path: ".github/workflows/tag.yml", Event: "push"}, "other"},
		// whole words: a workflow called Stage is still CI, a deploy is not
		{model.CIRun{Name: "Stage", Path: ".github/workflows/stage.yml", Event: "pull_request"}, "ci"},
		{model.CIRun{Name: "Deploy docs", Path: ".github/workflows/docs.yml", Event: "push"}, "other"},
		{model.CIRun{Name: "CI", Path: ".github/workflows/publish.yml", Event: "push"}, "other"},
		{model.CIRun{Name: "CI", Path: ".github/workflows/ci.yml", Event: "merge_group"}, "ci"},
		{model.CIRun{Name: "CI", Path: ".github/workflows/ci.yml", Event: "dynamic"}, "other"},
	}
	for _, c := range cases {
		if got := Categorize(c.run); got != c.want {
			t.Errorf("Categorize(%+v) = %s, want %s", c.run, got, c.want)
		}
	}
}

func TestCategorizeWithAVocabularyList(t *testing.T) {
	// VOCABULARY_FILE with ci.otherWorkflows ["nightly"] replaces the default words.
	nightly := OtherWorkflowsRe([]string{"nightly"})
	r := model.CIRun{Name: "Nightly build", Path: ".github/workflows/n.yml", Event: "schedule"}
	if got := CategorizeWith(r, nightly); got != "other" {
		t.Fatalf("nightly = %s", got)
	}
	r = model.CIRun{Name: "Release", Path: ".github/workflows/release.yml", Event: "push"}
	if got := CategorizeWith(r, nightly); got != "ci" {
		t.Fatalf("the replaced list no longer has release: %s", got)
	}
	if got := CategorizeWith(model.CIRun{Name: "Release", Event: "push"}, OtherWorkflowsRe(nil)); got != "ci" {
		t.Fatalf("an empty list matches nothing: %s", got)
	}
}

func TestOtherWorkflowsWordsAreEscapedAndUnicodeAware(t *testing.T) {
	re := OtherWorkflowsRe([]string{"a.b", "bloqué"})
	if !re.MatchString("run a.b now") || re.MatchString("run axb now") {
		t.Fatal("regular expression characters in a word must be literal")
	}
	if !re.MatchString("x bloqué") || re.MatchString("xbloqué") {
		t.Fatal("a word needs a boundary, also where the letter is not ASCII")
	}
}

func TestStates(t *testing.T) {
	cases := map[string]string{
		"": "running", "success": "passed", "failure": "failed", "skipped": "skipped", "cancelled": "cancelled",
		"canceled": "cancelled", "timed_out": "failed", "action_required": "pending", "neutral": "passed", "stale": "cancelled",
		"something_new": "something_new",
	}
	for conclusion, want := range cases {
		r := model.CIRun{}
		if conclusion != "" {
			r.Conclusion = sp(conclusion)
		}
		if got := StateOf(r); got != want {
			t.Errorf("StateOf(%q) = %s, want %s", conclusion, got, want)
		}
	}
}

func TestPullRequestGetsTheStateOfItsLastCIRun(t *testing.T) {
	runs := []model.CIRun{
		run("1", "h7", "ci", "failure", "u1", "2026-01-01T10:00:00Z"),
		run("2", "h7", "ci", "success", "u2", "2026-01-01T11:00:00Z"),
		run("3", "h8", "ci", "skipped", "u3", "2026-01-02T10:00:00Z"),
		run("4", "h7", "dependabot", "success", "u4", "2026-01-03T10:00:00Z"),
		run("5", "main1", "pages", "success", "u5", "2026-01-03T11:00:00Z"),
	}
	runs[4].Name = "pages"
	pulls := map[string]*model.Record{
		"7": pull("7", "h7"),
		"8": pull("8", "h8"),
		"9": pull("9"),
	}
	s := Summarize(runs, pulls)
	if !reflect.DeepEqual(s.ByPr["7"], PullCI{State: "passed", URL: "u2", At: "2026-01-01T11:00:00Z", Count: 2}) {
		t.Fatalf("PR 7 = %+v", s.ByPr["7"])
	}
	if !reflect.DeepEqual(s.ByPr["8"], PullCI{State: "skipped", URL: "u3", At: "2026-01-02T10:00:00Z", Count: 1}) {
		t.Fatalf("PR 8 = %+v", s.ByPr["8"])
	}
	if _, ok := s.ByPr["9"]; ok {
		t.Fatal("a PR without runs has no CI entry")
	}
	if s.Other["dependabot"].Count != 1 {
		t.Fatalf("dependabot = %+v", s.Other["dependabot"])
	}
	if s.Other["pages"].Last == nil || s.Other["pages"].Last.URL != "u5" || s.Other["pages"].Last.Name != "pages" {
		t.Fatalf("pages = %+v", s.Other["pages"])
	}
	if s.LastRunAt == nil || *s.LastRunAt != "2026-01-03T11:00:00Z" {
		t.Fatalf("lastRunAt = %v", s.LastRunAt)
	}
}

func TestSummaryJSONIsTheNodeShape(t *testing.T) {
	empty := Summarize(nil, nil)
	b, err := json.Marshal(empty)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"byPr":{},"other":{},"lastRunAt":null}` {
		t.Fatalf("an empty summary: %s", b)
	}
	runs := []model.CIRun{run("1", "h7", "ci", "success", "u2", "2026-01-01T11:00:00Z")}
	runs[0].Name = "CI"
	b, _ = json.Marshal(Summarize(runs, map[string]*model.Record{"7": pull("7", "h7")}))
	want := `{"byPr":{"7":{"state":"passed","url":"u2","at":"2026-01-01T11:00:00Z","count":1}},"other":{},"lastRunAt":"2026-01-01T11:00:00Z"}`
	if string(b) != want {
		t.Fatalf("summary JSON:\n%s\nwant\n%s", b, want)
	}
}

func TestOtherWorkflowKeepsItsLastRunWithItsState(t *testing.T) {
	runs := []model.CIRun{
		run("1", "a", "other", "failure", "u1", "2026-01-01T10:00:00Z"),
		run("2", "b", "other", "", "u2", "2026-01-03T10:00:00Z"),
	}
	runs[0].Name = "Release"
	runs[1].Name = "Tag"
	s := Summarize(runs, nil)
	if s.Other["other"].Count != 2 || s.Other["other"].Last.URL != "u2" || s.Other["other"].Last.State != "running" || s.Other["other"].Last.Name != "Tag" {
		t.Fatalf("other = %+v", s.Other["other"])
	}
}

func TestWindowIsAlwaysCIDaysBackAndSyncReadsFromADayBeforeTheLastOne(t *testing.T) {
	t.Setenv("CI_DAYS", "")
	now := time.UnixMilli(time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC).UnixMilli())
	floor := now.Add(-90 * 24 * time.Hour)
	if got := WindowFloor(now); !got.Equal(floor) {
		t.Fatalf("WindowFloor = %v, want %v", got, floor)
	}
	st := &Store{}
	if got := ReadFrom(now, st); !got.Equal(floor) {
		t.Fatalf("a store never synced reads from the window: %v", got)
	}
	st.meta.LastSync = sp("2026-02-28T12:00:00Z")
	want := time.Date(2026, 2, 27, 12, 0, 0, 0, time.UTC)
	if got := ReadFrom(now, st); !got.Equal(want) {
		t.Fatalf("ReadFrom after a sync = %v, want %v", got, want)
	}
	st.meta.LastSync = sp("2025-01-01T00:00:00Z")
	if got := ReadFrom(now, st); !got.Equal(floor) {
		t.Fatal("an old last sync cannot reach before the window")
	}
}

func TestCIDaysFromTheEnvironment(t *testing.T) {
	t.Setenv("CI_DAYS", "30")
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	if got := WindowFloor(now); !got.Equal(now.Add(-30 * 24 * time.Hour)) {
		t.Fatalf("CI_DAYS=30: %v", got)
	}
	t.Setenv("CI_DAYS", "nonsense")
	if got := WindowFloor(now); !got.Equal(now.Add(-90 * 24 * time.Hour)) {
		t.Fatalf("a CI_DAYS that is not a number keeps 90: %v", got)
	}
}
