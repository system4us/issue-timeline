package derive

// The JS regular expressions of the Node server use lookbehind and lookahead, which Go's regexp does not have. They are
// rewritten here: the regexp finds a candidate, and the boundary is checked on the text around it, as the lookaround
// would. The scan emulates matchAll: after a match, the next search starts at the end of the match, and a candidate
// that fails a boundary is retried one character later (so overlapping candidates are found, as JS finds them).

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// jsSpaceChars is the set JavaScript's \s matches. Go's \s is ASCII only, so the class is spelled out.
const jsSpaceChars = `\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

const (
	jsSpace    = "[" + jsSpaceChars + "]"  // \s
	notJSSpace = "[^" + jsSpaceChars + "]" // \S
)

// tailChars is how far a phrase's "rest of the sentence" reaches: up to 140 characters, not past a line break, a dot or
// a semicolon.
const tailChars = 140

// isWordRune is what a phrase or a key may not touch on either side: [\p{L}\p{N}_].
func isWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r)
}

// wordBefore reports whether the character that ends before byte offset i is a word character (a lookbehind).
func wordBefore(text string, i int) bool {
	if i <= 0 {
		return false
	}
	r, _ := utf8.DecodeLastRuneInString(text[:i])
	return isWordRune(r)
}

// wordAt reports whether the character that starts at byte offset i is a word character (a lookahead).
func wordAt(text string, i int) bool {
	if i >= len(text) {
		return false
	}
	r, _ := utf8.DecodeRuneInString(text[i:])
	return isWordRune(r)
}

// jsWordAt reports whether the character at byte offset i is a JavaScript \w, which with the u and i flags also takes
// the two letters that fold to ASCII (long s and Kelvin sign). A \b after a digit is a boundary when this is false.
func jsWordAt(text string, i int) bool {
	if i >= len(text) {
		return false
	}
	r, _ := utf8.DecodeRuneInString(text[i:])
	switch {
	case r == '_', r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		return true
	case r == 'ſ', r == 'K':
		return true
	}
	return false
}

// runeSizeAt is the byte length of the character at byte offset i (1 at the end, so a scan always moves on).
func runeSizeAt(text string, i int) int {
	if i >= len(text) {
		return 1
	}
	_, size := utf8.DecodeRuneInString(text[i:])
	return size
}

// utf16Len is the length of s in JavaScript's units (UTF-16 code units).
func utf16Len(s string) int {
	return len(utf16.Encode([]rune(s)))
}

// phraseSet is a list of phrases, matched case-insensitively. A phrase matches only between non-word characters, and
// is followed by the rest of its sentence (see tails).
type phraseSet struct {
	// all is every phrase as one alternation, longest first: the first alternative that matches is the longest one.
	// nil when the list is empty.
	all *regexp.Regexp
	// each has one anchored regexp per phrase, in the same order as all. It is used when the longest phrase found does
	// not end at a word boundary and a shorter one may.
	each []*regexp.Regexp
}

// newPhraseSet builds the matcher of a list. Phrases are trimmed, blank ones and repeats are dropped, and they are
// literal (regex characters in a phrase mean nothing).
func newPhraseSet(list []string) phraseSet {
	seen := map[string]bool{}
	var phrases []string
	for _, p := range list {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		phrases = append(phrases, p)
	}
	if len(phrases) == 0 {
		return phraseSet{}
	}
	sort.SliceStable(phrases, func(i, j int) bool { return utf16Len(phrases[i]) > utf16Len(phrases[j]) })
	alts := make([]string, len(phrases))
	each := make([]*regexp.Regexp, len(phrases))
	for i, p := range phrases {
		alts[i] = regexp.QuoteMeta(p)
		each[i] = regexp.MustCompile(`^(?i)(?:` + alts[i] + `)`)
	}
	return phraseSet{
		all:  regexp.MustCompile(`(?i)(?:` + strings.Join(alts, "|") + `)`),
		each: each,
	}
}

// next finds the first phrase match that starts at or after pos and passes the boundaries. It returns the byte offsets
// of the phrase, and false when there is none.
func (ps phraseSet) next(text string, pos int) (start, end int, ok bool) {
	if ps.all == nil {
		return 0, 0, false
	}
	for pos <= len(text) {
		loc := ps.all.FindStringIndex(text[pos:])
		if loc == nil {
			return 0, 0, false
		}
		s := pos + loc[0]
		if !wordBefore(text, s) {
			if e, found := ps.endAt(text, s); found {
				return s, e, true
			}
		}
		pos = s + runeSizeAt(text, s)
	}
	return 0, 0, false
}

// endAt is the end of the first phrase (longest first) that matches at s and is followed by a non-word character.
func (ps phraseSet) endAt(text string, s int) (int, bool) {
	for _, re := range ps.each {
		loc := re.FindStringIndex(text[s:])
		if loc == nil {
			continue
		}
		if e := s + loc[1]; !wordAt(text, e) {
			return e, true
		}
	}
	return 0, false
}

// tails returns, for each phrase match in text in order, the text after it up to the end of its sentence: the first
// group of the JS regex "(?<!w)phrase(?!w)([^\n.;]{0,140})". The next match is searched after that text.
func (ps phraseSet) tails(text string) []string {
	var out []string
	pos := 0
	for {
		_, e, ok := ps.next(text, pos)
		if !ok {
			return out
		}
		end := tailEnd(text, e)
		out = append(out, text[e:end])
		pos = end
	}
}

// tailEnd is the byte offset where the sentence that starts at e ends.
func tailEnd(text string, e int) int {
	i := e
	for n := 0; i < len(text) && n < tailChars; n++ {
		r, size := utf8.DecodeRuneInString(text[i:])
		if r == '\n' || r == '.' || r == ';' {
			break
		}
		i += size
	}
	return i
}
