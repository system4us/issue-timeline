package ci

import (
	"regexp"
	"sort"
	"strings"

	"gitlab.com/system4us/issue-timeline/internal/model"
)

// Categories of a run (what Categorize returns).
const (
	CatCI         = "ci"
	CatDependabot = "dependabot"
	CatPages      = "pages"
	CatOther      = "other"
)

// ciEvents are the triggers that make a run the CI of its commits.
var ciEvents = map[string]bool{
	"pull_request": true, "push": true, "workflow_dispatch": true, "merge_group": true, "pull_request_target": true, "schedule": true,
}

// defaultOtherWorkflows is ci.otherWorkflows of vocabulary.json: the words that make a run "other" (releases, tags,
// deploys). A VOCABULARY_FILE that replaces the list is compiled with OtherWorkflowsRe and given to CategorizeWith.
var defaultOtherWorkflows = []string{"release", "tag", "deploy", "publish", "pages", "dependabot"}

var (
	dependabotRe = regexp.MustCompile(`(?i)dependabot`)
	pagesRe      = regexp.MustCompile(`(?i)pages`)
	otherRe      = OtherWorkflowsRe(defaultOtherWorkflows)
)

// OtherWorkflowsRe compiles a list of words into the matcher of "other" workflows. Words match whole (a word is letters,
// digits and "_" around it), case-insensitively. Go's regexp has no lookbehind, so the boundary is matched as part of the
// pattern: the match itself only tests that one exists. An empty list matches nothing.
func OtherWorkflowsRe(phrases []string) *regexp.Regexp {
	seen := map[string]bool{}
	var list []string
	for _, p := range phrases {
		p = strings.TrimSpace(p)
		if p != "" && !seen[p] {
			seen[p] = true
			list = append(list, p)
		}
	}
	if len(list) == 0 {
		return regexp.MustCompile(`[^\s\S]`)
	}
	sort.SliceStable(list, func(i, j int) bool { return len(list[i]) > len(list[j]) })
	for i, p := range list {
		list[i] = regexp.QuoteMeta(p)
	}
	return regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}_])(?:` + strings.Join(list, "|") + `)(?:[^\p{L}\p{N}_]|$)`)
}

// Categorize is the category of a run: "ci", "dependabot", "pages", or "other" (releases, tags, deploys: the default
// words). Words are matched whole, so a workflow called "Stage" is not a tag.
func Categorize(run model.CIRun) string {
	return CategorizeWith(run, otherRe)
}

// CategorizeWith is Categorize with the matcher of the other workflows (see OtherWorkflowsRe).
func CategorizeWith(run model.CIRun, other *regexp.Regexp) string {
	if dependabotRe.MatchString(run.Path + " " + run.Actor) {
		return CatDependabot
	}
	if pagesRe.MatchString(run.Path + " " + run.Name) {
		return CatPages
	}
	if other.MatchString(run.Name + " " + run.Path) {
		return CatOther
	}
	if ciEvents[run.Event] {
		return CatCI
	}
	return CatOther
}

// stateNames maps a GitHub conclusion to the state the page shows.
var stateNames = map[string]string{
	"success": "passed", "failure": "failed", "failed": "failed", "skipped": "skipped", "cancelled": "cancelled", "canceled": "cancelled",
	"timed_out": "failed", "action_required": "pending", "neutral": "passed", "stale": "cancelled",
}

// StateOf is the state a run shows: "running" while it has no conclusion yet, otherwise the mapped conclusion.
func StateOf(run model.CIRun) string {
	if run.Conclusion == nil || *run.Conclusion == "" {
		return "running"
	}
	if s, ok := stateNames[*run.Conclusion]; ok {
		return s
	}
	return *run.Conclusion
}

// PullCI is the CI of one pull request: the state of its last CI run, with the link to it and how many runs its commits
// had.
type PullCI struct {
	State string `json:"state"`
	URL   string `json:"url"`
	At    string `json:"at"`
	Count int    `json:"count"`
}

// OtherRun is the last run of an other kind of workflow.
type OtherRun struct {
	URL   string `json:"url"`
	At    string `json:"at"`
	State string `json:"state"`
	Name  string `json:"name"`
}

// OtherCI is one other kind of workflow: how many runs, and the last one.
type OtherCI struct {
	Count int       `json:"count"`
	Last  *OtherRun `json:"last"`
}

// Summary is what the API serves as a repo's CI: each pull request's last CI run, the other workflows, and when the
// last run was created. The commits that changed the CI definition are added by the caller.
type Summary struct {
	ByPr      map[string]PullCI  `json:"byPr"`
	Other     map[string]OtherCI `json:"other"`
	LastRunAt *string            `json:"lastRunAt"`
}

// Summarize gives each pull request the state of its last "ci" run (only the runs of its commits count), and summarizes
// the other kinds apart. pulls is the code repo's pulls by key.
func Summarize(runs []model.CIRun, pulls map[string]*model.Record) Summary {
	prOfSha := map[string]string{}
	for _, k := range sortedKeys(pulls) {
		p := pulls[k]
		if p == nil {
			continue
		}
		for _, c := range p.Commits {
			if _, ok := prOfSha[c.Oid]; !ok {
				prOfSha[c.Oid] = p.Key
			}
		}
	}

	type ciEntry struct {
		count int
		last  *model.CIRun
	}
	byPr := map[string]*ciEntry{}
	for i := range runs {
		run := &runs[i]
		if run.Category != CatCI {
			continue
		}
		pr, ok := prOfSha[run.Sha]
		if !ok {
			continue
		}
		e := byPr[pr]
		if e == nil {
			e = &ciEntry{}
			byPr[pr] = e
		}
		e.count++
		if e.last == nil || atTime(run.At).After(atTime(e.last.At)) {
			e.last = run
		}
	}
	out := Summary{ByPr: map[string]PullCI{}, Other: map[string]OtherCI{}}
	for pr, e := range byPr {
		out.ByPr[pr] = PullCI{State: StateOf(*e.last), URL: e.last.URL, At: e.last.At, Count: e.count}
	}

	type otherEntry struct {
		count int
		last  *OtherRun
	}
	other := map[string]*otherEntry{}
	for _, run := range runs {
		if run.Category == CatCI {
			continue
		}
		e := other[run.Category]
		if e == nil {
			e = &otherEntry{}
			other[run.Category] = e
		}
		e.count++
		if e.last == nil || atTime(run.At).After(atTime(e.last.At)) {
			e.last = &OtherRun{URL: run.URL, At: run.At, State: StateOf(run), Name: run.Name}
		}
	}
	for kind, e := range other {
		out.Other[kind] = OtherCI{Count: e.count, Last: e.last}
	}

	for _, run := range runs {
		if run.At != "" && (out.LastRunAt == nil || atTime(run.At).After(atTime(*out.LastRunAt))) {
			at := run.At
			out.LastRunAt = &at
		}
	}
	return out
}

// sortedKeys lists the keys in the order JS visits an object's integer keys: by number, so for pull numbers the lower
// one wins a commit they share.
func sortedKeys(m map[string]*model.Record) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if len(keys[i]) != len(keys[j]) {
			return len(keys[i]) < len(keys[j])
		}
		return keys[i] < keys[j]
	})
	return keys
}
