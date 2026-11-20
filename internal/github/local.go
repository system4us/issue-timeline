package github

import (
	"encoding/json"

	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/provider"
)

// LocalLinks is what an edit changed in the records the store keeps: the relations and the parent of the issue and the
// other issues it names, and the notes edited or deleted on it. It is applied so the timeline shows the change before
// the next sync. It returns copies of the records it changed (keyed by their key); the stored ones are never changed.
// A record that is not in issues is skipped: its other side is only kept when the caller has it.
func LocalLinks(issues map[string]*model.Record, key string, ch provider.Changes) map[string]*model.Record {
	out := map[string]*model.Record{}
	me := issues[key]
	if me == nil {
		return out
	}
	// mutable is the copy of a record to change, made once per edit
	mutable := func(k string) *model.Record {
		if c, ok := out[k]; ok {
			return c
		}
		src := issues[k]
		if src == nil {
			return nil
		}
		c := *src
		out[k] = &c
		return &c
	}

	apply := func(l linkChange, on bool) {
		to := scalarText(l.To)
		switch l.Type {
		case "blocks":
			if m := mutable(key); m != nil {
				m.Blocking = refsWith(m.Blocking, model.Ref(to), on)
			}
			if other := mutable(to); other != nil {
				other.BlockedBy = refsWith(other.BlockedBy, model.Ref(key), on)
			}
		case "blockedBy":
			if m := mutable(key); m != nil {
				m.BlockedBy = refsWith(m.BlockedBy, model.Ref(to), on)
			}
			if other := mutable(to); other != nil {
				other.Blocking = refsWith(other.Blocking, model.Ref(key), on)
			}
		}
	}
	var add, remove []linkChange
	ch.Get("addLinks", &add)
	ch.Get("removeLinks", &remove)
	for _, l := range add {
		apply(l, true)
	}
	for _, l := range remove {
		apply(l, false)
	}

	if ch.Has("parent") {
		m := mutable(key)
		if m.Parent != nil {
			if old := mutable(string(*m.Parent)); old != nil {
				old.SubIssues = refsWith(old.SubIssues, model.Ref(key), false)
			}
		}
		if ch.IsNull("parent") {
			m.Parent = nil
		} else {
			p := model.Ref(scalarText(ch["parent"]))
			m.Parent = &p
			if np := mutable(string(p)); np != nil {
				np.SubIssues = refsWith(np.SubIssues, model.Ref(key), true)
			}
		}
	}

	// notes: an edited one takes its new body; a deleted one goes, and the count follows
	if me.Comments != nil {
		var edits []struct {
			ID   json.RawMessage `json:"id"`
			Body string          `json:"body"`
		}
		var deletes []json.RawMessage
		ch.Get("editComments", &edits)
		ch.Get("deleteComments", &deletes)
		if len(edits) > 0 || len(deletes) > 0 {
			m := mutable(key)
			comments := make([]model.Comment, len(m.Comments))
			copy(comments, m.Comments)
			for _, e := range edits {
				id := scalarText(e.ID)
				for i := range comments {
					if comments[i].ID == id {
						comments[i].Body = e.Body
						break
					}
				}
			}
			if len(deletes) > 0 {
				gone := map[string]bool{}
				for _, d := range deletes {
					gone[scalarText(d)] = true
				}
				kept := []model.Comment{}
				for _, c := range comments {
					if !gone[c.ID] {
						kept = append(kept, c)
					}
				}
				comments = kept
				m.CommentsCount = len(comments)
			}
			m.Comments = comments
		}
	}
	return out
}

// refsWith is a new list with ref added (on) or removed (off); the list it is given is never changed.
func refsWith(list []model.Ref, ref model.Ref, on bool) []model.Ref {
	cp := append([]model.Ref(nil), list...)
	if on {
		return model.AddRef(cp, ref)
	}
	return model.DropRef(cp, ref)
}
