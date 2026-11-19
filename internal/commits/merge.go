package commits

import (
	"fmt"
	"strings"
)

// MergeFacts is what a merged GitHub pull request and its merge commit say, for ClassifyGitHubMerge.
type MergeFacts struct {
	Parents        int      // parents of the merge commit
	Title          string   // first line of the merge commit's message
	PRTitle        string   // the pull request's title
	Number         int      // the pull request's number
	PRCommitTitles []string // the messages of the pull request's own commits
}

// ClassifyGitHubMerge tells how a merged PR landed on the default branch, from its merge commit: a merge commit has two
// parents; a squash is one commit titled like the PR (with "(#N)"); a rebase is one commit per PR commit, each keeping
// its own title; anything else is a single commit.
func ClassifyGitHubMerge(in MergeFacts) string {
	if in.Parents >= 2 {
		return "merge commit"
	}
	if strings.Contains(in.Title, fmt.Sprintf("(#%d)", in.Number)) || in.Title == in.PRTitle {
		return "squash"
	}
	for _, t := range in.PRCommitTitles {
		if t == in.Title {
			return "rebase"
		}
	}
	return "single commit"
}
