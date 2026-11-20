package derive

import (
	"regexp"
	"strings"

	"gitlab.com/system4us/issue-timeline/internal/model"
)

// What an edit changed, applied to the records the store keeps, so the timeline shows it before the next sync.
//
// Records in the store are never changed in place: each record an edit touches is replaced in the map by a copy, and
// the slices that change are new ones (so the old record, and any slice of it, stays as it was). The caller holds the
// store's Lock.

// LinkChange is one relation an edit adds or removes: Type is "blockedBy" or "blocks", To the other issue's key.
type LinkChange struct {
	Type string    `json:"type"`
	To   model.Ref `json:"to"`
}

// CommentEdit is a note an edit changed: its id and its new body.
type CommentEdit struct {
	ID   string `json:"id"`
	Body string `json:"body"`
}

var digitsOnly = regexp.MustCompile(`^[0-9]+$`)

// asStored is a key the way the stores keep it: a number when it is all digits (without leading zeros), text otherwise.
// A sync writes the same, so a change made here matches the next read.
func asStored(v string) model.Ref {
	if digitsOnly.MatchString(v) {
		return model.Ref(strings.TrimLeft(v, "0") + zeroIfAllZeros(v))
	}
	return model.Ref(v)
}

func zeroIfAllZeros(v string) string {
	if strings.TrimLeft(v, "0") == "" {
		return "0"
	}
	return ""
}

// editRefs returns a new list with v added (on) or removed (off). Values are compared as text. The list it is given is
// never written to.
func editRefs(list []model.Ref, v string, on bool) []model.Ref {
	out := make([]model.Ref, 0, len(list)+1)
	present := false
	for _, x := range list {
		if string(x) == v {
			present = true
			if !on {
				continue
			}
		}
		out = append(out, x)
	}
	if on && !present {
		out = append(out, asStored(v))
	}
	return out
}

// copyOnWrite hands out a copy of each record the first time an edit touches it, and keeps the copies until the edit is
// done. A record touched twice (an issue linked to itself) is the same copy both times.
type copyOnWrite struct {
	issues  map[string]*model.Record
	changed map[string]*model.Record
}

func (w *copyOnWrite) get(key string) (*model.Record, bool) {
	if r, ok := w.changed[key]; ok {
		return r, true
	}
	old := w.issues[key]
	if old == nil {
		return nil, false
	}
	c := *old
	w.changed[key] = &c
	return &c, true
}

// commit puts the copies in the map in place of the records they copy.
func (w *copyOnWrite) commit() {
	for k, r := range w.changed {
		w.issues[k] = r
	}
}

// ApplyLinks records an edit's relations on the issue key and on the other issues it names (when the store has them),
// and its parent. add and remove are applied in that order. setParent false leaves the parent alone; setParent with a
// nil parent removes it. A key that is not in the store changes nothing.
func ApplyLinks(issues map[string]*model.Record, key string, add, remove []LinkChange, parent *model.Ref, setParent bool) {
	w := &copyOnWrite{issues: issues, changed: map[string]*model.Record{}}
	me, ok := w.get(key)
	if !ok {
		return
	}
	apply := func(l LinkChange, on bool) {
		to := string(l.To)
		switch l.Type {
		case "blocks":
			me.Blocking = editRefs(me.Blocking, to, on)
			if other, ok := w.get(to); ok {
				other.BlockedBy = editRefs(other.BlockedBy, key, on)
			}
		case "blockedBy":
			me.BlockedBy = editRefs(me.BlockedBy, to, on)
			if other, ok := w.get(to); ok {
				other.Blocking = editRefs(other.Blocking, key, on)
			}
		}
	}
	for _, l := range add {
		apply(l, true)
	}
	for _, l := range remove {
		apply(l, false)
	}
	if setParent {
		if me.Parent != nil {
			if old, ok := w.get(string(*me.Parent)); ok {
				old.SubIssues = editRefs(old.SubIssues, key, false)
			}
		}
		if parent == nil {
			me.Parent = nil
		} else {
			p := asStored(string(*parent))
			me.Parent = &p
			if np, ok := w.get(string(*parent)); ok {
				np.SubIssues = editRefs(np.SubIssues, key, true)
			}
		}
	}
	w.commit()
}

// ApplyComments records an edit's notes on the issue key: the notes it edited get their new body, and the ones it
// deleted are dropped (and the count goes with them). A record without a comments list, or a key that is not in the
// store, changes nothing.
func ApplyComments(issues map[string]*model.Record, key string, edits []CommentEdit, deletes []string) {
	old := issues[key]
	if old == nil || old.Comments == nil {
		return
	}
	c := *old
	comments := make([]model.Comment, len(old.Comments))
	copy(comments, old.Comments)
	for _, e := range edits {
		for i := range comments {
			if comments[i].ID == e.ID {
				comments[i].Body = e.Body
				break
			}
		}
	}
	if len(deletes) > 0 {
		gone := map[string]bool{}
		for _, id := range deletes {
			gone[id] = true
		}
		kept := make([]model.Comment, 0, len(comments))
		for _, cm := range comments {
			if !gone[cm.ID] {
				kept = append(kept, cm)
			}
		}
		comments = kept
		c.CommentsCount = len(comments)
	}
	c.Comments = comments
	issues[key] = &c
}
