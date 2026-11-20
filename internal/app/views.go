package app

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"gitlab.com/system4us/issue-timeline/internal/provider"
	"gitlab.com/system4us/issue-timeline/internal/store"
)

// A saved view is a named set of what the page shows (filters, grouping, the dashboard layout...) kept on the server, per
// repo, so a team opens the same one. The server does not read the state: it is the page's JSON object, kept as it came.

const (
	viewNameMax  = 80
	viewStateMax = 32 << 10 // 32 KB: filters and a layout, never data
	viewsPerRepo = 100
	// LocalOwner owns the views saved from the machine the server runs on, where nobody signs in.
	LocalOwner = "local"
)

// View is a saved view.
type View struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Owner     string          `json:"owner"`
	CreatedAt string          `json:"createdAt"`
	UpdatedAt string          `json:"updatedAt"`
	State     json.RawMessage `json:"state"`
}

// viewStore keeps the views of every repo in one file (views.json), guarded by its own mutex.
type viewStore struct {
	mu   sync.Mutex
	file string
	m    map[string][]View // by repo key
	now  func() time.Time
}

func loadViews(file string) *viewStore {
	v := &viewStore{file: file, m: map[string][]View{}, now: time.Now}
	var raw map[string][]View
	_ = readJSONFile(file, &raw)
	for repo, list := range raw {
		for _, view := range list {
			if view.ID != "" && view.Name != "" && validViewState(view.State) == nil {
				v.m[repo] = append(v.m[repo], view)
			}
		}
	}
	return v
}

// save writes the file; the caller holds the mutex.
func (v *viewStore) save() error {
	b, err := json.Marshal(v.m)
	if err != nil {
		return err
	}
	return store.WriteAtomic(v.file, b, 0o600)
}

func validViewName(name string) error {
	switch {
	case name == "":
		return provider.Errorf(400, "a name is needed")
	case utf8.RuneCountInString(name) > viewNameMax:
		return provider.Errorf(400, "the name is longer than %d characters", viewNameMax)
	case strings.IndexFunc(name, unicode.IsControl) >= 0:
		return provider.Errorf(400, "the name has control characters")
	}
	return nil
}

// validViewState accepts a JSON object of at most viewStateMax bytes.
func validViewState(state json.RawMessage) error {
	if len(state) > viewStateMax {
		return provider.Errorf(413, "the view is larger than %d KB", viewStateMax>>10)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(state, &obj); err != nil || obj == nil {
		return provider.Errorf(400, "the state of a view is a JSON object")
	}
	return nil
}

func newViewID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// list is a repo's views, oldest first.
func (v *viewStore) list(repo string) []View {
	v.mu.Lock()
	defer v.mu.Unlock()
	return slices.Clone(v.m[repo])
}

// put saves a view by name: a new name is a new view; the same name (without case) by its owner replaces its state; by
// someone else it is a 409, so one person's view is never overwritten by another.
func (v *viewStore) put(repo, owner, name string, state json.RawMessage) (View, error) {
	name = strings.TrimSpace(name)
	if err := validViewName(name); err != nil {
		return View{}, err
	}
	if err := validViewState(state); err != nil {
		return View{}, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	now := v.now().UTC().Format(time.RFC3339)
	list := v.m[repo]
	for i, existing := range list {
		if !strings.EqualFold(existing.Name, name) {
			continue
		}
		if existing.Owner != owner {
			return View{}, provider.Errorf(409, "a view called %q already exists (it is %s's)", existing.Name, existing.Owner)
		}
		list[i].State, list[i].UpdatedAt = state, now
		if err := v.save(); err != nil {
			return View{}, err
		}
		return list[i], nil
	}
	if len(list) >= viewsPerRepo {
		return View{}, provider.Errorf(400, "a repo keeps at most %d views: delete one first", viewsPerRepo)
	}
	view := View{ID: newViewID(), Name: name, Owner: owner, CreatedAt: now, UpdatedAt: now, State: state}
	v.m[repo] = append(list, view)
	if err := v.save(); err != nil {
		v.m[repo] = list
		return View{}, err
	}
	return view, nil
}

// remove deletes a view: its owner may, and so may the machine the server runs on (`local`, which administers it).
func (v *viewStore) remove(repo, id, owner string, local bool) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	list := v.m[repo]
	i := slices.IndexFunc(list, func(x View) bool { return x.ID == id })
	if i < 0 {
		return provider.Errorf(404, "no such view")
	}
	if list[i].Owner != owner && !local {
		return provider.Errorf(403, "only %s can delete this view", list[i].Owner)
	}
	v.m[repo] = slices.Delete(slices.Clone(list), i, i+1)
	if len(v.m[repo]) == 0 {
		delete(v.m, repo)
	}
	return v.save()
}

// dropRepo forgets the views of a repo that was removed.
func (v *viewStore) dropRepo(repo string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if _, ok := v.m[repo]; !ok {
		return nil
	}
	delete(v.m, repo)
	return v.save()
}
