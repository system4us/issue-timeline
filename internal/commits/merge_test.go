package commits

import "testing"

func TestClassifyGitHubMerge(t *testing.T) {
	cases := []struct {
		name string
		in   MergeFacts
		want string
	}{
		{"two parents is a merge commit", MergeFacts{Parents: 2, Title: "Merge pull request #5", Number: 5}, "merge commit"},
		{"squash titled with (#N)", MergeFacts{Parents: 1, Title: "Add thing (#12)", Number: 12, PRTitle: "Add thing"}, "squash"},
		{"squash titled like the PR", MergeFacts{Parents: 1, Title: "Add thing", PRTitle: "Add thing", Number: 12}, "squash"},
		{"rebase keeps a PR commit's title", MergeFacts{Parents: 1, Title: "first commit", PRTitle: "Other", Number: 12, PRCommitTitles: []string{"first commit", "second"}}, "rebase"},
		{"anything else is a single commit", MergeFacts{Parents: 1, Title: "unrelated", PRTitle: "Other", Number: 12, PRCommitTitles: []string{"x"}}, "single commit"},
		{"(#N) must be the PR's own number", MergeFacts{Parents: 1, Title: "Add thing (#120)", Number: 12, PRTitle: "x"}, "single commit"},
	}
	for _, c := range cases {
		if got := ClassifyGitHubMerge(c.in); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
