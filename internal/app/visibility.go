package app

import (
	"gitlab.com/system4us/issue-timeline/internal/ci"
	"gitlab.com/system4us/issue-timeline/internal/derive"
	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/provider"
	"strconv"
)

// What a signed-in person may see of a repo, applied to what the server returns. The filters build new slices and
// copies; the stored records are never changed.

// issueAllowed is true when the person may see the issue key (every issue when the visibility lists none).
func issueAllowed(v provider.Visibility, key string) bool {
	return v.Issues == nil || v.Issues[key]
}

// applyVisibilityData filters a repo's payload: the issues not visible, the pulls and their links when code is hidden,
// and the edges (tree, dependencies, links) that name a hidden issue. The CI summary and the workflow changes are
// dropped when code is hidden.
func applyVisibilityData(d Data, v provider.Visibility) Data {
	if v.Issues == nil && !v.HideCode {
		return d
	}
	issues := make([]derive.Light, 0, len(d.Issues))
	for _, l := range d.Issues {
		if issueAllowed(v, l.Record.Key) {
			l.Record = visibleRefs(l.Record, v)
			issues = append(issues, l)
		}
	}
	d.Issues = issues

	links := make([]derive.Link, 0, len(d.Links))
	tree := make([]derive.TreeEdge, 0, len(d.Tree))
	deps := make([]derive.DepEdge, 0, len(d.Deps))
	if v.HideCode {
		d.Pulls = []derive.Light{}
	} else {
		// a new slice: the payload's pulls array is shared with the caller
		pulls := make([]derive.Light, 0, len(d.Pulls))
		for _, l := range d.Pulls {
			l.Record = visibleRefs(l.Record, v)
			pulls = append(pulls, l)
		}
		d.Pulls = pulls
		for _, l := range d.Links {
			if issueAllowed(v, l.Issue) {
				links = append(links, l)
			}
		}
	}
	for _, t := range d.Tree {
		if issueAllowed(v, t.Parent) && issueAllowed(v, t.Child) {
			tree = append(tree, t)
		}
	}
	for _, e := range d.Deps {
		if issueAllowed(v, e.Blocker) && issueAllowed(v, e.Blocked) {
			deps = append(deps, e)
		}
	}
	if v.HideCode {
		links = nil
	}
	d.Links = links
	d.Tree = tree
	d.Deps = deps

	if v.HideCode {
		d.CI = CI{
			Summary: ci.Summary{ByPr: map[string]ci.PullCI{}, Other: map[string]ci.OtherCI{}},
			Changes: []model.WorkflowChange{},
		}
	}
	return d
}

// visibleRecord is the record when the person may see it, with its hidden notes left out (a copy). A pull is hidden when
// code is hidden; an issue when it is not in the visible set.
func visibleRecord(r *model.Record, v provider.Visibility) (*model.Record, bool) {
	if r == nil {
		return nil, false
	}
	if r.Kind == "pull" {
		// HiddenComments is keyed by issue key: a merge request !5 must not lose the notes of issue #5
		if v.HideCode {
			return nil, false
		}
		return visibleRefs(r, v), true
	} else if !issueAllowed(v, r.Key) {
		return nil, false
	}
	out := visibleRefs(r, v)
	hidden := v.HiddenComments[r.Key]
	if len(hidden) == 0 || out.Comments == nil {
		return out, true
	}
	kept := make([]model.Comment, 0, len(out.Comments))
	for _, c := range out.Comments {
		if !hidden[c.ID] {
			kept = append(kept, c)
		}
	}
	if out == r {
		c := *r
		out = &c
	}
	out.Comments = kept
	out.CommentsCount = len(kept)
	return out, true
}

// visibleRefs is the record with the relations that name a hidden issue left out: Parent (set to nil), SubIssues,
// BlockedBy, Blocking and, on a pull, Closes. A record with nothing hidden is returned as it is; otherwise it is a copy,
// and the stored record is never changed. Without an issue list every relation is visible.
func visibleRefs(r *model.Record, v provider.Visibility) *model.Record {
	if v.Issues == nil || r == nil {
		return r
	}
	keep := func(refs []model.Ref) []model.Ref {
		out := make([]model.Ref, 0, len(refs))
		for _, ref := range refs {
			if issueAllowed(v, string(ref)) {
				out = append(out, ref)
			}
		}
		return out
	}
	parentHidden := r.Parent != nil && !issueAllowed(v, string(*r.Parent))
	// Redmine's own relations name both ends by id: the end that is not this record must be visible
	keepRelations := func(rels []model.Relation) []model.Relation {
		out := make([]model.Relation, 0, len(rels))
		for _, rel := range rels {
			other := rel.To
			if strconv.Itoa(rel.To) == r.Key {
				other = rel.From
			}
			if issueAllowed(v, strconv.Itoa(other)) {
				out = append(out, rel)
			}
		}
		return out
	}
	changed := parentHidden || len(keepRelations(r.Relations)) != len(r.Relations) ||
		len(keep(r.SubIssues)) != len(r.SubIssues) ||
		len(keep(r.BlockedBy)) != len(r.BlockedBy) ||
		len(keep(r.Blocking)) != len(r.Blocking) ||
		len(keep(r.Closes)) != len(r.Closes)
	if !changed {
		return r
	}
	c := *r
	if parentHidden {
		c.Parent = nil
	}
	c.SubIssues = keep(r.SubIssues)
	c.BlockedBy = keep(r.BlockedBy)
	c.Blocking = keep(r.Blocking)
	c.Closes = keep(r.Closes)
	if r.Relations != nil {
		c.Relations = keepRelations(r.Relations)
	}
	return &c
}

// visibleResults are the search results the person may see.
func visibleResults(rs []derive.SearchResult, v provider.Visibility) []derive.SearchResult {
	out := make([]derive.SearchResult, 0, len(rs))
	for _, r := range rs {
		if r.Kind == "pull" {
			if v.HideCode {
				continue
			}
		} else if !issueAllowed(v, r.Key) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// visibleCommits is the commit list the person may see: nothing when code is hidden, otherwise the rows with only the
// issue keys they may see.
func visibleCommits(l CommitList, v provider.Visibility) CommitList {
	if v.HideCode {
		return CommitList{Items: []CommitRow{}}
	}
	if v.Issues == nil {
		return l
	}
	items := make([]CommitRow, 0, len(l.Items))
	for _, row := range l.Items {
		kept := make([]string, 0, len(row.Issues))
		for _, k := range row.Issues {
			if issueAllowed(v, k) {
				kept = append(kept, k)
			}
		}
		row.Issues = kept
		items = append(items, row)
	}
	l.Items = items
	return l
}

// visibleCommit is one commit's detail as the person may see it: nothing when code is hidden, and only the issues they
// may see among the ones the commit names.
func visibleCommit(d CommitDetail, v provider.Visibility) (CommitDetail, bool) {
	if v.HideCode {
		return CommitDetail{}, false
	}
	if v.Issues != nil {
		kept := make([]string, 0, len(d.Issues))
		for _, k := range d.Issues {
			if issueAllowed(v, k) {
				kept = append(kept, k)
			}
		}
		d.Issues = kept
	}
	return d, true
}
