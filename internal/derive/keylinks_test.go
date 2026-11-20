package derive

import (
	"reflect"
	"testing"

	"gitlab.com/system4us/issue-timeline/internal/model"
)

func keyTestIssues(keys ...string) map[string]*model.Record {
	m := map[string]*model.Record{}
	for _, k := range keys {
		m[k] = &model.Record{Key: k}
	}
	return m
}

func findAllKeys(t *testing.T, repoKey, text string) []string {
	t.Helper()
	p := KeyPatternFor(repoKey)
	if p == nil {
		t.Fatalf("no pattern for %q", repoKey)
	}
	return p.FindAll(text)
}

func TestJiraKeyOfTheProjectIsFoundInATitleABranchOrACommit(t *testing.T) {
	pattern := KeyPatternFor("jira:https://acme.atlassian.net/PROJ")
	issues := keyTestIssues("PROJ-12", "PROJ-7")
	pulls := []*model.Record{
		{Key: "o/r#1", Title: "PROJ-12: login form", Head: "feature/x"},
		{Key: "o/r#2", Title: "wip", Head: "PROJ-7-fix", Commits: []model.PullCommit{{Message: "fix (PROJ-7)"}}},
		{Key: "o/r#3", Title: "OTHER-12 and PROJ-99 and xPROJ-12", Head: "main"},
	}
	got := DeriveKeyLinks(issues, pulls, pattern)
	want := []Link{
		{Issue: "PROJ-12", Pull: "o/r#1", Via: "key"},
		{Issue: "PROJ-7", Pull: "o/r#2", Via: "key"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v (another project, an unknown key and a key glued to letters are not links)", got, want)
	}
}

func TestRedmineIssueIsNamedByRefsOrFixesNotByABareNumber(t *testing.T) {
	pattern := KeyPatternFor("redmine:https://rm.example.com/backend")
	issues := keyTestIssues("12")
	pulls := []*model.Record{
		{Key: "o/r#5", Title: "Fixes #12 in the form", Head: "x"},
		{Key: "o/r#6", Title: "see #12", Head: "y", Commits: []model.PullCommit{{Message: "refs #12"}}},
	}
	want := []Link{{Issue: "12", Pull: "o/r#5", Via: "key"}, {Issue: "12", Pull: "o/r#6", Via: "key"}}
	if got := DeriveKeyLinks(issues, pulls, pattern); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
}

func TestRepoWithNoKeyedConventionHasNoPattern(t *testing.T) {
	if KeyPatternFor("owner/repo") != nil {
		t.Fatal("a GitHub or GitLab repo has no key pattern")
	}
	if got := DeriveKeyLinks(keyTestIssues("PROJ-12"), []*model.Record{{Key: "1", Title: "PROJ-12"}}, nil); len(got) != 0 {
		t.Fatalf("nothing is linked by key: %+v", got)
	}
	if got := (*KeyPattern)(nil).FindAll("PROJ-12"); got != nil {
		t.Fatalf("a nil pattern finds nothing: %q", got)
	}
}

func TestLinkedTrackerNativeReferencesDoNotLink(t *testing.T) {
	// in a linked tracker the native references of the code repo do not link: its #12 is not the tracker's #12
	trackerIssues := map[string]*model.Record{"12": {Key: "12"}}
	pulls := map[string]*model.Record{"o/r#3": {Key: "o/r#3", Closes: refs("12"), Body: "closes #12"}}
	native := deriveLinks(trackerIssues, pulls, LinkOptions{})
	if len(native) != 1 || native[0].Via != "closes" {
		t.Fatalf("native: %+v", native)
	}
	if got := deriveLinks(trackerIssues, pulls, LinkOptions{Native: model.Ptr(false)}); len(got) != 0 {
		t.Fatalf("native off: %+v", got)
	}
	key := DeriveKeyLinks(trackerIssues, []*model.Record{{Key: "o/r#3", Title: "refs #12"}}, KeyPatternFor("redmine:https://rm.example.com/backend"))
	got := deriveLinks(trackerIssues, pulls, LinkOptions{Native: model.Ptr(false), KeyLinks: key})
	if !reflect.DeepEqual(got, []Link{{Issue: "12", Pull: "o/r#3", Via: "key"}}) {
		t.Fatalf("key links: %+v", got)
	}
}

// Edge cases of the lookbehind and lookahead rewrite of the Jira pattern.

func TestJiraKeysNextToEachOtherAndAtTheEdgesOfText(t *testing.T) {
	const repo = "jira:https://acme.atlassian.net/PROJ"
	cases := []struct {
		text string
		want []string
	}{
		{"PROJ-1 PROJ-2", []string{"PROJ-1", "PROJ-2"}},
		{"(PROJ-1)PROJ-2", []string{"PROJ-1", "PROJ-2"}},
		{"PROJ-1PROJ-2", nil}, // each one touches a word character
		{"PROJ-1 ,PROJ-2.", []string{"PROJ-1", "PROJ-2"}},
		{"PROJ-1", []string{"PROJ-1"}},   // the whole text
		{"PROJ-12", []string{"PROJ-12"}}, // the end of the text is a boundary
		{"xPROJ-12", nil},                // a letter before
		{"PROJ-12a", nil},                // a letter after
		{"PROJ-12-", []string{"PROJ-12"}},
		{"PROJ-12_", nil},      // an underscore after
		{"PROJ-", nil},         // no number
		{"PROJ-12PROJ-3", nil}, // digits run into the next key
		{"PROJ-1 PROJ-1", []string{"PROJ-1", "PROJ-1"}},
	}
	for _, c := range cases {
		got := findAllKeys(t, repo, c.text)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: got %q, want %q", c.text, got, c.want)
		}
	}
}

func TestRedmineKeysAtBoundariesAndSpaces(t *testing.T) {
	const repo = "redmine:https://rm.example.com/backend"
	cases := []struct {
		text string
		want []string
	}{
		{"refs #1,refs #2", []string{"1", "2"}},
		{"refs  #12", []string{"12"}},                 // two spaces
		{"refs #12", []string{"12"}},                  // a no-break space is a space for \s
		{"refs: #12", []string{"12"}},                 // a colon, then a space
		{"refs:#12", nil},                             // a space is needed after the colon
		{"xrefs #1", nil},                             // a letter before
		{"refs #12a", nil},                            // a letter after
		{"refs #12_", nil},                            // an underscore after
		{"refs #12ſ", nil},                            // the long s is a word character for \b
		{"refs #12-", []string{"12"}},                 // a dash after is a boundary
		{"fixes #3 and Fixed #4", []string{"3", "4"}}, // the words are case-insensitive
		{"fixes refs #1", []string{"1"}},              // a phrase that does not end at a key is skipped
		{"refs #1", []string{"1"}},
	}
	for _, c := range cases {
		got := findAllKeys(t, repo, c.text)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: got %q, want %q", c.text, got, c.want)
		}
	}
}

func TestJiraProjectWithoutANameGivesNoPattern(t *testing.T) {
	for _, k := range []string{"jira:https://acme.atlassian.net/", "jira:https://acme.atlassian.net", "jira:PROJ"} {
		if KeyPatternFor(k) != nil {
			t.Errorf("%q: no project name, no pattern", k)
		}
	}
}

func TestTextsOfKeepsTheNonEmptyOnes(t *testing.T) {
	p := &model.Record{Title: "t", Head: "", Commits: []model.PullCommit{{Message: "m1"}, {Message: ""}, {Message: "m2"}}}
	want := []string{"t", "m1", "m2"}
	if got := TextsOf(p); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestDeriveKeyLinksIsOnePerPairAndKnownIssuesOnly(t *testing.T) {
	pattern := KeyPatternFor("jira:https://acme.atlassian.net/PROJ")
	issues := keyTestIssues("PROJ-1")
	pulls := []*model.Record{{Key: "p", Title: "PROJ-1 and PROJ-1", Head: "PROJ-1", Commits: []model.PullCommit{{Message: "PROJ-2"}}}}
	want := []Link{{Issue: "PROJ-1", Pull: "p", Via: "key"}}
	if got := DeriveKeyLinks(issues, pulls, pattern); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
}
