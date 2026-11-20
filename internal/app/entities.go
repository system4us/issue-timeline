package app

import (
	"context"
	"slices"

	"gitlab.com/system4us/issue-timeline/internal/provider"
)

// EntityKinds are the kinds of thing a repo's host lets a person create (nil when it lets none).
func (s *state) entityKinds(repo string) []string {
	es, ok := s.get(repo)
	if !ok {
		return nil
	}
	if ec, ok := es.Host.(provider.EntityCreator); ok {
		return ec.EntityKinds()
	}
	return nil
}

// createEntity creates a label, milestone, version or category on the host and forgets the cached edit options of the
// repo, so the next form already lists it. The caller has checked the mode and who may write.
func (s *state) createEntity(ctx context.Context, repo string, cred provider.Cred, e provider.NewEntity) (map[string]any, error) {
	es, ok := s.get(repo)
	if !ok {
		return nil, provider.Errorf(400, "repo not configured")
	}
	ec, ok := es.Host.(provider.EntityCreator)
	if !ok {
		return nil, provider.Errorf(501, "creating labels and milestones is not built for this repo")
	}
	if !slices.Contains(ec.EntityKinds(), e.Kind) {
		return nil, provider.Errorf(400, "this repo cannot create a %q (it can create: %v)", e.Kind, ec.EntityKinds())
	}
	out, err := ec.CreateEntity(ctx, cred, e)
	if err != nil {
		return nil, err
	}
	s.optsMu.Lock()
	delete(s.opts, repo)
	s.optsMu.Unlock()
	return out, nil
}
