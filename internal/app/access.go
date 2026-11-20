package app

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"gitlab.com/system4us/issue-timeline/internal/auth"
	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/provider"
)

// Who may see and change what. A signed-in person's role and visibility come from their host, read with their own
// credential and kept in their session for five minutes. This machine's own identity (Local, no session) is admin.

// refused is true for the host answers that mean "this person has no access here": not signed in, forbidden, or not
// found (a repo they cannot see reads as one with no role).
func refused(err error) bool {
	var pe *provider.Error
	if errors.As(err, &pe) {
		return pe.Status == 401 || pe.Status == 403 || pe.Status == 404
	}
	return errors.Is(err, provider.ErrNotFound)
}

// sessionAccounts returns the host's Accounts interface and the matching account in sess (same provider and origin).
// Jira does not sign people in, so no session sees a Jira project.
func sessionAccounts(host provider.Host, sess *auth.Session) (provider.Accounts, auth.Account, bool) {
	acct, ok := sess.AccountFor(host.Provider(), host.Origin())
	if !ok {
		return nil, auth.Account{}, false
	}
	acc, ok := host.(provider.Accounts)
	return acc, acct, ok
}

// roleIn is the role of who in a repo: admin for this machine's own identity, read from the host for a session (and
// kept in the session), none for anyone else.
func (s *state) roleIn(ctx context.Context, repo string, who Who) (provider.Role, error) {
	if who.Session == nil {
		if who.Local {
			return provider.RoleAdmin, nil
		}
		return provider.RoleNone, nil
	}
	sess := who.Session
	if role, ok := sess.CachedRole(repo); ok {
		return role, nil
	}
	es, ok := s.get(repo)
	if !ok {
		return provider.RoleNone, nil
	}
	role := provider.RoleNone
	cache := true
	if acc, acct, ok := sessionAccounts(es.Host, sess); ok {
		r, err := acc.RoleOf(ctx, provider.Cred{Token: acct.Token}, acct.User)
		switch {
		case err == nil:
			role = r
		case refused(err):
			role = provider.RoleNone
			var pe *provider.Error
			if errors.As(err, &pe) && pe.Status == 401 {
				cache = false
			}
		default:
			return provider.RoleNone, err
		}
	}
	if cache {
		sess.KeepRole(repo, role)
	}
	return role, nil
}

// canRead is true when who has a role in a repo (and the host answered).
func (s *state) canRead(ctx context.Context, repo string, who Who) bool {
	role, err := s.roleIn(ctx, repo, who)
	return err == nil && role != provider.RoleNone
}

func (s *state) filterReadable(ctx context.Context, list []string, who Who) []string {
	if len(list) <= 1 || who.Session == nil {
		out := []string{}
		for _, r := range list {
			if s.canRead(ctx, r, who) {
				out = append(out, r)
			}
		}
		return out
	}
	keep := make([]bool, len(list))
	var wg sync.WaitGroup
	for i, r := range list {
		wg.Add(1)
		go func(idx int, repo string) {
			defer wg.Done()
			keep[idx] = s.canRead(ctx, repo, who)
		}(i, r)
	}
	wg.Wait()
	out := make([]string, 0, len(list))
	for i, r := range list {
		if keep[i] {
			out = append(out, r)
		}
	}
	return out
}

// visibleRepos are the configured repos who may see.
func (s *state) visibleRepos(ctx context.Context, who Who) []string {
	return s.filterReadable(ctx, s.repos(), who)
}

// visibleLinked are the code repos linked to a tracker that who may see.
func (s *state) visibleLinked(ctx context.Context, repo string, who Who) []string {
	return s.filterReadable(ctx, s.linkedOf(repo), who)
}

// visibility is what who may see inside a repo: nothing hidden for this machine; for a session, what the host says,
// computed from the issues read under the store's read lock (the lock is released before the host is asked).
// The session keeps it under the repo and the store's last sync: a sync that adds records makes the next read ask again,
// so a new issue the person may see is not hidden for the rest of the cache's life.
func (s *state) visibility(ctx context.Context, repo string, who Who) (provider.Visibility, error) {
	if who.Session == nil {
		return provider.Visibility{}, nil
	}
	sess := who.Session
	es, ok := s.get(repo)
	if !ok {
		return provider.Visibility{}, fmt.Errorf("repo not configured: %s", repo)
	}
	es.Store.RLock()
	cacheKey := repo
	if last := es.Store.Data.Meta.LastSync; last != nil {
		cacheKey += "@" + *last
	}
	es.Store.RUnlock()
	if v, ok := sess.CachedVisibility(cacheKey); ok {
		return v, nil
	}
	role, err := s.roleIn(ctx, repo, who)
	if err != nil {
		return provider.Visibility{}, err
	}
	acc, acct, ok := sessionAccounts(es.Host, sess)
	if !ok {
		// a host that does not sign people in (Jira) or is not signed into: the person sees none of its issues
		v := provider.Visibility{Issues: map[string]bool{}, HideCode: true}
		sess.KeepVisibility(cacheKey, v)
		return v, nil
	}
	es.Store.RLock()
	issues := make([]*model.Record, 0, len(es.Store.Data.Issues))
	for _, r := range es.Store.Data.Issues {
		issues = append(issues, r)
	}
	es.Store.RUnlock()
	v, err := acc.VisibilityOf(ctx, provider.Cred{Token: acct.Token}, acct.User, role, issues)
	if err != nil {
		return provider.Visibility{}, err
	}
	sess.KeepVisibility(cacheKey, v)
	return v, nil
}

// refusalPlaces says where a person's role is, in the words of the edit refusals.
var refusalPlaces = map[string]string{
	"github":  "on GitHub in this repo",
	"gitlab":  "on GitLab in this project",
	"redmine": "in Redmine in this project",
}

// writer is the credential an edit of repo is made with, or why the person may not make it. Jira cannot be edited.
// A session edits with its own token, and needs an editor's or an admin's role on its host. This machine's identity
// edits with the server's own credential.
func (s *state) writer(ctx context.Context, repo string, who Who) (provider.Cred, string, error) {
	p := providerOf(repo)
	if p == "jira" {
		return provider.Cred{}, "editing Jira issues is not built yet", nil
	}
	es, ok := s.get(repo)
	if !ok {
		return provider.Cred{}, "", fmt.Errorf("repo not configured: %s", repo)
	}
	if who.Session == nil {
		if !who.Local {
			return provider.Cred{}, "sign in first", nil
		}
		tok, err := s.ownToken(ctx, repo)
		if err != nil {
			return provider.Cred{}, "", err
		}
		return provider.Cred{Token: tok}, "", nil
	}
	sess := who.Session
	acct, ok := sess.AccountFor(es.Host.Provider(), es.Host.Origin())
	if !ok {
		return provider.Cred{}, "you are signed in to another host", nil
	}
	role, err := s.roleIn(ctx, repo, who)
	if err != nil {
		return provider.Cred{}, "", err
	}
	where := refusalPlaces[p]
	switch role {
	case provider.RoleNone:
		return provider.Cred{}, fmt.Sprintf("you are not a member %s", where), nil
	case provider.RoleViewer:
		return provider.Cred{}, fmt.Sprintf("your role %s is read-only", where), nil
	}
	return provider.Cred{Token: acct.Token}, "", nil
}
