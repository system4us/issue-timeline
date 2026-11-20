// Package derive builds what the frontend needs from one repo's store: the light records and the links between issues
// and pull requests (native references, keys of a tracker), tree and dependency edges, search, and the local changes an
// edit makes before the next sync. It is the Go port of derive.js, vocabulary.js, links.js, keylinks.js, search.js and
// local-apply.js.
//
// Records are never changed here: ApplyLinks and ApplyComments put a modified copy in the map (see docs/server.md).
package derive

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
)

// defaultVocabularyJSON is the words issue bodies use. The web app imports the same file (web/src/fields.ts), so the
// server and the page read the same vocabulary.
//
//go:embed vocabulary.json
var defaultVocabularyJSON []byte

// Vocabulary is the words issue bodies use: the dependency phrases, the field keys of the edit form and the CI
// workflows that are not a pull request's CI. The JSON names are the ones the payload and the web app use.
type Vocabulary struct {
	BlockedBy []string `json:"blockedBy"`
	Blocks    []string `json:"blocks"`
	FieldKeys []string `json:"fieldKeys"`
	// CIOtherWorkflows is ci.otherWorkflows of the Node vocabulary. It is not in the payload (the web app does not read
	// it); the CI package reads it.
	CIOtherWorkflows []string `json:"-"`
}

var defaultVocabulary = mustParseVocabulary(defaultVocabularyJSON, Vocabulary{})

// DefaultVocabulary is the built-in vocabulary (several languages). Each call returns its own copy.
func DefaultVocabulary() Vocabulary {
	return cloneVocabulary(defaultVocabulary)
}

// LoadVocabulary returns the defaults with the file applied over them: each of blockedBy, blocks and fieldKeys the file
// names as a list replaces the default list, and ci.otherWorkflows does too. An empty file name gives the defaults.
// An unreadable or invalid file is an error.
func LoadVocabulary(file string) (Vocabulary, error) {
	if file == "" {
		return DefaultVocabulary(), nil
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return Vocabulary{}, fmt.Errorf("reading vocabulary %s: %w", file, err)
	}
	v, err := parseVocabulary(b, DefaultVocabulary())
	if err != nil {
		return Vocabulary{}, fmt.Errorf("vocabulary %s: %w", file, err)
	}
	return v, nil
}

func mustParseVocabulary(b []byte, base Vocabulary) Vocabulary {
	v, err := parseVocabulary(b, base)
	if err != nil {
		panic("derive: built-in vocabulary is invalid: " + err.Error())
	}
	return v
}

// parseVocabulary applies the lists of a JSON object over base. A key whose value is not an array is ignored, as the
// Node server ignores it.
func parseVocabulary(b []byte, base Vocabulary) (Vocabulary, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return Vocabulary{}, err
	}
	out := cloneVocabulary(base)
	lists := map[string]*[]string{
		"blockedBy": &out.BlockedBy,
		"blocks":    &out.Blocks,
		"fieldKeys": &out.FieldKeys,
	}
	for name, dst := range lists {
		if list, ok := raw[name]; ok && isJSONArray(list) {
			var v []string
			if err := json.Unmarshal(list, &v); err != nil {
				return Vocabulary{}, fmt.Errorf("%s must be a list of phrases: %w", name, err)
			}
			*dst = v
		}
	}
	if ci, ok := raw["ci"]; ok {
		// A ci that is not an object has no otherWorkflows: it is ignored.
		var c struct {
			OtherWorkflows json.RawMessage `json:"otherWorkflows"`
		}
		if json.Unmarshal(ci, &c) == nil && isJSONArray(c.OtherWorkflows) {
			var v []string
			if err := json.Unmarshal(c.OtherWorkflows, &v); err != nil {
				return Vocabulary{}, fmt.Errorf("ci.otherWorkflows must be a list of names: %w", err)
			}
			out.CIOtherWorkflows = v
		}
	}
	return out, nil
}

func isJSONArray(b []byte) bool {
	t := bytes.TrimSpace(b)
	return len(t) > 0 && t[0] == '['
}

// cloneVocabulary copies the lists, and makes the empty ones non-nil so the payload writes [] and not null.
func cloneVocabulary(v Vocabulary) Vocabulary {
	return Vocabulary{
		BlockedBy:        cloneList(v.BlockedBy),
		Blocks:           cloneList(v.Blocks),
		FieldKeys:        cloneList(v.FieldKeys),
		CIOtherWorkflows: cloneList(v.CIOtherWorkflows),
	}
}

func cloneList(l []string) []string {
	out := make([]string, len(l))
	copy(out, l)
	return out
}
