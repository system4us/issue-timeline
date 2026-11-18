package auth

import (
	"encoding/json"
	"os"

	"gitlab.com/system4us/issue-timeline/internal/provider"
)

// The roles a person has in a repo, as the server sees them:
//
//	admin   a Redmine administrator, a GitHub repo admin, a GitLab Owner
//	editor  a Redmine member whose role is one of the editor roles (by default Manager and Developer), GitHub
//	        maintain/push/triage, GitLab Developer and up
//	viewer  any other member
//
// A person who is not a member has no role there (provider.RoleNone) and does not see the repo.

// RoleMapping names the Redmine roles that can edit. A normal Redmine user cannot read the role permissions, so the
// mapping is by role name.
type RoleMapping struct {
	Editor []string
}

// DefaultRoleMapping is the editor roles when roles.json does not say otherwise.
func DefaultRoleMapping() RoleMapping {
	return RoleMapping{Editor: []string{"Manager", "Developer"}}
}

// LoadRoleMapping reads roles.json (the editor role names, next to the store). A missing or unreadable file, or an
// "editor" that is not a list, gives the defaults.
func LoadRoleMapping(file string) RoleMapping {
	b, err := os.ReadFile(file)
	if err != nil {
		return DefaultRoleMapping()
	}
	var data struct {
		Editor *[]string `json:"editor"`
	}
	if err := json.Unmarshal(b, &data); err != nil || data.Editor == nil {
		return DefaultRoleMapping()
	}
	return RoleMapping{Editor: *data.Editor}
}

// RedmineRole is the role of a Redmine person in a project. admin wins; a member with no roles has none; one editor
// role is enough to edit; any other member only reads.
func RedmineRole(admin bool, roleNames []string, m RoleMapping) provider.Role {
	if admin {
		return provider.RoleAdmin
	}
	if len(roleNames) == 0 {
		return provider.RoleNone
	}
	for _, r := range roleNames {
		for _, e := range m.Editor {
			if r == e {
				return provider.RoleEditor
			}
		}
	}
	return provider.RoleViewer
}

// GitHubPermissions are the permissions GitHub's repo endpoint gives the signed-in person.
type GitHubPermissions struct {
	Admin, Maintain, Push, Triage, Pull bool
}

// GitHubRole is the role from GitHub's own permissions for a repo. admin, maintain, push and triage can change an
// issue (labels, assignees, milestone, state), so they are editors; pull alone reads. No permissions at all is no
// access. The mapping is by permission, not by the role's name.
func GitHubRole(p *GitHubPermissions) provider.Role {
	if p == nil {
		return provider.RoleNone
	}
	switch {
	case p.Admin:
		return provider.RoleAdmin
	case p.Maintain || p.Push || p.Triage:
		return provider.RoleEditor
	case p.Pull:
		return provider.RoleViewer
	}
	return provider.RoleNone
}
