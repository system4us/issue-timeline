package commits

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/store"
)

const (
	maxFilePatch = 200_000   // characters: a bigger file keeps its stats, not its patch
	maxDiff      = 1_500_000 // characters of patch per commit
)

// File is one file of a commit's diff, as the API serves it and the cache keeps it.
type File struct {
	Path      string  `json:"path"`
	OldPath   *string `json:"oldPath"`
	Status    string  `json:"status"`
	Additions int     `json:"additions"`
	Deletions int     `json:"deletions"`
	Patch     *string `json:"patch"`
	Note      *string `json:"note"`
}

// CountLines counts the added and removed lines of a patch: its lines start with + or -, the patches carry no file
// headers.
func CountLines(patch string) (additions, deletions int) {
	for _, line := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(line, "+"):
			additions++
		case strings.HasPrefix(line, "-"):
			deletions++
		}
	}
	return additions, deletions
}

// jsLen is the length of s the way JS counts it (UTF-16 code units), so the caps are the same as the Node server's.
func jsLen(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// FinishFiles fills in the stats from the patches and caps the size of what is cached and sent. truncated is true when
// a patch was left out for its size (the file keeps its stats and a note).
func FinishFiles(files []model.HostFile) (out []File, truncated bool) {
	budget := maxDiff
	out = make([]File, 0, len(files))
	for _, f := range files {
		var additions, deletions int
		if f.Patch != nil {
			additions, deletions = CountLines(*f.Patch)
		} else {
			additions, deletions = intOr0(f.Additions), intOr0(f.Deletions)
		}
		var text *string
		if f.Patch != nil {
			p := *f.Patch
			n := jsLen(p)
			if n > maxFilePatch || n > budget {
				truncated = true
			} else {
				text = &p
				budget -= n
			}
		}
		var note *string
		switch {
		case f.Patch == nil && f.TooLarge:
			note = strPtr("Too large to show.")
		case f.Patch == nil:
			note = strPtr("No text diff (binary, or not included by the host).")
		case text == nil:
			note = strPtr("Diff omitted: too large.")
		}
		out = append(out, File{
			Path: f.Path, OldPath: f.OldPath, Status: f.Status,
			Additions: additions, Deletions: deletions, Patch: text, Note: note,
		})
	}
	return out, truncated
}

func intOr0(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func strPtr(s string) *string { return &s }

// DiffDirFor is the folder of a repo's cached diffs, next to its store: diffs/<first 16 hex of sha256(repo)>.
func DiffDirFor(storeDir, repo string) string {
	sum := sha256.Sum256([]byte(repo))
	return filepath.Join(storeDir, "diffs", hex.EncodeToString(sum[:])[:16])
}

// CachedDiff is what one commit's diff cache file holds: the commit's info, its files and whether any patch was cut.
type CachedDiff struct {
	Info      *model.CommitInfo `json:"info"`
	Files     []File            `json:"files"`
	Truncated bool              `json:"truncated"`
}

// checkSha refuses anything but a full commit id, because the id becomes a file name.
func checkSha(sha string) error {
	if !FullSHA.MatchString(sha) {
		return fmt.Errorf("not a full commit id: %q", sha)
	}
	return nil
}

// ReadCachedDiff reads a commit's cached diff; nil when there is none. A cache file that does not parse counts as none,
// so the diff is read from the host again (as the Node server did).
func ReadCachedDiff(dir, sha string) (*CachedDiff, error) {
	if err := checkSha(sha); err != nil {
		return nil, err
	}
	name := filepath.Join(dir, sha+".json")
	b, err := os.ReadFile(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", name, err)
	}
	var v CachedDiff
	if err := json.Unmarshal(b, &v); err != nil || v.Files == nil {
		return nil, nil
	}
	return &v, nil
}

// WriteCachedDiff writes a commit's diff cache file atomically.
func WriteCachedDiff(dir, sha string, v *CachedDiff) error {
	if err := checkSha(sha); err != nil {
		return err
	}
	if v == nil {
		return errors.New("no diff to cache")
	}
	out := *v
	if out.Files == nil {
		out.Files = []File{}
	}
	b, err := json.Marshal(out)
	if err != nil {
		return err
	}
	return store.WriteAtomic(filepath.Join(dir, sha+".json"), b, 0o644)
}
