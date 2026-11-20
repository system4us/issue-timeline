package derive

import (
	"reflect"
	"strconv"
	"strings"
	"testing"

	"gitlab.com/system4us/issue-timeline/internal/model"
)

func searchRecs() []*model.Record {
	return []*model.Record{
		{Key: "12", Kind: "issue", Title: "Login form", State: "OPEN", Body: "El formulario de login necesita revisión.", Comments: []model.Comment{{Body: "falta validar la contraseña"}}},
		{Key: "13", Kind: "issue", Title: "OAuth provider", State: "CLOSED"},
		{Key: "owner/repo#5", Kind: "pull", Title: "Fix the login form", State: "OPEN", Body: "closes #12"},
	}
}

func keysOf(res []SearchResult) []string {
	out := []string{}
	for _, r := range res {
		out = append(out, r.Key)
	}
	return out
}

func TestAKeyMatchesWithOrWithoutItsHashAndRanksFirst(t *testing.T) {
	if got := Search(searchRecs(), "#12", 50); got[0].Key != "12" {
		t.Fatalf("#12: %+v", got)
	}
	if got := Search(searchRecs(), "12", 50); got[0].Key != "12" {
		t.Fatalf("12: %+v", got)
	}
	if got := Search(searchRecs(), "#5", 50); got[0].Key != "owner/repo#5" {
		t.Fatalf("#5: %+v", got)
	}
}

func TestTitlesRankAboveContentAndContentComesWithASnippet(t *testing.T) {
	found := Search(searchRecs(), "login", 50)
	if !reflect.DeepEqual(keysOf(found), []string{"12", "owner/repo#5"}) {
		t.Fatalf("keys %v: the two titles, the issue first by key", keysOf(found))
	}
	content := Search(searchRecs(), "contraseña", 50)
	if content[0].Key != "12" || content[0].Snippet == nil || !strings.Contains(*content[0].Snippet, "contraseña") {
		t.Fatalf("content: %+v", content)
	}
	if found[0].Snippet != nil {
		t.Fatal("a title match has no snippet")
	}
}

func TestAnEmptyOrUnmatchedQueryFindsNothing(t *testing.T) {
	if got := Search(searchRecs(), "  ", 50); len(got) != 0 || got == nil {
		t.Fatalf("blank: %#v", got)
	}
	if got := Search(searchRecs(), "zzz", 50); len(got) != 0 {
		t.Fatalf("unmatched: %+v", got)
	}
	if got := Search(searchRecs(), "#", 50); len(got) != 0 {
		t.Fatalf("a bare hash: %+v", got)
	}
}

func TestLimitCutsTheResults(t *testing.T) {
	recs := []*model.Record{}
	for i := 1; i <= 60; i++ {
		recs = append(recs, &model.Record{Key: strconv.Itoa(i), Title: "x"})
	}
	if got := Search(recs, "x", 0); len(got) != 50 {
		t.Fatalf("the default limit is 50, got %d", len(got))
	}
	if got := Search(recs, "x", 5); len(got) != 5 || got[4].Key != "5" {
		t.Fatalf("limit 5: %v", keysOf(got))
	}
}

func TestResultsAreSortedByKeyNumerically(t *testing.T) {
	recs := []*model.Record{
		{Key: "10", Title: "login"}, {Key: "9", Title: "login"}, {Key: "owner/repo#5", Title: "login"},
	}
	got := keysOf(Search(recs, "login", 50))
	if !reflect.DeepEqual(got, []string{"9", "10", "owner/repo#5"}) {
		t.Fatalf("got %v", got)
	}
}

func TestSnippetHasEllipsesWhereTheTextGoesOn(t *testing.T) {
	body := strings.Repeat("a ", 60) + "NEEDLE" + strings.Repeat(" b", 60)
	got := Search([]*model.Record{{Key: "1", Body: body}}, "needle", 50)
	if len(got) != 1 || got[0].Snippet == nil {
		t.Fatalf("got %+v", got)
	}
	s := *got[0].Snippet
	if !strings.HasPrefix(s, "…") || !strings.HasSuffix(s, "…") || !strings.Contains(s, "NEEDLE") {
		t.Fatalf("snippet %q", s)
	}
}

func TestSnippetCountsTheTextInJavaScriptUnits(t *testing.T) {
	// 40 emoji are 80 UTF-16 units: the match is at unit 80, so the snippet starts 30 units before it (unit 50, which is
	// 15 emoji), and it ends with the text.
	body := strings.Repeat("😀", 40) + "target"
	got := Search([]*model.Record{{Key: "1", Body: body}}, "target", 50)
	want := "…" + strings.Repeat("😀", 15) + "target"
	if got[0].Snippet == nil || *got[0].Snippet != want {
		t.Fatalf("snippet %q, want %q", deref(got[0].Snippet), want)
	}
}

func TestSnippetCollapsesRunsOfSpaces(t *testing.T) {
	got := Search([]*model.Record{{Key: "1", Body: "one   two\n\n\nthree target"}}, "target", 50)
	if *got[0].Snippet != "one two three target" {
		t.Fatalf("snippet %q", *got[0].Snippet)
	}
}

func TestNilRecordsAreSkipped(t *testing.T) {
	got := Search([]*model.Record{nil, {Key: "1", Title: "x"}}, "x", 50)
	if len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}
