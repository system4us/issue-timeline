import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { compile, loadVocabulary, vocabulary } from "./vocabulary.js";
import { deriveDeps } from "./derive.js";
import { categorize } from "./ci.js";
import { buildCommitLinks } from "./links.js";
import { gitlabIssueRelations } from "./gitlab.js";

// A small store: issues keyed by number, the way the stores keep them.
const issue = (number, body = "", extra = {}) => ({ number, body, blockedBy: [], blocking: [], ...extra });
const edges = (issues, vocab) => deriveDeps(Object.fromEntries(issues.map((i) => [i.number, i])), vocab).map((e) => `${e.blocker}->${e.blocked}`).sort();

test("the defaults read dependencies in English, Spanish, Portuguese, French and German", () => {
  const issues = [
    issue(1), issue(2), issue(3), issue(4), issue(5), issue(6),
    issue(10, "Depends on #1"),
    issue(11, "Bloqueado por #2"),
    issue(12, "Depende de #3"),
    issue(13, "Bloqué par #4"),
    issue(14, "Bloqueado pelo #5"),
    issue(15, "Abhängig von #6"),
    issue(16, "Blocks #2"),
  ];
  // "Blocks #2" in 16 says 16 blocks 2: the blocker is 16.
  assert.deepEqual(edges(issues), ["1->10", "2->11", "16->2", "3->12", "4->13", "5->14", "6->15"].sort());
});

test("a blocker reference is read to the end of its sentence; a later #N in another sentence is not a blocker", () => {
  const issues = [issue(95), issue(7), issue(20, "Depende de F2a y del outbox (#95). Ver #7 para contexto.")];
  assert.deepEqual(edges(issues), ["95->20"]);
});

test("a custom vocabulary replaces the defaults for the lists it gives", () => {
  const vocab = { blockedBy: ["waits for"], blocks: ["unblocks"], fieldKeys: [], ci: { otherWorkflows: [] } };
  const issues = [issue(1), issue(2), issue(3, "Waits for #1"), issue(4, "Unblocks #2"), issue(5, "Depends on #1")];
  assert.deepEqual(edges(issues, compile(vocab)), ["1->3", "4->2"].sort(), "the defaults no longer apply");
});

test("phrases are literal: regex characters in a phrase do not change its meaning", () => {
  const vocab = compile({ blockedBy: ["need (urgent)"], blocks: [], fieldKeys: [], ci: { otherWorkflows: [] } });
  assert.deepEqual(edges([issue(1), issue(2, "Need (urgent) #1"), issue(3, "Needs urgent #1")], vocab), ["1->2"]);
});

test("loadVocabulary applies VOCABULARY_FILE over the defaults", async () => {
  const dir = await mkdtemp(join(process.env.CLAUDE_JOB_DIR ?? tmpdir(), "vocab-"));
  const file = join(dir, "vocab.json");
  await writeFile(file, JSON.stringify({ blockedBy: ["espera a"], ci: { otherWorkflows: ["nightly"] } }));
  const v = loadVocabulary(file);
  assert.deepEqual(v.blockedBy, ["espera a"]);
  assert.deepEqual(v.ci.otherWorkflows, ["nightly"]);
  assert.deepEqual(v.blocks, vocabulary.blocks, "lists the file does not name keep the defaults");
  assert.ok(vocabulary.blockedBy.includes("depends on"));
});

test("CI: whole words decide the other workflows, so a workflow called Stage is still CI", () => {
  assert.equal(categorize({ name: "Stage", path: ".github/workflows/stage.yml", event: "pull_request" }), "ci");
  assert.equal(categorize({ name: "Deploy docs", path: ".github/workflows/docs.yml", event: "push" }), "other");
  assert.equal(categorize({ name: "CI", path: ".github/workflows/publish.yml", event: "push" }), "other");
  assert.equal(categorize({ name: "CI", path: ".github/workflows/ci.yml", event: "merge_group" }), "ci");
});

test("commits: a GitLab merge request is linked by its 'See merge request' title", () => {
  const pulls = { 7: { number: 7, commits: [], closes: [] } };
  const issues = { 3: issue(3) };
  const links = buildCommitLinks({
    commits: [{ sha: "c1", title: "Merge branch 'x' into 'main'\n\nSee merge request group/project!7" }],
    pulls, issues,
  });
  assert.equal(links.get("c1").pr, 7);
});

test("GitLab issue links: is_blocked_by is a blocker, blocks is blocked; relates_to is not a dependency", () => {
  const r = gitlabIssueRelations([
    { iid: 4, link_type: "is_blocked_by" },
    { iid: 9, link_type: "blocks" },
    { iid: 12, link_type: "relates_to" },
  ]);
  assert.deepEqual(r, { blockedBy: [4], blocking: [9] });
  assert.deepEqual(gitlabIssueRelations(undefined), { blockedBy: [], blocking: [] });
});

test("GitLab commit links: the MR's merge commit links the commit to the MR, whatever its message says", () => {
  const pulls = { 807: { number: 807, commits: [{ oid: "own1" }], closes: [], mergeCommitSha: "df673b91" } };
  const links = buildCommitLinks({
    commits: [
      { sha: "df673b91", title: "Merge branch 'feature/x' into 'master'" },
      { sha: "own1", title: "fix something" },
      { sha: "other", title: "Merge branch 'y' into 'master'" },
    ],
    pulls, issues: {},
  });
  assert.equal(links.get("df673b91").pr, 807);
  assert.equal(links.get("own1").pr, 807);
  assert.equal(links.get("other").pr, null);
});
