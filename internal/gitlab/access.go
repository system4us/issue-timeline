package gitlab

import (
	"context"
	"fmt"
	"net/url"
	"strconv"

	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/provider"
)

// GitLab's access levels: 10 Guest, 20 Reporter, 30 Developer, 40 Maintainer, 50 Owner. Below Reporter, a person does
// not see the code (pulls, commits, diffs, CI) or the internal notes.
const (
	reporterLevel  = 20
	developerLevel = 30
	ownerLevel     = 50
	viewerLevel    = 10
)

type glMember struct {
	AccessLevel int `json:"access_level"`
}

// WhoAmI is the account a token belongs to. A refused token is a *provider.Error with status 401.
func (r *Repo) WhoAmI(ctx context.Context, token string) (provider.User, error) {
	c := r.newClient(token, false, nil)
	u, err := getOne[glUser](ctx, c, "user", nil)
	if err != nil {
		return provider.User{}, err
	}
	if u == nil {
		return provider.User{}, fmt.Errorf("GitLab did not answer for /user")
	}
	return provider.User{ID: u.ID, Login: u.Username, Name: firstNonEmpty(u.Name, u.Username), Admin: u.IsAdmin}, nil
}

// accessLevel is the person's access level in the project, read with their token: 0 when they are not a member.
func (r *Repo) accessLevel(ctx context.Context, c *client, user provider.User) (int, error) {
	id, err := provider.PathID(strconv.Itoa(user.ID), "the user id")
	if err != nil {
		return 0, err
	}
	m, err := getOne[glMember](ctx, c, r.projectPath("members/all/"+id), nil)
	if err != nil || m == nil {
		return 0, err
	}
	return m.AccessLevel, nil
}

// roleOfLevel maps an access level to a role: 50 admin, 30 and up editor, 10 and up viewer, else none.
func roleOfLevel(level int) provider.Role {
	switch {
	case level >= ownerLevel:
		return provider.RoleAdmin
	case level >= developerLevel:
		return provider.RoleEditor
	case level >= viewerLevel:
		return provider.RoleViewer
	}
	return provider.RoleNone
}

// RoleOf is the person's role in the project, read with their own token. Not a member is no role.
func (r *Repo) RoleOf(ctx context.Context, cred provider.Cred, user provider.User) (provider.Role, error) {
	tok, err := r.credToken(ctx, cred)
	if err != nil {
		return provider.RoleNone, err
	}
	c := r.newClient(tok, cred.Token == "", nil)
	level, err := r.accessLevel(ctx, c, user)
	if err != nil {
		return provider.RoleNone, err
	}
	return roleOfLevel(level), nil
}

// VisibilityOf is what the person may see of the records, read with their own token. Confidential issues are visible
// only when the person's token lists them. Internal notes are hidden below Reporter, and so is the code (HideCode).
// The access level is read here, once, because a role does not tell a Guest from a Reporter.
func (r *Repo) VisibilityOf(ctx context.Context, cred provider.Cred, user provider.User, role provider.Role, issues []*model.Record) (provider.Visibility, error) {
	tok, err := r.credToken(ctx, cred)
	if err != nil {
		return provider.Visibility{}, err
	}
	c := r.newClient(tok, cred.Token == "", nil)
	level, err := r.accessLevel(ctx, c, user)
	if err != nil {
		return provider.Visibility{}, err
	}
	vis := provider.Visibility{HideCode: level < reporterLevel}

	anyConfidential := false
	for _, rec := range issues {
		if rec.Confidential != nil && *rec.Confidential {
			anyConfidential = true
			break
		}
	}
	if anyConfidential {
		// the confidential issues this token lists (every page), by iid
		listed, err := listPages[glRef](ctx, c, r.projectPath("issues"), url.Values{"confidential": {"true"}})
		if err != nil {
			return provider.Visibility{}, err
		}
		seen := map[string]bool{}
		for _, l := range listed {
			seen[strconv.Itoa(l.IID)] = true
		}
		vis.Issues = map[string]bool{}
		for _, rec := range issues {
			if rec.Confidential != nil && *rec.Confidential && !seen[rec.Key] {
				continue
			}
			vis.Issues[rec.Key] = true
		}
	}

	if level < reporterLevel {
		for _, rec := range issues {
			for _, cm := range rec.Comments {
				if !cm.Private {
					continue
				}
				if vis.HiddenComments == nil {
					vis.HiddenComments = map[string]map[string]bool{}
				}
				if vis.HiddenComments[rec.Key] == nil {
					vis.HiddenComments[rec.Key] = map[string]bool{}
				}
				vis.HiddenComments[rec.Key][cm.ID] = true
			}
		}
	}
	return vis, nil
}
