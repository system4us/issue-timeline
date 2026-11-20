package derive

import (
	"encoding/json"
	"maps"
	"regexp"
	"sort"

	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/store"
)

// Link is an issue <-> pull request link. Via is "closes" (the pull closes the issue), "event" (a cross-reference),
// "key" (the tracker's key in the pull's text) or "mention" (a "#N" in the pull's body). Issue and Pull are keys.
type Link struct {
	Issue string `json:"issue"`
	Pull  string `json:"pull"`
	Via   string `json:"via"`
}

// TreeEdge is a parent -> child edge between issues: a native sub-issue, or a checklist item of the parent's body.
// Checked is the box of a checklist item; nil for a native edge.
type TreeEdge struct {
	Parent  string `json:"parent"`
	Child   string `json:"child"`
	Via     string `json:"via"`
	Checked *bool  `json:"checked"`
}

// DepEdge is a dependency, blocker -> blocked: native ("blocked by" / "blocking" of the tracker) or written in a body.
type DepEdge struct {
	Blocker string `json:"blocker"`
	Blocked string `json:"blocked"`
	Via     string `json:"via"`
}

// LinkOptions are the sources of the issue <-> pull links. Native reads the references of the pulls ("closes #N",
// cross-references, "#N" in a body); nil means on, so the zero value reads them. A tracker linked to code repos turns
// it off (model.Ptr(false)): those numbers are the code repo's own issues, not the tracker's. KeyLinks are the links
// by key (see DeriveKeyLinks); they always count.
type LinkOptions struct {
	Native   *bool
	KeyLinks []Link
}

func (o LinkOptions) native() bool { return o.Native == nil || *o.Native }

// pairKey is an (issue, pull) or (parent, child) or (blocker, blocked) pair of keys.
type pairKey struct{ a, b string }

// linkRank is how strong a link's source is: when a pair has more than one, the stronger one is kept.
var linkRank = map[string]int{"closes": 0, "event": 1, "key": 1, "mention": 2}

var mentionRe = regexp.MustCompile(`#([0-9]+)`)

// deriveLinks reads the links. Key links are added first, so a key beats an event with the same rank.
func deriveLinks(issues, pulls map[string]*model.Record, opts LinkOptions) []Link {
	found := map[pairKey]string{}
	add := func(issue, pull, via string) {
		if _, ok := issues[issue]; !ok {
			return
		}
		if _, ok := pulls[pull]; !ok {
			return
		}
		k := pairKey{issue, pull}
		if cur, ok := found[k]; !ok || linkRank[via] < linkRank[cur] {
			found[k] = via
		}
	}
	for _, l := range opts.KeyLinks {
		add(l.Issue, l.Pull, "key")
	}
	if opts.native() {
		for _, p := range pulls {
			for _, n := range p.Closes {
				add(string(n), p.Key, "closes")
			}
			for _, e := range p.Events {
				if e.Type == "CrossReferenced" && eventString(e, "sourceKind") == "Issue" {
					add(eventRef(e, "source"), p.Key, "event")
				}
			}
			for _, m := range mentionRe.FindAllStringSubmatch(p.Body, -1) {
				add(m[1], p.Key, "mention")
			}
		}
		for _, i := range issues {
			for _, e := range i.Events {
				if e.Type == "CrossReferenced" && eventString(e, "sourceKind") == "PullRequest" {
					add(i.Key, eventRef(e, "source"), "event")
				}
			}
		}
	}
	out := make([]Link, 0, len(found))
	for k, via := range found {
		out = append(out, Link{Issue: k.a, Pull: k.b, Via: via})
	}
	sort.Slice(out, func(i, j int) bool {
		if c := ByKey(out[i].Pull, out[j].Pull); c != 0 {
			return c < 0
		}
		return ByKey(out[i].Issue, out[j].Issue) < 0
	})
	return out
}

// eventRef is an event's field that names a record (a number or a key), as text; "" when the event has none.
func eventRef(e model.Event, name string) string {
	var r model.Ref
	if e.Field(name, &r) {
		return string(r)
	}
	return ""
}

func eventString(e model.Event, name string) string {
	var s string
	if e.Field(name, &s) {
		return s
	}
	return ""
}

// truthyRef is JavaScript's truth for a stored reference: a missing one, an empty one and the number 0 are false.
func truthyRef(r *model.Ref) bool {
	return r != nil && *r != "" && *r != "0"
}

var checklistRe = regexp.MustCompile(`(?m)^[ \t]*(?:[-*]|[0-9]+\.)[ \t]+\[([ xX])\][^\n]*?#([0-9]+)`)

// deriveTree reads the parent -> child edges: the native sub-issues, and the checklists an epic keeps in its body
// ("- [ ] #12 title" / "- [x] #13"). A native edge replaces a checklist one for the same pair.
func deriveTree(issues map[string]*model.Record) []TreeEdge {
	edges := map[pairKey]TreeEdge{}
	add := func(parent, child, via string, checked *bool) {
		if parent == child {
			return
		}
		if _, ok := issues[parent]; !ok {
			return
		}
		if _, ok := issues[child]; !ok {
			return
		}
		k := pairKey{parent, child}
		if _, ok := edges[k]; !ok || via == "native" {
			edges[k] = TreeEdge{Parent: parent, Child: child, Via: via, Checked: checked}
		}
	}
	for _, i := range issues {
		for _, c := range i.SubIssues {
			add(i.Key, string(c), "native", nil)
		}
		if truthyRef(i.Parent) {
			add(string(*i.Parent), i.Key, "native", nil)
		}
		for _, m := range checklistRe.FindAllStringSubmatch(i.Body, -1) {
			checked := m[1] != " "
			add(i.Key, m[2], "checklist", &checked)
		}
	}
	out := make([]TreeEdge, 0, len(edges))
	for _, e := range edges {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if c := ByKey(out[i].Parent, out[j].Parent); c != 0 {
			return c < 0
		}
		return ByKey(out[i].Child, out[j].Child) < 0
	})
	return out
}

var numberRe = regexp.MustCompile(`#([0-9]+)`)

// deriveDeps reads the dependencies, blocker -> blocked: the native ones, and the phrases of the vocabulary in a body.
// A native edge replaces a text one for the same pair.
func deriveDeps(issues map[string]*model.Record, vocab Vocabulary) []DepEdge {
	blockedBy := newPhraseSet(vocab.BlockedBy)
	blocks := newPhraseSet(vocab.Blocks)
	edges := map[pairKey]string{}
	add := func(blocker, blocked, via string) {
		if blocker == blocked {
			return
		}
		if _, ok := issues[blocker]; !ok {
			return
		}
		if _, ok := issues[blocked]; !ok {
			return
		}
		k := pairKey{blocker, blocked}
		if _, ok := edges[k]; !ok || via == "native" {
			edges[k] = via
		}
	}
	for _, i := range issues {
		for _, n := range i.BlockedBy {
			add(string(n), i.Key, "native")
		}
		for _, n := range i.Blocking {
			add(i.Key, string(n), "native")
		}
		for _, tail := range blockedBy.tails(i.Body) {
			for _, r := range numberRe.FindAllStringSubmatch(tail, -1) {
				add(r[1], i.Key, "text")
			}
		}
		for _, tail := range blocks.tails(i.Body) {
			for _, r := range numberRe.FindAllStringSubmatch(tail, -1) {
				add(i.Key, r[1], "text")
			}
		}
	}
	out := make([]DepEdge, 0, len(edges))
	for k, via := range edges {
		out = append(out, DepEdge{Blocker: k.a, Blocked: k.b, Via: via})
	}
	sort.Slice(out, func(i, j int) bool {
		if c := ByKey(out[i].Blocker, out[j].Blocker); c != 0 {
			return c < 0
		}
		return ByKey(out[i].Blocked, out[j].Blocked) < 0
	})
	return out
}

// Payload is what the frontend reads (web/src/types.ts): the meta of the repo, the light records, and the edges. The
// route adds the rest (mode, linked, ci, status).
type Payload struct {
	Meta       store.Meta `json:"meta"`
	Issues     []Light    `json:"issues"`
	Pulls      []Light    `json:"pulls"`
	Links      []Link     `json:"links"`
	Tree       []TreeEdge `json:"tree"`
	Deps       []DepEdge  `json:"deps"`
	Vocabulary Vocabulary `json:"vocabulary"`
}

// Light is a record without its bodies, comments, events, commits, reviews and edges (the list of the payload). It
// adds labelEvents (the label changes), and reviewsCount and firstReviewAt when the record has reviews. It points at
// the stored record, which is never changed.
type Light struct {
	Record *model.Record
	// LabelEvents counts the Labeled and Unlabeled events.
	LabelEvents int
	// ReviewsCount is nil for a record that has no reviews list (the fields are then left out).
	ReviewsCount *int
	// FirstReviewAt is the earliest review time, nil when no review has one.
	FirstReviewAt *string
}

var lightDropped = []string{"body", "comments", "events", "commits", "reviews", "subIssues", "blockedBy", "blocking"}

// MarshalJSON writes the record's fields that the list keeps, and the light fields.
func (l Light) MarshalJSON() ([]byte, error) {
	if l.Record == nil {
		return []byte("null"), nil
	}
	b, err := json.Marshal(l.Record)
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	for _, k := range lightDropped {
		delete(m, k)
	}
	delete(m, "firstReviewAt")
	delete(m, "reviewsCount")
	if l.ReviewsCount != nil {
		m["reviewsCount"] = mustJSON(*l.ReviewsCount)
		m["firstReviewAt"] = mustJSON(l.FirstReviewAt)
	}
	m["labelEvents"] = mustJSON(l.LabelEvents)
	return json.Marshal(m)
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func light(r *model.Record) Light {
	l := Light{Record: r}
	for _, e := range r.Events {
		if e.Type == "Labeled" || e.Type == "Unlabeled" {
			l.LabelEvents++
		}
	}
	if r.Reviews != nil {
		n := len(r.Reviews)
		l.ReviewsCount = &n
		for _, rv := range r.Reviews {
			if rv.At == nil || *rv.At == "" {
				continue
			}
			if l.FirstReviewAt == nil || *rv.At < *l.FirstReviewAt {
				at := *rv.At
				l.FirstReviewAt = &at
			}
		}
	}
	return l
}

// BuildPayload builds the payload of one repo's store. The caller holds the store's read lock while it runs. The
// payload keeps no reference into d: the meta is copied, and the records are the stored (immutable) pointers.
func BuildPayload(d *store.Data, vocab Vocabulary, opts LinkOptions) Payload {
	vocab = cloneVocabulary(vocab)
	issues := make([]Light, 0, len(d.Issues))
	for _, r := range d.Issues {
		issues = append(issues, light(r))
	}
	pulls := make([]Light, 0, len(d.Pulls))
	for _, r := range d.Pulls {
		pulls = append(pulls, light(r))
	}
	sort.Slice(issues, func(i, j int) bool { return ByKey(issues[i].Record.Key, issues[j].Record.Key) < 0 })
	sort.Slice(pulls, func(i, j int) bool { return ByKey(pulls[i].Record.Key, pulls[j].Record.Key) < 0 })
	return Payload{
		Meta:       copyMeta(d.Meta),
		Issues:     issues,
		Pulls:      pulls,
		Links:      deriveLinks(d.Issues, d.Pulls, opts),
		Tree:       deriveTree(d.Issues),
		Deps:       deriveDeps(d.Issues, vocab),
		Vocabulary: vocab,
	}
}

// copyMeta copies the meta, so the payload does not share the store's slices, pointers or maps.
func copyMeta(m store.Meta) store.Meta {
	m.Repo = copyStr(m.Repo)
	m.LastSync = copyStr(m.LastSync)
	m.FullSyncAt = copyStr(m.FullSyncAt)
	syncs := make([]store.SyncEntry, len(m.Syncs))
	copy(syncs, m.Syncs)
	m.Syncs = syncs
	if m.Totals != nil {
		t := *m.Totals
		m.Totals = &t
	}
	m.Extra = maps.Clone(m.Extra)
	return m
}

func copyStr(s *string) *string {
	if s == nil {
		return nil
	}
	v := *s
	return &v
}
