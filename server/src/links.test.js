import { test } from "node:test";
import assert from "node:assert/strict";
import { buildCommitLinks } from "./links.js";

const issues = { 10: { number: 10 }, 20: { number: 20 }, 30: { number: 30 } };
const pulls = {
  5: { number: 5, closes: [10], commits: [{ oid: "aaa" }, { oid: "bbb" }] },
  7: { number: 7, closes: [], commits: [] },
  // a PR number is never taken for an issue, even when mentioned
  50: { number: 50, closes: [], commits: [] },
};

test("a PR's own commits map exactly, and the PR's closes come with them", () => {
  const links = buildCommitLinks({ commits: [{ sha: "aaa", title: "wip" }], pulls, issues });
  assert.deepEqual(links.get("aaa"), { pr: 5, issues: [10] });
});

test("a squash title names its PR", () => {
  const links = buildCommitLinks({ commits: [{ sha: "x1", title: "feat: thing (#7)" }], pulls, issues });
  assert.deepEqual(links.get("x1"), { pr: 7, issues: [] });
});

test("a merge commit names its PR", () => {
  const links = buildCommitLinks({ commits: [{ sha: "m1", title: "Merge pull request #7 from a/b" }], pulls, issues });
  assert.equal(links.get("m1").pr, 7);
});

test("closes / fixes keywords and plain #N name known issues, not PRs", () => {
  const links = buildCommitLinks({
    commits: [{ sha: "c1", title: "fix login, closes #20 and see #30 (#50)" }],
    pulls, issues,
  });
  assert.deepEqual(links.get("c1"), { pr: 50, issues: [20, 30] });
});

test("unknown numbers are ignored", () => {
  const links = buildCommitLinks({ commits: [{ sha: "u1", title: "fixes #999" }], pulls, issues });
  assert.deepEqual(links.get("u1"), { pr: null, issues: [] });
});
