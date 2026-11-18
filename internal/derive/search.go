package derive

import (
	"regexp"
	"sort"
	"strings"
	"unicode/utf16"

	"gitlab.com/system4us/issue-timeline/internal/model"
)

// Search inside one repo: by key (#12, or PROJ-12, or label#5 for a linked pull request), by title, and by content (the
// description and the comments). Ranked: a key first, then a title, then the content.

// snippetSize is the length of a snippet in JavaScript's units (UTF-16 code units), as the Node server counts it.
const snippetSize = 90

// defaultSearchLimit is the number of results when the caller gives none.
const defaultSearchLimit = 50

var spaceRunRe = regexp.MustCompile(jsSpace + `+`)

// SearchResult is one match: the record's key, kind, title and state, and for a match in the content, a snippet of
// where it is (nil otherwise).
type SearchResult struct {
	Key     string  `json:"key"`
	Kind    string  `json:"kind"`
	Title   string  `json:"title"`
	State   string  `json:"state"`
	Snippet *string `json:"snippet"`
}

// Search returns the matches of q among records, best first, at most limit (50 when limit is 0 or less). A query is
// trimmed, lowercased, and a leading "#" is dropped; an empty one finds nothing.
func Search(records []*model.Record, q string, limit int) []SearchResult {
	needle := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(q)), "#")
	out := []SearchResult{}
	if needle == "" {
		return out
	}
	type hit struct {
		res   SearchResult
		score int
	}
	var hits []hit
	for _, r := range records {
		if r == nil {
			continue
		}
		key := strings.ToLower(r.Key)
		score := 0
		var snippet *string
		switch {
		case key == needle || strings.HasSuffix(key, "#"+needle) || key == "#"+needle:
			score = 3
		case strings.Contains(strings.ToLower(r.Title), needle):
			score = 2
		default:
			texts := []string{r.Body}
			for _, c := range r.Comments {
				texts = append(texts, c.Body)
			}
			for _, t := range texts {
				if s := snippetAround(t, needle); s != nil {
					snippet = s
					score = 1
					break
				}
			}
		}
		if score > 0 {
			hits = append(hits, hit{res: SearchResult{Key: r.Key, Kind: r.Kind, Title: r.Title, State: r.State, Snippet: snippet}, score: score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return naturalCompare(hits[i].res.Key, hits[j].res.Key) < 0
	})
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	if len(hits) > limit {
		hits = hits[:limit]
	}
	for _, h := range hits {
		out = append(out, h.res)
	}
	return out
}

// snippetAround is the text around the first match of needle (lowercase) in text, 30 units before it and 90 in all,
// with runs of spaces made one, and an ellipsis where the text goes on. nil when there is no match.
func snippetAround(text, needle string) *string {
	byteAt := strings.Index(strings.ToLower(text), needle)
	if byteAt < 0 {
		return nil
	}
	// The index is in units of the lowercased text, and it is applied to the text as the Node server does.
	at := utf16Len(strings.ToLower(text)[:byteAt])
	units := utf16.Encode([]rune(text))
	start := at - 30
	if start < 0 {
		start = 0
	}
	end := start + snippetSize
	if end > len(units) {
		end = len(units)
	}
	s := ""
	if start > 0 {
		s = "…"
	}
	s += spaceRunRe.ReplaceAllString(string(utf16.Decode(units[start:end])), " ")
	if start+snippetSize < len(units) {
		s += "…"
	}
	return &s
}
