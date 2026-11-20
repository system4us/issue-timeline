package httpapi

import (
	"encoding/json"
	"net/http"

	"gitlab.com/system4us/issue-timeline/internal/app"
)

const viewBodyLimit = 40 << 10 // a name and a state of at most 32 KB

// viewOwner is who a view is saved as: the person signed in, or the local machine.
func viewOwner(r *http.Request) string {
	c := callerOf(r)
	if c.sess != nil {
		return c.sess.User.Login
	}
	return app.LocalOwner
}

// listViews are the saved views of a repo the caller may see.
func (s *server) listViews(w http.ResponseWriter, r *http.Request) {
	rr, ok := s.route(w, r)
	if !ok {
		return
	}
	views := s.app.Views(rr.repo)
	if views == nil {
		views = []app.View{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"views": views})
}

// saveView saves a view of the page for everyone who can see the repo. It is not an edit on the host, so it needs no write
// mode: a read-only repo has views too.
func (s *server) saveView(w http.ResponseWriter, r *http.Request) {
	rr, ok := s.route(w, r)
	if !ok {
		return
	}
	var in struct {
		Name  string          `json:"name"`
		State json.RawMessage `json:"state"`
	}
	if !readBody(w, r, viewBodyLimit, &in) {
		return
	}
	view, err := s.app.SaveView(rr.repo, viewOwner(r), in.Name, in.State)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, view)
}

// deleteView removes a view: its owner, or the machine the server runs on.
func (s *server) deleteView(w http.ResponseWriter, r *http.Request) {
	rr, ok := s.route(w, r)
	if !ok {
		return
	}
	if err := s.app.DeleteView(rr.repo, r.PathValue("id"), viewOwner(r), callerOf(r).local); err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	s.listViews(w, r) // what is left
}
