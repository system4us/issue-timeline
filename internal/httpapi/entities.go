package httpapi

import (
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"gitlab.com/system4us/issue-timeline/internal/provider"
)

const (
	entityBodyLimit  = 16 << 10 // a name, a colour, a description
	entityNameMax    = 200
	entityDescMax    = 2000
	entityColorShape = `^[0-9a-fA-F]{6}$`
)

var entityColor = regexp.MustCompile(entityColorShape)

type newEntityBody struct {
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Color       string `json:"color"`
	Description string `json:"description"`
	DueOn       string `json:"dueOn"`
}

// check reads what a create form sent into a provider.NewEntity, or says what is wrong with it.
func (b newEntityBody) check() (provider.NewEntity, string) {
	name := strings.TrimSpace(b.Name)
	switch {
	case name == "":
		return provider.NewEntity{}, "a name is needed"
	case utf8.RuneCountInString(name) > entityNameMax:
		return provider.NewEntity{}, "the name is too long"
	case strings.IndexFunc(name, unicode.IsControl) >= 0:
		return provider.NewEntity{}, "the name has control characters"
	case utf8.RuneCountInString(b.Description) > entityDescMax:
		return provider.NewEntity{}, "the description is too long"
	}
	color := strings.TrimPrefix(strings.TrimSpace(b.Color), "#")
	if color != "" && !entityColor.MatchString(color) {
		return provider.NewEntity{}, "the colour is six hex digits, such as 0e8a16"
	}
	if b.DueOn != "" {
		if _, err := time.Parse("2006-01-02", b.DueOn); err != nil {
			return provider.NewEntity{}, "the due date is YYYY-MM-DD"
		}
	}
	return provider.NewEntity{Kind: b.Kind, Name: name, Color: strings.ToLower(color), Description: b.Description, DueOn: b.DueOn}, ""
}

// createEntity creates a label, milestone, version or category on the repo's host, so a picker has it to choose.
func (s *server) createEntity(w http.ResponseWriter, r *http.Request) {
	rr, ok := s.route(w, r)
	if !ok || !s.writeAllowed(w, rr) {
		return
	}
	if providerOf(rr.repo) == "jira" {
		writeError(w, http.StatusNotImplemented, "creating labels and milestones is not built for Jira")
		return
	}
	cred, ok := s.writerFor(w, r, rr)
	if !ok {
		return
	}
	var body newEntityBody
	if !readBody(w, r, entityBodyLimit, &body) {
		return
	}
	e, why := body.check()
	if why != "" {
		writeError(w, http.StatusBadRequest, why)
		return
	}
	out, err := s.app.CreateEntity(r.Context(), rr.repo, cred, e)
	if err != nil {
		fail(w, err, http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}
