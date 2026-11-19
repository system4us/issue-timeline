package redmine

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/provider"
)

// WhoAmI is the account a key belongs to: its name is "first last", or the login when there is no name. A key Redmine
// refuses is a 401.
func (r *Repo) WhoAmI(ctx context.Context, token string) (provider.User, error) {
	if token == "" {
		return provider.User{}, provider.Errorf(401, "no API key")
	}
	c := r.newClient(token)
	var d struct {
		User struct {
			ID        int    `json:"id"`
			Login     string `json:"login"`
			Firstname string `json:"firstname"`
			Lastname  string `json:"lastname"`
			Admin     bool   `json:"admin"`
		} `json:"user"`
	}
	if err := c.do(ctx, http.MethodGet, "/users/current.json", nil, "", nil, &d); err != nil {
		if isRefused(err) {
			return provider.User{}, provider.Errorf(401, "Redmine refused the API key")
		}
		return provider.User{}, err
	}
	name := strings.TrimSpace(d.User.Firstname + " " + d.User.Lastname)
	if name == "" {
		name = d.User.Login
	}
	return provider.User{ID: d.User.ID, Login: d.User.Login, Name: name, Admin: d.User.Admin}, nil
}

// RoleOf is the person's role in the project, read with their own key: an admin of Redmine is admin; a member with one
// of the editor roles is editor; any other member is viewer; a person who is not a member, or whom Redmine refuses or
// does not find, has no role. A member's roles can come through a group they belong to.
func (r *Repo) RoleOf(ctx context.Context, cred provider.Cred, user provider.User) (provider.Role, error) {
	if user.Admin {
		return provider.RoleAdmin, nil
	}
	c, err := r.personClient(cred)
	if err != nil {
		return provider.RoleNone, err
	}
	names, member, err := r.roleNames(ctx, c, user.ID)
	if err != nil {
		if isRefused(err) || isNotFound(err) {
			return provider.RoleNone, nil
		}
		return provider.RoleNone, err
	}
	if !member || len(names) == 0 {
		return provider.RoleNone, nil
	}
	for _, n := range names {
		if r.editors[n] {
			return provider.RoleEditor, nil
		}
	}
	return provider.RoleViewer, nil
}

// roleNames are the names of the roles the user has in the project, directly or through one of their groups, and
// whether the user is a member at all. The memberships are read a page at a time.
func (r *Repo) roleNames(ctx context.Context, c client, userID int) ([]string, bool, error) {
	// The groups the user is in: a failure leaves none, and the memberships then decide.
	groups := map[int]bool{}
	var me struct {
		User struct {
			Groups []rmRef `json:"groups"`
		} `json:"user"`
	}
	if err := c.do(ctx, http.MethodGet, "/users/current.json", url.Values{"include": {"groups"}}, "", nil, &me); err == nil {
		for _, g := range me.User.Groups {
			groups[g.ID] = true
		}
	}
	var names []string
	seen := map[string]bool{}
	member := false
	for offset := 0; offset < maxMembershipOffset; offset += pageSize {
		var d struct {
			Memberships []struct {
				User  *rmRef  `json:"user"`
				Group *rmRef  `json:"group"`
				Roles []rmRef `json:"roles"`
			} `json:"memberships"`
			TotalCount *int `json:"total_count"`
		}
		q := url.Values{"limit": {strconv.Itoa(pageSize)}, "offset": {strconv.Itoa(offset)}}
		if err := c.do(ctx, http.MethodGet, "/projects/"+url.PathEscape(r.identifier)+"/memberships.json", q, "", nil, &d); err != nil {
			return nil, false, err
		}
		for _, m := range d.Memberships {
			if (m.User != nil && m.User.ID == userID) || (m.Group != nil && groups[m.Group.ID]) {
				member = true
				for _, role := range m.Roles {
					if !seen[role.Name] {
						seen[role.Name] = true
						names = append(names, role.Name)
					}
				}
			}
		}
		if len(d.Memberships) < pageSize || (d.TotalCount != nil && offset+pageSize >= *d.TotalCount) {
			break
		}
	}
	return names, member, nil
}

// VisibilityOf is what the person may see of the project's records, read with their own key. The issues are the ones
// Redmine lists for the key (private issues included, and roles that see only their own). A visible issue with a
// private note is read again with the key, and each private note the read does not include is hidden from the person.
// A read Redmine refuses or does not find hides all of that issue's private notes. Any other failure is an error: the
// person then sees nothing new.
func (r *Repo) VisibilityOf(ctx context.Context, cred provider.Cred, user provider.User, _ provider.Role, issues []*model.Record) (provider.Visibility, error) {
	c, err := r.personClient(cred)
	if err != nil {
		return provider.Visibility{}, err
	}
	proj, err := r.project(ctx, c)
	if err != nil {
		return provider.Visibility{}, err
	}
	ids, err := listIssues(ctx, c, proj.ID, "", nil)
	if err != nil {
		return provider.Visibility{}, err
	}
	vis := provider.Visibility{Issues: make(map[string]bool, len(ids))}
	for _, id := range ids {
		vis.Issues[strconv.Itoa(id)] = true
	}
	var check []*model.Record
	for _, rec := range issues {
		if vis.Issues[rec.Key] && hasPrivateComment(rec) {
			check = append(check, rec)
		}
	}
	hidden, err := hiddenNotes(ctx, c, check)
	if err != nil {
		return provider.Visibility{}, err
	}
	if len(hidden) > 0 {
		vis.HiddenComments = hidden
	}
	return vis, nil
}

func hasPrivateComment(rec *model.Record) bool {
	for _, cm := range rec.Comments {
		if cm.Private {
			return true
		}
	}
	return false
}

// hiddenNotes reads each record's issue with the person's key, visibilityConcurrency at a time, and returns the private
// note ids the read does not include, per issue key.
func hiddenNotes(ctx context.Context, c client, recs []*model.Record) (map[string]map[string]bool, error) {
	out := map[string]map[string]bool{}
	if len(recs) == 0 {
		return out, nil
	}
	found := make([][]string, len(recs))
	errs := make([]error, len(recs))
	sem := make(chan struct{}, visibilityConcurrency)
	var wg sync.WaitGroup
	for i, rec := range recs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			found[i], errs[i] = hiddenFor(ctx, c, rec)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	for i, rec := range recs {
		if len(found[i]) == 0 {
			continue
		}
		m := make(map[string]bool, len(found[i]))
		for _, id := range found[i] {
			m[id] = true
		}
		out[rec.Key] = m
	}
	return out, nil
}

// hiddenFor is the private note ids of one issue that the person's read does not include.
func hiddenFor(ctx context.Context, c client, rec *model.Record) ([]string, error) {
	id, err := pathIssueID(rec.Key)
	if err != nil {
		return nil, err
	}
	shown := map[string]bool{}
	raw, err := fetchIssue(ctx, c, id, "journals")
	switch {
	case err == nil:
		for _, j := range raw.Journals {
			shown[strconv.Itoa(j.ID)] = true
		}
	case isRefused(err) || isNotFound(err):
		// the person cannot read the issue's notes at all: none of its private ones is theirs
	default:
		return nil, err
	}
	var hidden []string
	for _, cm := range rec.Comments {
		if cm.Private && !shown[cm.ID] {
			hidden = append(hidden, cm.ID)
		}
	}
	return hidden, nil
}
