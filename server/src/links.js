// Links between commits and the pull requests and issues they belong to. Everything is read from the stores (no call
// to the host): a PR's own commits are exact; for the others the title tells, as GitHub writes it ("... (#584)" for a
// squash, "Merge pull request #584" for a merge commit), and "closes #N" / "fixes #N" / "#N" name the issues.

// GitHub: "... (#584)" for a squash, "Merge pull request #584". GitLab: "See merge request group/project!584" (also the
// title of a merge commit of an MR), where the number after "!" is the MR's iid.
const PR_IN_TITLE = [/\(#(\d+)\)\s*$/, /^Merge pull request #(\d+)\b/, /See merge request \S+!(\d+)/];
const CLOSING = /\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)\s*:?\s+#(\d+)/gi;
const ANY_REF = /#(\d+)\b/g;

/** Maps every known commit (by sha) to { pr, issues }. `pulls` and `issues` are the stores' records keyed by number. */
export function buildCommitLinks({ commits, pulls, issues }) {
  // A PR's own commits: exact, and the PR's "closes" are the issues it closes.
  const prOfSha = new Map();
  for (const p of Object.values(pulls)) {
    for (const c of p.commits ?? []) if (!prOfSha.has(c.oid)) prOfSha.set(c.oid, p.number);
    // The commit a merge or a squash left on the branch: GitLab's MR says which one it is, whatever the title says.
    if (p.mergeCommitSha && !prOfSha.has(p.mergeCommitSha)) prOfSha.set(p.mergeCommitSha, p.number);
  }
  const isPr = (n) => !!pulls[n];
  const isIssue = (n) => !!issues[n] && !isPr(n);
  const links = new Map();
  for (const c of commits) {
    const title = c.title ?? c.message ?? "";
    let pr = prOfSha.get(c.sha) ?? null;
    if (pr === null) {
      for (const re of PR_IN_TITLE) {
        const n = Number(re.exec(title)?.[1]);
        if (n && isPr(n)) { pr = n; break; }
      }
    }
    const found = new Set(pr ? pulls[pr].closes ?? [] : []);
    for (const m of title.matchAll(CLOSING)) if (isIssue(Number(m[1]))) found.add(Number(m[1]));
    for (const m of title.matchAll(ANY_REF)) if (isIssue(Number(m[1]))) found.add(Number(m[1]));
    links.set(c.sha, { pr, issues: [...found].filter(isIssue).sort((a, b) => a - b) });
  }
  return links;
}
