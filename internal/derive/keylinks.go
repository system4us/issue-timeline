package derive

import (
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"gitlab.com/system4us/issue-timeline/internal/model"
)

// Links by key: a pull request (or one of its commits) names an issue of the tracker by the tracker's own key, in its
// title, its branch or its commit messages. Jira keys are "PROJ-12" in the project of the tracker; Redmine ids are
// written the way Redmine asks for them ("refs #12", "fixes #12").

// KeyPattern finds a tracker's keys in a text. It is nil for a repo that has no keyed links.
type KeyPattern struct {
	// re has the key as its first group, without the lookarounds.
	re *regexp.Regexp
	// endOK checks the character after the match (the lookahead, or the \b of Redmine's pattern).
	endOK func(text string, i int) bool
}

var redmineKeyRe = regexp.MustCompile(`(?i)(?:refs?|fix(?:e[sd])?|closes?|resolves?)` + jsSpace + `*:?` + jsSpace + `+#([0-9]+)`)

// KeyPatternFor is the pattern of a repo key: a Jira project ("jira:https://site/PROJ") finds its PROJ-N keys, a
// Redmine project ("redmine:...") finds "refs #N" / "fixes #N". Any other repo, or a Jira key without a project name,
// gives nil.
func KeyPatternFor(repoKey string) *KeyPattern {
	if strings.HasPrefix(repoKey, "jira:") {
		u, err := url.Parse(strings.TrimPrefix(repoKey, "jira:"))
		if err != nil || !u.IsAbs() {
			return nil
		}
		var project string
		for _, seg := range strings.Split(u.EscapedPath(), "/") {
			if seg != "" {
				project = seg
			}
		}
		if project == "" {
			return nil
		}
		return &KeyPattern{
			re:    regexp.MustCompile("(" + regexp.QuoteMeta(project) + `-[0-9]+)`),
			endOK: func(text string, i int) bool { return !wordAt(text, i) },
		}
	}
	if strings.HasPrefix(repoKey, "redmine:") {
		return &KeyPattern{
			re:    redmineKeyRe,
			endOK: func(text string, i int) bool { return !jsWordAt(text, i) },
		}
	}
	return nil
}

// FindAll returns the keys the text names, in order, as the first group of each match. A key must not touch a letter,
// a digit or an underscore on either side (the Jira key "xPROJ-12" is not a link, and "PROJ-12PROJ-3" names none).
func (p *KeyPattern) FindAll(text string) []string {
	if p == nil {
		return nil
	}
	var out []string
	pos := 0
	for pos <= len(text) {
		loc := p.re.FindStringSubmatchIndex(text[pos:])
		if loc == nil {
			break
		}
		s, e := pos+loc[0], pos+loc[1]
		if !wordBefore(text, s) && p.endOK(text, e) {
			out = append(out, text[pos+loc[2]:pos+loc[3]])
			pos = e
			continue
		}
		_, size := utf8.DecodeRuneInString(text[s:])
		pos = s + size
	}
	return out
}

// TextsOf is the texts of a pull request that can name an issue: its title, its head branch and its commits' messages.
// Empty texts are left out.
func TextsOf(pull *model.Record) []string {
	var out []string
	add := func(s string) {
		if s != "" {
			out = append(out, s)
		}
	}
	add(pull.Title)
	add(pull.Head)
	for _, c := range pull.Commits {
		add(c.Message)
	}
	return out
}

// DeriveKeyLinks finds the links the pattern gives between the pulls and the issues: one Link per pair, via "key". Only
// issues the tracker has count. The order is the order of the pulls, then of their texts.
func DeriveKeyLinks(issues map[string]*model.Record, pulls []*model.Record, p *KeyPattern) []Link {
	out := []Link{}
	if p == nil {
		return out
	}
	seen := map[pairKey]bool{}
	for _, pull := range pulls {
		for _, text := range TextsOf(pull) {
			for _, key := range p.FindAll(text) {
				if issues[key] == nil {
					continue
				}
				k := pairKey{key, pull.Key}
				if seen[k] {
					continue
				}
				seen[k] = true
				out = append(out, Link{Issue: key, Pull: pull.Key, Via: "key"})
			}
		}
	}
	return out
}
