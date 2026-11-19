package gitlab

import (
	"encoding/json"
	"strings"

	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/provider"
)

// linkTypes are the link types of the change, as GitLab names them.
var linkTypes = map[string]string{"blocks": "blocks", "blockedBy": "is_blocked_by", "relates": "relates_to"}

// linkChange is one link to add or remove: its type ("blocks", "blockedBy", "relates") and the other issue's iid.
type linkChange struct {
	Type string
	To   string
}

// noteEdit is an existing note to edit, by its id.
type noteEdit struct {
	ID   string
	Body string
}

// localChange is what an edit changed in the links, the parent and the notes: applied to the records the store keeps,
// so the timeline shows it before the next sync.
type localChange struct {
	addLinks    []linkChange
	removeLinks []linkChange
	parentSet   bool
	parent      *string // nil is no parent
	editNotes   []noteEdit
	deleteNotes []string
}

// rawID is an id as the change gives it: a number or a string, as text. "" for null or nothing.
func rawID(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return ""
	}
	if s[0] == '"' {
		var v string
		if json.Unmarshal([]byte(s), &v) == nil {
			return v
		}
	}
	return s
}

// parseLinks reads a list of { type, to } changes.
func parseLinks(ch provider.Changes, name string) ([]linkChange, error) {
	var raw []struct {
		Type string          `json:"type"`
		To   json.RawMessage `json:"to"`
	}
	if !ch.Get(name, &raw) {
		return nil, nil
	}
	out := make([]linkChange, 0, len(raw))
	for _, l := range raw {
		out = append(out, linkChange{Type: l.Type, To: rawID(l.To)})
	}
	return out, nil
}

// localOf reads the links, the parent and the notes of a change.
func localOf(ch provider.Changes) (localChange, error) {
	var lc localChange
	var err error
	if lc.addLinks, err = parseLinks(ch, "addLinks"); err != nil {
		return lc, err
	}
	if lc.removeLinks, err = parseLinks(ch, "removeLinks"); err != nil {
		return lc, err
	}
	if ch.Has("parent") {
		lc.parentSet = true
		if id := rawID(ch["parent"]); id != "" {
			lc.parent = &id
		}
	}
	var edits []struct {
		ID   json.RawMessage `json:"id"`
		Body string          `json:"body"`
	}
	if ch.Get("editComments", &edits) {
		for _, e := range edits {
			lc.editNotes = append(lc.editNotes, noteEdit{ID: rawID(e.ID), Body: e.Body})
		}
	}
	var dels []json.RawMessage
	if ch.Get("deleteComments", &dels) {
		for _, d := range dels {
			lc.deleteNotes = append(lc.deleteNotes, rawID(d))
		}
	}
	return lc, nil
}

// ApplyLocally applies an edit's links and parent to the other records the store keeps, copy-on-write: each other issue
// is replaced by a copy. The issue itself is EditIssue's (it returns it with the same changes). Call it under the
// store's Lock. old is the record before the edit.
func ApplyLocally(issues map[string]*model.Record, old *model.Record, ch provider.Changes) error {
	lc, err := localOf(ch)
	if err != nil {
		return err
	}
	key := old.Key
	keyRef := model.Ref(key)
	apply := func(l linkChange, on bool) {
		to := l.To
		if to == "" || to == key {
			return
		}
		rec, ok := issues[to]
		if !ok {
			return
		}
		c := *rec
		switch l.Type {
		case "blocks":
			c.BlockedBy = setRef(rec.BlockedBy, keyRef, on)
		case "blockedBy":
			c.Blocking = setRef(rec.Blocking, keyRef, on)
		default:
			return
		}
		issues[to] = &c
	}
	for _, l := range lc.addLinks {
		apply(l, true)
	}
	for _, l := range lc.removeLinks {
		apply(l, false)
	}
	if lc.parentSet {
		// the old parent loses the issue as a sub-issue, the new one gains it
		if old.Parent != nil {
			setSub(issues, string(*old.Parent), key, false)
		}
		if lc.parent != nil {
			setSub(issues, *lc.parent, key, true)
		}
	}
	return nil
}

// setSub adds or removes key from the sub-issues of the issue named by parent, when the store has that issue.
func setSub(issues map[string]*model.Record, parent, key string, on bool) {
	if parent == "" || parent == key {
		return
	}
	rec, ok := issues[parent]
	if !ok {
		return
	}
	c := *rec
	c.SubIssues = setRef(rec.SubIssues, model.Ref(key), on)
	issues[parent] = &c
}

// setRef returns a new list with ref in it (on) or without it. The list it is given is never changed.
func setRef(list []model.Ref, ref model.Ref, on bool) []model.Ref {
	if on && model.HasRef(list, ref) {
		return append([]model.Ref(nil), list...)
	}
	out := make([]model.Ref, 0, len(list)+1)
	for _, x := range list {
		if x != ref {
			out = append(out, x)
		}
	}
	if on {
		out = append(out, ref)
	}
	return out
}

// applySelf applies the links, the parent and the notes of a change to the record itself (a copy the caller owns).
func applySelf(rec *model.Record, lc localChange) {
	for _, l := range lc.addLinks {
		applySelfLink(rec, l, true)
	}
	for _, l := range lc.removeLinks {
		applySelfLink(rec, l, false)
	}
	if lc.parentSet {
		if lc.parent == nil {
			rec.Parent = nil
		} else {
			ref := model.Ref(*lc.parent)
			rec.Parent = &ref
		}
	}
	if rec.Comments == nil || (len(lc.editNotes) == 0 && len(lc.deleteNotes) == 0) {
		return
	}
	comments := append([]model.Comment(nil), rec.Comments...)
	for _, e := range lc.editNotes {
		for i := range comments {
			if comments[i].ID == e.ID {
				comments[i].Body = e.Body
			}
		}
	}
	if len(lc.deleteNotes) > 0 {
		kept := comments[:0]
		for _, c := range comments {
			if !contains(lc.deleteNotes, c.ID) {
				kept = append(kept, c)
			}
		}
		comments = kept
		rec.CommentsCount = len(comments)
	}
	rec.Comments = comments
}

func applySelfLink(rec *model.Record, l linkChange, on bool) {
	ref := model.Ref(l.To)
	switch l.Type {
	case "blocks":
		rec.Blocking = setRef(rec.Blocking, ref, on)
	case "blockedBy":
		rec.BlockedBy = setRef(rec.BlockedBy, ref, on)
	}
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// checkLinkType is an error for a link type this package does not know how to add.
func checkLinkType(t string) error {
	if _, ok := linkTypes[t]; !ok {
		return provider.Errorf(422, "unknown link type %s", t)
	}
	return nil
}

// validateLocal checks the ids a change puts in a URL path, before any request is made.
func validateLocal(lc localChange) error {
	for _, l := range lc.addLinks {
		if err := checkLinkType(l.Type); err != nil {
			return err
		}
		if _, err := provider.PathID(l.To, "the other issue"); err != nil {
			return err
		}
	}
	if lc.parent != nil {
		if _, err := provider.PathID(*lc.parent, "the parent"); err != nil {
			return err
		}
	}
	for _, e := range lc.editNotes {
		if _, err := provider.PathID(e.ID, "a note id"); err != nil {
			return err
		}
	}
	for _, id := range lc.deleteNotes {
		if _, err := provider.PathID(id, "a note id"); err != nil {
			return err
		}
	}
	return nil
}
