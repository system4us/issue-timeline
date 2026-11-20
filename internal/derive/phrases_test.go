package derive

import (
	"strings"
	"testing"
	"unicode/utf8"

	"gitlab.com/system4us/issue-timeline/internal/model"
)

// Edge cases of the lookaround rewrites: the results must be the ones the JS regexes give.

func TestPhraseAtTheStartOfTextIsFound(t *testing.T) {
	equalStrings(t, depsOf(DefaultVocabulary(), numRec(1, "blocks #2"), numRec(2, "")), []string{"1->2"})
}

func TestPhraseAtTheEndOfTextHasNoSentenceAndNoEdge(t *testing.T) {
	equalStrings(t, depsOf(DefaultVocabulary(), numRec(1, "x"), numRec(3, "depends on")), []string{})
}

// The blocked-by phrase reads 140 characters. A phrase that starts right after them is still a match when the last of
// those characters is not a word character (the lookbehind looks at the text, not at the match before).
func TestPhraseRightAfterAFullTailIsFoundWhenTheCharacterBeforeIsNotAWord(t *testing.T) {
	body := "Depends on" + " #1 " + strings.Repeat("x", 135) + " " + "blocks #2"
	issues := []*model.Record{numRec(1, ""), numRec(2, ""), numRec(3, body)}
	equalStrings(t, depsOf(DefaultVocabulary(), issues...), []string{"1->3", "3->2"})
}

func TestPhraseRightAfterAFullTailIsNotFoundWhenTheCharacterBeforeIsAWord(t *testing.T) {
	body := "Depends on" + " #1 " + strings.Repeat("x", 136) + "blocks #2"
	issues := []*model.Record{numRec(1, ""), numRec(2, ""), numRec(3, body)}
	equalStrings(t, depsOf(DefaultVocabulary(), issues...), []string{"1->3"})
}

// A longer phrase that is not followed by a boundary gives way to a shorter one that is.
func TestShorterPhraseIsTakenWhenTheLongerOneIsNotWhole(t *testing.T) {
	vocab := Vocabulary{Blocks: []string{"bloquea a", "bloquea"}}
	equalStrings(t, depsOf(vocab, numRec(3, "bloquea ab #5"), numRec(5, "")), []string{"3->5"})
}

func TestDigitsAndUnderscoreAreWordCharactersAroundAPhrase(t *testing.T) {
	cases := map[string]bool{
		"blocks2 #1":  false, // a digit after
		"blocks_ #1":  false, // an underscore after
		"2blocks #1":  false, // a digit before
		"ñblocks #1":  false, // a letter before
		"¿blocks #1":  true,  // punctuation before
		"(blocks) #1": true,  // a parenthesis after: the phrase ends at a boundary
	}
	for body, want := range cases {
		got := len(depsOf(Vocabulary{Blocks: []string{"blocks"}}, numRec(3, body), numRec(1, ""))) == 1
		if got != want {
			t.Errorf("%q: edge found = %v, want %v", body, got, want)
		}
	}
}

func TestPhraseIsCaseInsensitive(t *testing.T) {
	equalStrings(t, depsOf(DefaultVocabulary(), numRec(3, "BLOCKS #4"), numRec(4, "")), []string{"3->4"})
}

func TestTailStopsAtALineBreakAndASemicolon(t *testing.T) {
	issues := []*model.Record{numRec(1, ""), numRec(2, ""), numRec(3, "Blocks #1\n#2; #4")}
	equalStrings(t, depsOf(DefaultVocabulary(), issues...), []string{"3->1"})
}

func TestTailStopsAt140Characters(t *testing.T) {
	// " " + 139 letters is exactly 140 characters: the " #9" after them is past the end of the sentence.
	past := "blocks" + " " + strings.Repeat("a", 139) + " #9"
	// " " + 135 letters + " #9" is 139 characters: inside.
	inside := "blocks" + " " + strings.Repeat("a", 135) + " #9"
	issues := []*model.Record{numRec(1, past), numRec(2, inside), numRec(9, ""), numRec(7, "")}
	equalStrings(t, depsOf(DefaultVocabulary(), issues...), []string{"2->9"})
}

func TestTailCountsCharactersNotBytes(t *testing.T) {
	// 135 letters of two bytes each are 270 bytes but 135 characters: "#9" is inside the 140 characters.
	body := "blocks " + strings.Repeat("é", 135) + " #9"
	if utf8.RuneCountInString(body) <= 140 || len(body) <= 140 {
		t.Fatal("test setup: the text must be longer than 140 in both bytes and characters")
	}
	equalStrings(t, depsOf(DefaultVocabulary(), numRec(2, body), numRec(9, "")), []string{"2->9"})
}

func TestPhrasesAreTrimmedAndDeduplicated(t *testing.T) {
	vocab := Vocabulary{Blocks: []string{"  blocks  ", "blocks", "", "   "}}
	equalStrings(t, depsOf(vocab, numRec(1, "blocks #2"), numRec(2, "")), []string{"1->2"})
}

func TestEmptyListMatchesNothing(t *testing.T) {
	equalStrings(t, depsOf(Vocabulary{}, numRec(1, "blocks #2 depends on #3"), numRec(2, ""), numRec(3, "")), []string{})
}

func TestMatchesInsideATailAreSkippedLikeMatchAll(t *testing.T) {
	// As with matchAll, the search resumes after the tail of a match: "b c" lies inside the tail of "a b", so it is
	// not a match of its own.
	ps := newPhraseSet([]string{"a b", "b c"})
	equalStrings(t, ps.tails("a b c"), []string{" c"})
}

func TestNewPhraseSetOrdersLongestFirst(t *testing.T) {
	ps := newPhraseSet([]string{"ab", "abc", "a"})
	if len(ps.each) != 3 || !ps.each[0].MatchString("abc") {
		t.Fatal("the longest phrase must come first")
	}
}
