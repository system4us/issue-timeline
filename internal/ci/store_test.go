package ci

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"gitlab.com/system4us/issue-timeline/internal/model"
)

func readFileJSON(t *testing.T, file string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// A CI file written by the Node server (ci.js CiStore.save) is read, and written back with the same fields.
func TestStoreFileKeepsTheNodeShape(t *testing.T) {
	file := filepath.Join(t.TempDir(), "repo.ci.json")
	js := `{"meta":{"repo":"owner/repo","lastSync":"2026-01-02T03:04:05.000Z"},` +
		`"runs":[{"id":"9","url":"u9","name":"CI","path":".github/workflows/ci.yml","event":"push","actor":"ana","branch":"main","sha":"abc","status":"completed","conclusion":"success","at":"2026-01-02T03:00:00Z","category":"ci"}],` +
		`"changes":[{"sha":"def","title":"touch ci","author":"ana","at":"2026-01-01T00:00:00Z","url":"https://x/def"}]}`
	if err := os.WriteFile(file, []byte(js), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	runs := st.Runs()
	if len(runs) != 1 || runs[0].ID != "9" || *runs[0].Conclusion != "success" || *runs[0].Branch != "main" || runs[0].Category != "ci" {
		t.Fatalf("runs = %+v", runs)
	}
	if ch := st.Changes(); len(ch) != 1 || ch[0].Title != "touch ci" || *ch[0].URL != "https://x/def" {
		t.Fatalf("changes = %+v", ch)
	}
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}
	written := readFileJSON(t, file)
	if !reflect.DeepEqual(keysOf(written), []string{"changes", "meta", "runs"}) {
		t.Fatalf("top-level keys = %v", keysOf(written))
	}
	meta := written["meta"].(map[string]any)
	if meta["repo"] != "owner/repo" || meta["lastSync"] != "2026-01-02T03:04:05.000Z" {
		t.Fatalf("meta = %v", meta)
	}
	r := written["runs"].([]any)[0].(map[string]any)
	for k, want := range map[string]any{"id": "9", "url": "u9", "name": "CI", "path": ".github/workflows/ci.yml", "event": "push",
		"actor": "ana", "branch": "main", "sha": "abc", "status": "completed", "conclusion": "success", "at": "2026-01-02T03:00:00Z", "category": "ci"} {
		if r[k] != want {
			t.Errorf("run.%s = %v, want %v", k, r[k], want)
		}
	}
	c := written["changes"].([]any)[0].(map[string]any)
	if c["sha"] != "def" || c["title"] != "touch ci" || c["author"] != "ana" || c["url"] != "https://x/def" {
		t.Fatalf("change = %v", c)
	}
}

func keysOf(m map[string]any) []string {
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func TestEmptyWindowWritesEmptyLists(t *testing.T) {
	file := filepath.Join(t.TempDir(), "sub", "x.ci.json")
	st, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}
	written := readFileJSON(t, file)
	if _, ok := written["runs"].([]any); !ok {
		t.Fatalf("runs must be a list: %v", written["runs"])
	}
	if _, ok := written["changes"].([]any); !ok {
		t.Fatalf("changes must be a list: %v", written["changes"])
	}
}

func TestMergeKeepsTheWindowAndReplacesTheSameIDs(t *testing.T) {
	st, _ := Open(filepath.Join(t.TempDir(), "x.ci.json"))
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	floor := now.Add(-90 * 24 * time.Hour)
	old := run("0", "a", "ci", "success", "u0", "2025-01-01T00:00:00Z") // before the window: dropped
	st.Merge("o/r", []model.CIRun{old}, floor, now)
	if len(st.Runs()) != 0 {
		t.Fatal("a run before the window is not kept")
	}
	st.Merge("o/r", []model.CIRun{
		run("1", "a", "ci", "failure", "u1", "2026-02-01T10:00:00Z"),
		run("2", "b", "ci", "success", "u2", "2026-02-02T10:00:00Z"),
	}, floor, now)
	st.Merge("o/r", []model.CIRun{run("1", "a", "ci", "success", "u1b", "2026-02-01T10:00:00Z")}, floor, now.Add(time.Hour))
	runs := st.Runs()
	if len(runs) != 2 || runs[0].ID != "2" || runs[1].ID != "1" || runs[1].URL != "u1b" {
		t.Fatalf("runs = %+v", runs)
	}
	if m := st.Meta(); *m.Repo != "o/r" || *m.LastSync != "2026-03-01T01:00:00.000Z" {
		t.Fatalf("meta = %+v", m)
	}
	// Runs that fell out of the window are dropped on the next merge.
	st.Merge("o/r", nil, time.Date(2026, 2, 2, 0, 0, 0, 0, time.UTC), now)
	if got := st.Runs(); len(got) != 1 || got[0].ID != "2" {
		t.Fatalf("after the window moved: %+v", got)
	}
}

func TestAnotherRepoStartsAgainAndLosesItsChanges(t *testing.T) {
	st, _ := Open(filepath.Join(t.TempDir(), "x.ci.json"))
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	floor := now.Add(-90 * 24 * time.Hour)
	st.Merge("one/r", []model.CIRun{run("1", "a", "ci", "success", "u", "2026-02-01T10:00:00Z")}, floor, now)
	st.SetChanges([]model.WorkflowChange{{Sha: "c", Title: "t"}})
	st.Merge("two/r", nil, floor, now)
	if len(st.Runs()) != 0 || len(st.Changes()) != 0 {
		t.Fatalf("the runs and changes of another repo are dropped: %v %v", st.Runs(), st.Changes())
	}
	st.SetChanges([]model.WorkflowChange{{Sha: "c", Title: "t"}})
	st.Merge("two/r", nil, floor, now.Add(time.Minute))
	if len(st.Changes()) != 1 {
		t.Fatal("a sync of the same repo keeps its changes")
	}
}

func TestStoreIsSafeForConcurrentSyncAndReads(t *testing.T) {
	st, _ := Open(filepath.Join(t.TempDir(), "x.ci.json"))
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	floor := now.Add(-90 * 24 * time.Hour)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			st.Merge("o/r", []model.CIRun{run(string(rune('a'+i)), "s", "ci", "success", "u", "2026-02-01T10:00:00Z")}, floor, now)
			_ = st.Save()
		}(i)
		go func() {
			defer wg.Done()
			_ = st.Runs()
			_ = Summarize(st.Runs(), nil)
		}()
	}
	wg.Wait()
}
