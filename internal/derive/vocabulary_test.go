package derive

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"gitlab.com/system4us/issue-timeline/internal/model"
)

// numRec is an issue keyed by its number, the way the stores keep it.
func numRec(n int, body string) *model.Record {
	return &model.Record{Kind: "issue", ID: strconv.Itoa(n), Key: strconv.Itoa(n), Number: model.Ptr(n), Body: body}
}

// depsOf is "blocker->blocked" for each dependency, sorted, of a set of issues read with vocab.
func depsOf(vocab Vocabulary, issues ...*model.Record) []string {
	m := map[string]*model.Record{}
	for _, r := range issues {
		m[r.Key] = r
	}
	var out []string
	for _, e := range deriveDeps(m, vocab) {
		out = append(out, e.Blocker+"->"+e.Blocked)
	}
	sort.Strings(out)
	return out
}

func equalStrings(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestDefaultsReadDependenciesInFiveLanguages(t *testing.T) {
	issues := []*model.Record{
		numRec(1, ""), numRec(2, ""), numRec(3, ""), numRec(4, ""), numRec(5, ""), numRec(6, ""),
		numRec(10, "Depends on #1"),
		numRec(11, "Bloqueado por #2"),
		numRec(12, "Depende de #3"),
		numRec(13, "Bloqué par #4"),
		numRec(14, "Bloqueado pelo #5"),
		numRec(15, "Abhängig von #6"),
		numRec(16, "Blocks #2"), // "Blocks #2" in 16 says 16 blocks 2: the blocker is 16
	}
	equalStrings(t, depsOf(DefaultVocabulary(), issues...), []string{"1->10", "16->2", "2->11", "3->12", "4->13", "5->14", "6->15"})
}

func TestBlockerReadToEndOfSentence(t *testing.T) {
	issues := []*model.Record{
		numRec(95, ""), numRec(7, ""),
		numRec(20, "Depende de F2a y del outbox (#95). Ver #7 para contexto."),
	}
	equalStrings(t, depsOf(DefaultVocabulary(), issues...), []string{"95->20"})
}

func TestCustomVocabularyReplacesTheListsItNames(t *testing.T) {
	vocab := Vocabulary{BlockedBy: []string{"waits for"}, Blocks: []string{"unblocks"}}
	issues := []*model.Record{
		numRec(1, ""), numRec(2, ""), numRec(3, "Waits for #1"), numRec(4, "Unblocks #2"), numRec(5, "Depends on #1"),
	}
	equalStrings(t, depsOf(vocab, issues...), []string{"1->3", "4->2"}) // the defaults no longer apply
}

func TestPhrasesAreLiteral(t *testing.T) {
	vocab := Vocabulary{BlockedBy: []string{"need (urgent)"}}
	issues := []*model.Record{numRec(1, ""), numRec(2, "Need (urgent) #1"), numRec(3, "Needs urgent #1")}
	equalStrings(t, depsOf(vocab, issues...), []string{"1->2"})
}

func TestLoadVocabularyAppliesFileOverDefaults(t *testing.T) {
	file := filepath.Join(t.TempDir(), "vocab.json")
	if err := os.WriteFile(file, []byte(`{"blockedBy":["espera a"],"ci":{"otherWorkflows":["nightly"]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	v, err := LoadVocabulary(file)
	if err != nil {
		t.Fatal(err)
	}
	equalStrings(t, v.BlockedBy, []string{"espera a"})
	equalStrings(t, v.CIOtherWorkflows, []string{"nightly"})
	equalStrings(t, v.Blocks, DefaultVocabulary().Blocks) // lists the file does not name keep the defaults
	if !contains(DefaultVocabulary().BlockedBy, "depends on") {
		t.Fatal("the defaults lost 'depends on'")
	}
}

func TestLoadVocabularyEmptyNameIsTheDefault(t *testing.T) {
	v, err := LoadVocabulary("")
	if err != nil {
		t.Fatal(err)
	}
	equalStrings(t, v.FieldKeys, DefaultVocabulary().FieldKeys)
	equalStrings(t, v.CIOtherWorkflows, DefaultVocabulary().CIOtherWorkflows)
}

func TestLoadVocabularyRejectsAFileThatIsNotJSON(t *testing.T) {
	file := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(file, []byte(`{"blockedBy": [`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadVocabulary(file); err == nil {
		t.Fatal("an invalid file must be an error")
	}
	if _, err := LoadVocabulary(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("a missing file must be an error")
	}
}

func TestDefaultVocabularyIsACopy(t *testing.T) {
	a := DefaultVocabulary()
	a.BlockedBy[0] = "changed"
	if DefaultVocabulary().BlockedBy[0] == "changed" {
		t.Fatal("DefaultVocabulary must return its own copy")
	}
}

func TestVocabularyPayloadKeysAreTheThreeLists(t *testing.T) {
	v := Vocabulary{BlockedBy: []string{"a"}}
	b := mustMarshal(t, cloneVocabulary(v)) // what BuildPayload writes: no list is null
	if strings.Contains(string(b), "otherWorkflows") || strings.Contains(string(b), "CIOther") {
		t.Fatalf("the CI list must not be in the payload: %s", b)
	}
	if !strings.Contains(string(b), `"blocks":[]`) {
		t.Fatalf("an empty list must be written as [], not null: %s", b)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
