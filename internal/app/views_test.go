package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitlab.com/system4us/issue-timeline/internal/provider"
)

func statusOf(err error) int {
	if pe, ok := err.(*provider.Error); ok {
		return pe.Status
	}
	return 0
}

func newViewStore(t *testing.T) (*viewStore, string) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "views.json")
	v := loadViews(file)
	tick := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	v.now = func() time.Time { tick = tick.Add(time.Minute); return tick }
	return v, file
}

var sampleState = json.RawMessage(`{"query":"state=open","groupBy":"asignado"}`)

func TestAViewIsSavedListedAndSurvivesARestart(t *testing.T) {
	v, file := newViewStore(t)
	saved, err := v.put("acme/app", "ana", "  Open bugs ", sampleState)
	if err != nil || saved.Name != "Open bugs" || saved.Owner != "ana" || saved.ID == "" {
		t.Fatalf("%+v, %v", saved, err)
	}
	if _, err := v.put("acme/app", "luis", "Roadmap", sampleState); err != nil {
		t.Fatal(err)
	}
	if _, err := v.put("acme/other", "ana", "Elsewhere", sampleState); err != nil {
		t.Fatal(err)
	}
	got := loadViews(file).list("acme/app") // read back from the file
	if len(got) != 2 || got[0].Name != "Open bugs" || got[1].Name != "Roadmap" {
		t.Fatalf("list: %+v", got)
	}
	if string(got[0].State) != string(sampleState) {
		t.Fatalf("the state is kept as it came: %s", got[0].State)
	}
	if len(loadViews(file).list("acme/none")) != 0 {
		t.Fatal("a repo with no views lists none")
	}
}

func TestTheSameNameByTheSamePersonReplacesItAndByAnotherIs409(t *testing.T) {
	v, _ := newViewStore(t)
	first, _ := v.put("r", "ana", "Mine", json.RawMessage(`{"a":1}`))
	again, err := v.put("r", "ana", "MINE", json.RawMessage(`{"a":2}`))
	if err != nil || again.ID != first.ID || string(again.State) != `{"a":2}` || again.UpdatedAt <= first.UpdatedAt || again.CreatedAt != first.CreatedAt {
		t.Fatalf("replace: %+v (first %+v), %v", again, first, err)
	}
	if len(v.list("r")) != 1 {
		t.Fatal("a replacement is not a second view")
	}
	if _, err := v.put("r", "luis", "mine", sampleState); statusOf(err) != 409 {
		t.Fatalf("another person's name must be a 409, got %v", err)
	}
}

func TestOnlyTheOwnerOrTheLocalMachineDeletes(t *testing.T) {
	v, _ := newViewStore(t)
	a, _ := v.put("r", "ana", "A", sampleState)
	b, _ := v.put("r", "ana", "B", sampleState)
	if err := v.remove("r", a.ID, "luis", false); statusOf(err) != 403 {
		t.Fatalf("another person: %v", err)
	}
	if err := v.remove("r", a.ID, "ana", false); err != nil {
		t.Fatalf("the owner: %v", err)
	}
	if err := v.remove("r", b.ID, LocalOwner, true); err != nil {
		t.Fatalf("the local machine: %v", err)
	}
	if err := v.remove("r", a.ID, "ana", false); statusOf(err) != 404 {
		t.Fatalf("already gone: %v", err)
	}
	if len(v.list("r")) != 0 {
		t.Fatal("nothing should be left")
	}
}

func TestWhatIsNotAViewIsRefused(t *testing.T) {
	v, _ := newViewStore(t)
	cases := map[string]struct {
		name  string
		state string
		want  int
	}{
		"no name":         {"  ", `{}`, 400},
		"long name":       {strings.Repeat("x", viewNameMax+1), `{}`, 400},
		"control chars":   {"a\x00b", `{}`, 400},
		"not an object":   {"n", `[1]`, 400},
		"a string":        {"n", `"x"`, 400},
		"null":            {"n", `null`, 400},
		"not json":        {"n", `{`, 400},
		"too large state": {"n", `{"x":"` + strings.Repeat("a", viewStateMax) + `"}`, 413},
	}
	for name, c := range cases {
		if _, err := v.put("r", "ana", c.name, json.RawMessage(c.state)); statusOf(err) != c.want {
			t.Errorf("%s: status %d (%v), want %d", name, statusOf(err), err, c.want)
		}
	}
	if len(v.list("r")) != 0 {
		t.Fatal("a refused view is not kept")
	}
}

func TestARepoKeepsAtMostAHundredViews(t *testing.T) {
	v, _ := newViewStore(t)
	for i := 0; i < viewsPerRepo; i++ {
		if _, err := v.put("r", "ana", fmt.Sprintf("v%d", i), sampleState); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := v.put("r", "ana", "one more", sampleState); statusOf(err) != 400 {
		t.Fatalf("the 101st: %v", err)
	}
	if _, err := v.put("r", "ana", "v7", json.RawMessage(`{"x":1}`)); err != nil {
		t.Fatalf("replacing one is still fine: %v", err)
	}
}

func TestRemovingARepoDropsItsViews(t *testing.T) {
	v, file := newViewStore(t)
	_, _ = v.put("gone", "ana", "A", sampleState)
	_, _ = v.put("kept", "ana", "B", sampleState)
	if err := v.dropRepo("gone"); err != nil {
		t.Fatal(err)
	}
	re := loadViews(file)
	if len(re.list("gone")) != 0 || len(re.list("kept")) != 1 {
		t.Fatalf("gone %v kept %v", re.list("gone"), re.list("kept"))
	}
}

func TestACorruptEntryInTheFileIsDroppedNotTheWholeFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "views.json")
	if err := os.WriteFile(file, []byte(`{"r":[{"id":"1","name":"Good","owner":"a","state":{"q":1}},{"id":"","name":"No id","state":{}},{"id":"3","name":"Bad state","state":[1]}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got := loadViews(file).list("r")
	if len(got) != 1 || got[0].Name != "Good" {
		t.Fatalf("%+v", got)
	}
}
