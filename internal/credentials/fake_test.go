package credentials

import (
	"errors"
	"sync"
)

// fakeKeyring stands in for the OS keyring: a map of full name -> value, guarded so concurrent tests can share it.
type fakeKeyring struct {
	mu    sync.Mutex
	vault map[string]string
}

func newFakeKeyring() *fakeKeyring { return &fakeKeyring{vault: map[string]string{}} }

func (f *fakeKeyring) Get(name string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.vault[name]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}

func (f *fakeKeyring) Set(name, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.vault[name] = value
	return nil
}

func (f *fakeKeyring) Delete(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.vault[name]; !ok {
		return ErrNotFound
	}
	delete(f.vault, name)
	return nil
}

// put sets a value directly in the vault (as if another program had saved it).
func (f *fakeKeyring) put(name, value string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.vault[name] = value
}

// clearAll empties the vault.
func (f *fakeKeyring) clearAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.vault = map[string]string{}
}

// noKeyring is a keyring that is not there: every call fails, as with no secret-tool installed.
type noKeyring struct{}

var errNoKeyring = errors.New("spawn secret-tool ENOENT")

func (noKeyring) Get(string) (string, error) { return "", ErrNotFound }
func (noKeyring) Set(string, string) error   { return errNoKeyring }
func (noKeyring) Delete(string) error        { return ErrNotFound }
