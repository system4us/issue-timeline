package commits

import (
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"gitlab.com/system4us/issue-timeline/internal/model"
)

func strp(s string) *string { return &s }
func intp(n int) *int       { return &n }

func TestCountLinesCountsPlusAndMinusLines(t *testing.T) {
	a, d := CountLines("+one\n-two\n three\n+++ four\n--- five\n")
	if a != 2 || d != 2 {
		t.Fatalf("CountLines = %d, %d; want 2, 2 (the lines starting with + or - count, even those that look like headers)", a, d)
	}
	if a, d := CountLines(""); a != 0 || d != 0 {
		t.Fatalf("an empty patch has no lines: %d, %d", a, d)
	}
}

func TestFinishFilesFillsStatsAndNotes(t *testing.T) {
	files := []model.HostFile{
		{Path: "a.go", Status: "modified", Patch: strp("+x\n-y\n")},
		{Path: "img.png", Status: "added", Additions: intp(7), Deletions: intp(0), Patch: nil},
		{Path: "big.bin", Status: "modified", TooLarge: true, Patch: nil},
		{Path: "moved.go", OldPath: strp("old.go"), Status: "renamed", Patch: strp("+z\n")},
	}
	out, truncated := FinishFiles(files)
	if truncated {
		t.Fatal("nothing was too big")
	}
	if len(out) != 4 {
		t.Fatalf("%d files", len(out))
	}
	if out[0].Additions != 1 || out[0].Deletions != 1 || out[0].Note != nil || *out[0].Patch != "+x\n-y\n" {
		t.Fatalf("a.go: %+v", out[0])
	}
	if out[1].Additions != 7 || out[1].Deletions != 0 || *out[1].Note != "No text diff (binary, or not included by the host)." || out[1].Patch != nil {
		t.Fatalf("img.png: %+v", out[1])
	}
	if *out[2].Note != "Too large to show." {
		t.Fatalf("big.bin note: %+v", out[2])
	}
	if out[3].OldPath == nil || *out[3].OldPath != "old.go" {
		t.Fatalf("oldPath lost: %+v", out[3])
	}
}

func TestFinishFilesCapsThePatchesAndKeepsTheStats(t *testing.T) {
	big := "+" + strings.Repeat("x", maxFilePatch) // one character over the cap of a file
	files := []model.HostFile{{Path: "huge.txt", Status: "added", Patch: strp(big)}}
	out, truncated := FinishFiles(files)
	if !truncated || out[0].Patch != nil || *out[0].Note != "Diff omitted: too large." || out[0].Additions != 1 {
		t.Fatalf("a file over the cap keeps its stats only: truncated=%v %+v", truncated, out[0])
	}

	// The commit's budget: a second patch that no longer fits is left out, the first one stays.
	// 8 patches of 199999 characters: seven fit in the 1500000 of a commit, the eighth does not.
	part := "+" + strings.Repeat("y", maxFilePatch-1)
	files = nil
	for i := 0; i < 8; i++ {
		files = append(files, model.HostFile{Path: strconv.Itoa(i), Status: "modified", Patch: strp(part)})
	}
	out, truncated = FinishFiles(files)
	if len(out) != 8 || out[6].Patch == nil || out[7].Patch != nil || *out[7].Note != "Diff omitted: too large." {
		t.Fatalf("the first seven patches fit, the eighth does not: %+v", out[6:])
	}
	if !truncated {
		t.Fatal("truncated must be set when a patch is left out")
	}
}

func TestFinishFilesCountsTheCapInUTF16Units(t *testing.T) {
	// 100000 characters of U+1F600 are 200000 UTF-16 units: the same as the Node server's length check.
	emoji := "+" + strings.Repeat("\U0001F600", 100_000)
	out, truncated := FinishFiles([]model.HostFile{{Path: "e", Status: "added", Patch: strp(emoji)}})
	if !truncated || out[0].Patch != nil {
		t.Fatalf("a patch of 200001 UTF-16 units must be over the 200000 cap: truncated=%v", truncated)
	}
}

func TestDiffDirForIsTheSameNameAsNode(t *testing.T) {
	// sha256("owner/repo") begins 4e5b... in the Node server (createHash("sha256").digest("hex").slice(0, 16)).
	got := DiffDirFor("/data", "owner/repo")
	if filepath.Dir(got) != filepath.Join("/data", "diffs") || len(filepath.Base(got)) != 16 {
		t.Fatalf("DiffDirFor = %s", got)
	}
	if got != DiffDirFor("/data", "owner/repo") || got == DiffDirFor("/data", "owner/other") {
		t.Fatal("the folder must depend on the repo only")
	}
}

func TestCachedDiffRoundTripAndAbsent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "diffs")
	got, err := ReadCachedDiff(dir, shaA)
	if err != nil || got != nil {
		t.Fatalf("absent cache: %v, %v", got, err)
	}
	title, author, at := "fix it", "ana", "2026-01-02T00:00:00Z"
	v := &CachedDiff{
		Info:      &model.CommitInfo{Title: title, Author: &author, At: &at},
		Files:     []File{{Path: "a.go", Status: "modified", Additions: 1, Deletions: 0, Patch: strp("+x\n"), Note: nil}},
		Truncated: true,
	}
	if err := WriteCachedDiff(dir, shaA, v); err != nil {
		t.Fatal(err)
	}
	back, err := ReadCachedDiff(dir, shaA)
	if err != nil || back == nil {
		t.Fatalf("read back: %v, %v", back, err)
	}
	if !reflect.DeepEqual(back, v) {
		t.Fatalf("round trip differs:\n%+v\n%+v", back, v)
	}
	written := readJSON(t, filepath.Join(dir, shaA+".json"))
	if !reflect.DeepEqual(keys(written), []string{"files", "info", "truncated"}) {
		t.Fatalf("cache keys = %v", keys(written))
	}
}

func TestCachedDiffWithoutFilesIsReadAgain(t *testing.T) {
	dir := t.TempDir()
	if err := WriteCachedDiff(dir, shaB, &CachedDiff{Info: &model.CommitInfo{Title: "t"}}); err != nil {
		t.Fatal(err)
	}
	back, err := ReadCachedDiff(dir, shaB)
	if err != nil || back == nil || back.Files == nil || len(back.Files) != 0 {
		t.Fatalf("a cache with no files is written as an empty list: %+v %v", back, err)
	}
}

func TestBrokenCacheFileCountsAsAbsent(t *testing.T) {
	dir := t.TempDir()
	if err := WriteCachedDiff(dir, shaA, &CachedDiff{Files: []File{}}); err != nil {
		t.Fatal(err)
	}
	if err := writeRaw(filepath.Join(dir, shaA+".json"), "{not json"); err != nil {
		t.Fatal(err)
	}
	got, err := ReadCachedDiff(dir, shaA)
	if err != nil || got != nil {
		t.Fatalf("a broken file is read again from the host: %v, %v", got, err)
	}
}

func TestCachedDiffRefusesAShaThatIsNotAFullId(t *testing.T) {
	dir := t.TempDir()
	for _, bad := range []string{"../../x", "../" + shaA, shaA[:39], "A" + shaA[1:], shaA + "/..", "", "abc"} {
		if err := WriteCachedDiff(dir, bad, &CachedDiff{Files: []File{}}); err == nil {
			t.Errorf("WriteCachedDiff accepted %q", bad)
		}
		if _, err := ReadCachedDiff(dir, bad); err == nil {
			t.Errorf("ReadCachedDiff accepted %q", bad)
		}
	}
	if err := WriteCachedDiff(dir, "../../x", &CachedDiff{Files: []File{}}); err == nil {
		t.Fatal("the traversal must be refused")
	}
}

func TestFullSHA(t *testing.T) {
	if !FullSHA.MatchString(shaA) || FullSHA.MatchString(shaA+"\n") || FullSHA.MatchString("A"+shaA[1:]) {
		t.Fatal("FullSHA must match exactly 40 lowercase hex digits")
	}
}
