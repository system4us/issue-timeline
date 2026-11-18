package derive

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/store"
)

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func refs(list ...string) []model.Ref {
	out := make([]model.Ref, len(list))
	for i, s := range list {
		out[i] = model.Ref(s)
	}
	return out
}

func recs(list ...*model.Record) map[string]*model.Record {
	m := map[string]*model.Record{}
	for _, r := range list {
		m[r.Key] = r
	}
	return m
}

// ev is an event of the given type with its fields (name, value pairs).
func ev(typ string, kv ...any) model.Event {
	e := model.Event{Type: typ}
	for i := 0; i+1 < len(kv); i += 2 {
		e.SetField(kv[i].(string), kv[i+1])
	}
	return e
}

func TestByKeySortsNumbersAsNumbersAndTextAsText(t *testing.T) {
	got := []string{"10", "9", "100"}
	sort.SliceStable(got, func(i, j int) bool { return ByKey(got[i], got[j]) < 0 })
	if !reflect.DeepEqual(got, []string{"9", "10", "100"}) {
		t.Fatalf("numbers: %q", got)
	}
	got = []string{"PROJ-10", "PROJ-9", "OPS-1"}
	sort.SliceStable(got, func(i, j int) bool { return ByKey(got[i], got[j]) < 0 })
	if !reflect.DeepEqual(got, []string{"OPS-1", "PROJ-10", "PROJ-9"}) {
		t.Fatalf("text: %q", got)
	}
}

func TestByKeyTreatsOnlyCanonicalNumbersAsNumbers(t *testing.T) {
	// "010" is not what Number("010") writes back ("10"), so it is text: it sorts before "9" as text does.
	if ByKey("010", "9") >= 0 {
		t.Error(`"010" must sort as text, before "9"`)
	}
	if ByKey("10", "9") <= 0 {
		t.Error(`"10" must sort after "9" as a number`)
	}
	if ByKey("7", "7") != 0 {
		t.Error("equal keys compare equal")
	}
}

func TestTextKeyLinksLikeANumber(t *testing.T) {
	issues := recs(&model.Record{Key: "PROJ-12"})
	pulls := recs(&model.Record{Key: "7", Closes: refs("PROJ-12")})
	got := deriveLinks(issues, pulls, LinkOptions{})
	if !reflect.DeepEqual(got, []Link{{Issue: "PROJ-12", Pull: "7", Via: "closes"}}) {
		t.Fatalf("got %+v", got)
	}
}

func TestLinksTiePullsToIssuesByClosesEventsAndMentions(t *testing.T) {
	issues := recs(
		&model.Record{Key: "1"},
		&model.Record{Key: "2", Events: []model.Event{ev("CrossReferenced", "source", 20, "sourceKind", "PullRequest")}},
		&model.Record{Key: "3"},
	)
	pulls := recs(
		&model.Record{Key: "10", Closes: refs("1")},
		&model.Record{Key: "20"},
		&model.Record{Key: "30", Body: "see #3 and #999"},
	)
	got := deriveLinks(issues, pulls, LinkOptions{})
	want := []Link{{Issue: "1", Pull: "10", Via: "closes"}, {Issue: "2", Pull: "20", Via: "event"}, {Issue: "3", Pull: "30", Via: "mention"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestStrongestSourceOfALinkIsKept(t *testing.T) {
	// a key link and a cross-reference have the same rank: the key link was added first, so it stays.
	issues := recs(&model.Record{Key: "12", Events: []model.Event{ev("CrossReferenced", "source", 5, "sourceKind", "PullRequest")}})
	pulls := recs(&model.Record{Key: "5"})
	got := deriveLinks(issues, pulls, LinkOptions{KeyLinks: []Link{{Issue: "12", Pull: "5", Via: "key"}}})
	if !reflect.DeepEqual(got, []Link{{Issue: "12", Pull: "5", Via: "key"}}) {
		t.Fatalf("got %+v", got)
	}
	// a close beats a mention of the same pair
	issues = recs(&model.Record{Key: "3"})
	pulls = recs(&model.Record{Key: "9", Closes: refs("3"), Body: "#3"})
	got = deriveLinks(issues, pulls, LinkOptions{})
	if len(got) != 1 || got[0].Via != "closes" {
		t.Fatalf("got %+v", got)
	}
}

func TestNativeLinksAreOnUnlessSwitchedOff(t *testing.T) {
	issues := recs(&model.Record{Key: "1"})
	pulls := recs(&model.Record{Key: "2", Closes: refs("1")})
	if got := deriveLinks(issues, pulls, LinkOptions{}); len(got) != 1 {
		t.Fatalf("the zero value reads native links: %+v", got)
	}
	if got := deriveLinks(issues, pulls, LinkOptions{Native: model.Ptr(true)}); len(got) != 1 {
		t.Fatalf("Native true reads them: %+v", got)
	}
	if got := deriveLinks(issues, pulls, LinkOptions{Native: model.Ptr(false)}); len(got) != 0 {
		t.Fatalf("Native false reads none: %+v", got)
	}
}

func TestTreeReadsNativeSubIssuesAndChecklistsOfAnEpic(t *testing.T) {
	issues := recs(
		&model.Record{Key: "1", Body: "## Trabajo\n- [ ] #2 first\n- [x] #3 second\n* [ ] see #4 and #1\n- [ ] #99 gone\nplain #5"},
		&model.Record{Key: "2"},
		&model.Record{Key: "3"},
		&model.Record{Key: "4"},
		&model.Record{Key: "5", SubIssues: refs("6"), Parent: refPtr("7")},
		&model.Record{Key: "6"},
		&model.Record{Key: "7"},
	)
	got := deriveTree(issues)
	want := []TreeEdge{
		{Parent: "1", Child: "2", Via: "checklist", Checked: boolPtr(false)},
		{Parent: "1", Child: "3", Via: "checklist", Checked: boolPtr(true)},
		{Parent: "1", Child: "4", Via: "checklist", Checked: boolPtr(false)},
		{Parent: "5", Child: "6", Via: "native"},
		{Parent: "7", Child: "5", Via: "native"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
	// native JSON: checked is null, not left out
	if b := string(mustMarshal(t, want[3])); !strings.Contains(b, `"checked":null`) {
		t.Fatalf("a native edge writes checked as null: %s", b)
	}
}

func TestTreeNativeEdgeReplacesChecklistEdge(t *testing.T) {
	issues := recs(
		&model.Record{Key: "1", Body: "- [ ] #2 child"},
		&model.Record{Key: "2", Parent: refPtr("1")},
	)
	got := deriveTree(issues)
	if len(got) != 1 || got[0].Via != "native" || got[0].Checked != nil {
		t.Fatalf("got %+v", got)
	}
}

func TestDepsReadNativeAndPhrasesOfABody(t *testing.T) {
	issues := recs(
		&model.Record{Key: "1", Body: "Depende de F2a y del outbox (#2). Más abajo #3 no cuenta."},
		&model.Record{Key: "2", Body: "bloquea #4 y #5\nPendiente decidir memoria. Refs #1"},
		&model.Record{Key: "3", BlockedBy: refs("4")},
		&model.Record{Key: "4", Body: "Blocked by #5; blocks #1"},
		&model.Record{Key: "5"},
	)
	got := deriveDeps(issues, DefaultVocabulary())
	var lines []string
	for _, d := range got {
		lines = append(lines, d.Blocker+">"+d.Blocked+":"+d.Via)
	}
	sort.Strings(lines)
	want := []string{"2>1:text", "2>4:text", "2>5:text", "4>1:text", "4>3:native", "5>4:text"}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("got %q, want %q", lines, want)
	}
}

func TestDepsNativeEdgeReplacesTextEdge(t *testing.T) {
	issues := recs(
		&model.Record{Key: "1", Body: "blocks #2"},
		&model.Record{Key: "2", BlockedBy: refs("1")},
	)
	got := deriveDeps(issues, DefaultVocabulary())
	if len(got) != 1 || got[0].Via != "native" {
		t.Fatalf("got %+v", got)
	}
}

func TestDepsIgnoreSelfAndUnknownKeys(t *testing.T) {
	issues := recs(&model.Record{Key: "1", Body: "blocks #1 and blocks #9", BlockedBy: refs("1", "8")})
	if got := deriveDeps(issues, DefaultVocabulary()); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestPayloadIsLight(t *testing.T) {
	d := dataWith(
		&model.Record{Kind: "issue", ID: "1", Key: "1", Body: "x", Comments: []model.Comment{{ID: "c", Body: "big"}}, Events: []model.Event{}, Labels: []model.Label{}},
	)
	p := BuildPayload(d, DefaultVocabulary(), LinkOptions{})
	b := string(mustMarshal(t, p.Issues[0]))
	for _, gone := range []string{`"body"`, `"comments"`, `"events"`} {
		if strings.Contains(b, gone) {
			t.Fatalf("the list keeps %s: %s", gone, b)
		}
	}
}

func TestPayloadHasTheFrontendsFieldsAndNoMore(t *testing.T) {
	d := dataWith(&model.Record{Kind: "issue", ID: "1", Key: "1"})
	var top map[string]json.RawMessage
	if err := json.Unmarshal(mustMarshal(t, BuildPayload(d, DefaultVocabulary(), LinkOptions{})), &top); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for k := range top {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	want := []string{"deps", "issues", "links", "meta", "pulls", "tree", "vocabulary"}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("payload keys %q, want %q", keys, want)
	}
}

func TestPayloadLightFields(t *testing.T) {
	d := dataWith(
		&model.Record{Kind: "pull", ID: "5", Key: "5", Reviews: []model.Review{
			{State: "APPROVED", At: strPtr("2026-03-02T00:00:00Z")},
			{State: "COMMENTED", At: strPtr("2026-03-01T00:00:00Z")},
			{State: "PENDING", At: nil},
		}, Events: []model.Event{ev("Labeled"), ev("Unlabeled"), ev("Closed")}},
		&model.Record{Kind: "pull", ID: "6", Key: "6"},
	)
	d.Pulls["6"].Events = nil
	p := BuildPayload(d, DefaultVocabulary(), LinkOptions{})
	m := lightMap(t, p.Pulls[0])
	if string(m["reviewsCount"]) != "3" || string(m["firstReviewAt"]) != `"2026-03-01T00:00:00Z"` || string(m["labelEvents"]) != "2" {
		t.Fatalf("with reviews: %s %s %s", m["reviewsCount"], m["firstReviewAt"], m["labelEvents"])
	}
	m = lightMap(t, p.Pulls[1])
	if _, ok := m["reviewsCount"]; ok {
		t.Fatal("a record without reviews leaves reviewsCount out")
	}
	if _, ok := m["firstReviewAt"]; ok {
		t.Fatal("a record without reviews leaves firstReviewAt out")
	}
	if string(m["labelEvents"]) != "0" {
		t.Fatalf("labelEvents = %s", m["labelEvents"])
	}
}

func TestPayloadWithReviewsButNoReviewTimeWritesNull(t *testing.T) {
	d := dataWith(&model.Record{Kind: "pull", ID: "5", Key: "5", Reviews: []model.Review{{State: "PENDING"}}})
	p := BuildPayload(d, DefaultVocabulary(), LinkOptions{})
	m := lightMap(t, p.Pulls[0])
	if string(m["firstReviewAt"]) != "null" || string(m["reviewsCount"]) != "1" {
		t.Fatalf("firstReviewAt %s, reviewsCount %s", m["firstReviewAt"], m["reviewsCount"])
	}
}

func TestPayloadKeepsUnknownFieldsOfARecord(t *testing.T) {
	r := &model.Record{Kind: "issue", ID: "1", Key: "1"}
	r.Extra = map[string]json.RawMessage{"vendorField": json.RawMessage(`{"a":1}`)}
	m := lightMap(t, BuildPayload(dataWith(r), DefaultVocabulary(), LinkOptions{}).Issues[0])
	if string(m["vendorField"]) != `{"a":1}` {
		t.Fatalf("vendorField = %s", m["vendorField"])
	}
}

func TestPayloadIsSortedByKey(t *testing.T) {
	d := dataWith(&model.Record{Kind: "issue", ID: "10", Key: "10"}, &model.Record{Kind: "issue", ID: "9", Key: "9"})
	p := BuildPayload(d, DefaultVocabulary(), LinkOptions{})
	if p.Issues[0].Record.Key != "9" || p.Issues[1].Record.Key != "10" {
		t.Fatalf("order: %s, %s", p.Issues[0].Record.Key, p.Issues[1].Record.Key)
	}
}

func TestPayloadDoesNotShareTheStoresMeta(t *testing.T) {
	d := dataWith(&model.Record{Kind: "issue", ID: "1", Key: "1"})
	d.Meta.Syncs = []store.SyncEntry{{At: "a", Issues: 1}}
	d.Meta.LastSync = strPtr("first")
	p := BuildPayload(d, DefaultVocabulary(), LinkOptions{})
	d.Meta.Syncs[0].Issues = 99
	*d.Meta.LastSync = "changed"
	if p.Meta.Syncs[0].Issues != 1 || *p.Meta.LastSync != "first" {
		t.Fatal("the payload must not change when the store does")
	}
}

func TestPayloadVocabularyIsTheOneGiven(t *testing.T) {
	v := Vocabulary{BlockedBy: []string{"wait"}}
	p := BuildPayload(dataWith(), v, LinkOptions{})
	if !reflect.DeepEqual(p.Vocabulary.BlockedBy, []string{"wait"}) || p.Vocabulary.Blocks == nil {
		t.Fatalf("vocabulary %+v", p.Vocabulary)
	}
}

func TestPayloadRoutesLinkOptions(t *testing.T) {
	d := dataWith(
		&model.Record{Kind: "issue", ID: "12", Key: "12"},
		&model.Record{Kind: "pull", ID: "o/r#3", Key: "o/r#3", Closes: refs("12")},
	)
	p := BuildPayload(d, DefaultVocabulary(), LinkOptions{Native: model.Ptr(false), KeyLinks: []Link{{Issue: "12", Pull: "o/r#3", Via: "key"}}})
	if len(p.Links) != 1 || p.Links[0].Via != "key" {
		t.Fatalf("links %+v", p.Links)
	}
}

func dataWith(list ...*model.Record) *store.Data {
	d := &store.Data{Meta: store.Meta{Schema: store.Schema, Syncs: []store.SyncEntry{}}, Issues: map[string]*model.Record{}, Pulls: map[string]*model.Record{}}
	for _, r := range list {
		if r.Kind == "pull" {
			d.Pulls[r.Key] = r
		} else {
			d.Issues[r.Key] = r
		}
	}
	return d
}

func lightMap(t *testing.T, l Light) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(mustMarshal(t, l), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func refPtr(s string) *model.Ref {
	r := model.Ref(s)
	return &r
}

func strPtr(s string) *string { return &s }

func boolPtr(b bool) *bool { return &b }
