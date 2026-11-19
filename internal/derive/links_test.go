package derive

import (
	"reflect"
	"testing"

	"gitlab.com/system4us/issue-timeline/internal/model"
)

func commitLinkTestData() (pulls, issues map[string]*model.Record) {
	pulls = map[string]*model.Record{
		"5":  {Key: "5", Closes: refs("10"), Commits: []model.PullCommit{{Oid: "aaa"}, {Oid: "bbb"}}},
		"7":  {Key: "7"},
		"50": {Key: "50"}, // a PR number is never taken for an issue, even when mentioned
	}
	issues = map[string]*model.Record{
		"10": {Key: "10"},
		"20": {Key: "20"},
		"30": {Key: "30"},
	}
	return pulls, issues
}

func TestAPullsOwnCommitsMapExactlyAndItsClosesComeWithThem(t *testing.T) {
	pulls, issues := commitLinkTestData()
	links := BuildCommitLinks([]model.Commit{{Sha: "aaa", Title: "wip"}}, pulls, issues)
	want := CommitLink{PR: strPtr("5"), Issues: []string{"10"}}
	if !reflect.DeepEqual(links["aaa"], want) {
		t.Fatalf("got %+v, want %+v", links["aaa"], want)
	}
}

func TestASquashTitleNamesItsPR(t *testing.T) {
	pulls, issues := commitLinkTestData()
	links := BuildCommitLinks([]model.Commit{{Sha: "x1", Title: "feat: thing (#7)"}}, pulls, issues)
	if !reflect.DeepEqual(links["x1"], CommitLink{PR: strPtr("7"), Issues: []string{}}) {
		t.Fatalf("got %+v", links["x1"])
	}
}

func TestAMergeCommitNamesItsPR(t *testing.T) {
	pulls, issues := commitLinkTestData()
	links := BuildCommitLinks([]model.Commit{{Sha: "m1", Title: "Merge pull request #7 from a/b"}}, pulls, issues)
	if links["m1"].PR == nil || *links["m1"].PR != "7" {
		t.Fatalf("got %+v", links["m1"])
	}
}

func TestClosesFixesKeywordsAndPlainNumbersNameKnownIssuesNotPRs(t *testing.T) {
	pulls, issues := commitLinkTestData()
	links := BuildCommitLinks([]model.Commit{{Sha: "c1", Title: "fix login, closes #20 and see #30 (#50)"}}, pulls, issues)
	want := CommitLink{PR: strPtr("50"), Issues: []string{"20", "30"}}
	if !reflect.DeepEqual(links["c1"], want) {
		t.Fatalf("got %+v, want %+v", links["c1"], want)
	}
}

func TestUnknownNumbersAreIgnored(t *testing.T) {
	pulls, issues := commitLinkTestData()
	links := BuildCommitLinks([]model.Commit{{Sha: "u1", Title: "fixes #999"}}, pulls, issues)
	if !reflect.DeepEqual(links["u1"], CommitLink{PR: nil, Issues: []string{}}) {
		t.Fatalf("got %+v", links["u1"])
	}
}

func TestGitLabMergeRequestIsLinkedByItsSeeMergeRequestTitle(t *testing.T) {
	pulls := map[string]*model.Record{"7": {Key: "7", Commits: []model.PullCommit{}, Closes: []model.Ref{}}}
	issues := map[string]*model.Record{"3": {Key: "3"}}
	links := BuildCommitLinks([]model.Commit{{Sha: "c1", Title: "Merge branch 'x' into 'main'\n\nSee merge request group/project!7"}}, pulls, issues)
	if links["c1"].PR == nil || *links["c1"].PR != "7" {
		t.Fatalf("got %+v", links["c1"])
	}
}

func TestGitLabMergeCommitLinksByMergeCommitShaWhateverItsMessageSays(t *testing.T) {
	pulls := map[string]*model.Record{
		"807": {Key: "807", Commits: []model.PullCommit{{Oid: "own1"}}, MergeCommitSha: strPtr("df673b91")},
	}
	links := BuildCommitLinks([]model.Commit{
		{Sha: "df673b91", Title: "Merge branch 'feature/x' into 'master'"},
		{Sha: "own1", Title: "fix something"},
		{Sha: "other", Title: "Merge branch 'y' into 'master'"},
	}, pulls, map[string]*model.Record{})
	if links["df673b91"].PR == nil || *links["df673b91"].PR != "807" {
		t.Fatalf("merge commit: %+v", links["df673b91"])
	}
	if links["own1"].PR == nil || *links["own1"].PR != "807" {
		t.Fatalf("own commit: %+v", links["own1"])
	}
	if links["other"].PR != nil {
		t.Fatalf("other commit: %+v", links["other"])
	}
}

func TestAnIssueThatIsAlsoAPullIsNotAnIssue(t *testing.T) {
	pulls, issues := commitLinkTestData()
	issues["50"] = &model.Record{Key: "50"} // the same key in both maps: a pull wins
	links := BuildCommitLinks([]model.Commit{{Sha: "c2", Title: "closes #50"}}, pulls, issues)
	if !reflect.DeepEqual(links["c2"].Issues, []string{}) {
		t.Fatalf("got %+v", links["c2"])
	}
}
