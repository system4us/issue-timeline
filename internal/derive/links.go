package derive

import (
	"regexp"
	"sort"

	"gitlab.com/system4us/issue-timeline/internal/model"
)

// CommitLink is what a commit belongs to: the pull request (nil when none is known) and the issues it names.
type CommitLink struct {
	PR     *string  `json:"pr"`
	Issues []string `json:"issues"`
}

// Commit titles name their pull request in three ways. GitHub: "... (#584)" for a squash, "Merge pull request #584" for
// a merge commit. GitLab: "See merge request group/project!584" (also the title of a merge commit of an MR), where the
// number after "!" is the MR's iid.
var prInTitle = []*regexp.Regexp{
	regexp.MustCompile(`\(#([0-9]+)\)` + jsSpace + `*$`),
	regexp.MustCompile(`^Merge pull request #([0-9]+)\b`),
	regexp.MustCompile(`See merge request ` + notJSSpace + `+!([0-9]+)`),
}

var (
	closingRe = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)` + jsSpace + `*:?` + jsSpace + `+#([0-9]+)`)
	anyRefRe  = regexp.MustCompile(`#([0-9]+)\b`)
)

// BuildCommitLinks maps every commit (by sha) to its pull request and issues. Everything comes from the stores (no call
// to the host). A pull request's own commits are exact, and the pull's closes are the issues it closes; for the others
// the title tells. pulls and issues are the stores' records keyed by key.
func BuildCommitLinks(commits []model.Commit, pulls, issues map[string]*model.Record) map[string]CommitLink {
	// A pull request's own commits, and the commit a merge or squash left on the branch (GitLab's MR says which one it
	// is, whatever the title says). Pulls are visited in key order, so a commit in two pulls goes to the same one.
	prOfSha := map[string]string{}
	for _, k := range sortedKeys(pulls) {
		p := pulls[k]
		for _, c := range p.Commits {
			if _, ok := prOfSha[c.Oid]; !ok {
				prOfSha[c.Oid] = k
			}
		}
		if p.MergeCommitSha != nil && *p.MergeCommitSha != "" {
			if _, ok := prOfSha[*p.MergeCommitSha]; !ok {
				prOfSha[*p.MergeCommitSha] = k
			}
		}
	}
	isPr := func(k string) bool { _, ok := pulls[k]; return ok }
	isIssue := func(k string) bool { _, ok := issues[k]; return ok && !isPr(k) }

	out := make(map[string]CommitLink, len(commits))
	for _, c := range commits {
		title := c.Title
		var pr *string
		if k, ok := prOfSha[c.Sha]; ok {
			pr = &k
		} else {
			for _, re := range prInTitle {
				if m := re.FindStringSubmatch(title); m != nil && isPr(m[1]) {
					k := m[1]
					pr = &k
					break
				}
			}
		}
		found := map[string]bool{}
		if pr != nil {
			for _, n := range pulls[*pr].Closes {
				found[string(n)] = true
			}
		}
		for _, m := range closingRe.FindAllStringSubmatch(title, -1) {
			found[m[1]] = true
		}
		for _, m := range anyRefRe.FindAllStringSubmatch(title, -1) {
			found[m[1]] = true
		}
		list := make([]string, 0, len(found))
		for k := range found {
			if isIssue(k) {
				list = append(list, k)
			}
		}
		sort.Slice(list, func(i, j int) bool { return ByKey(list[i], list[j]) < 0 })
		out[c.Sha] = CommitLink{PR: pr, Issues: list}
	}
	return out
}

func sortedKeys(m map[string]*model.Record) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return ByKey(keys[i], keys[j]) < 0 })
	return keys
}
