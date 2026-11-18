// What the frontend needs, derived from the store: light records (no bodies, comments or commits) and the
// issue <-> pull request links ("closes #N" references, cross-reference events, "#N" mentions in a PR's body).
import { compile, vocabulary as vocabularyDefaults } from "./vocabulary.js";
const light = (r) => {
  const { body, comments, events, commits, reviews, subIssues, blockedBy, blocking, ...rest } = r;
  return {
    ...rest,
    firstReviewAt: reviews ? reviews.map((x) => x.at).filter(Boolean).sort()[0] ?? null : undefined,
    reviewsCount: reviews?.length,
    labelEvents: events.filter((e) => e.type === "Labeled" || e.type === "Unlabeled").length,
  };
};

export function deriveLinks(issues, pulls) {
  const links = new Map(); // "issue:pull" -> via
  const add = (issue, pull, via) => {
    if (!issues[issue] || !pulls[pull]) return;
    const key = `${issue}:${pull}`;
    const rank = { closes: 0, event: 1, mention: 2 };
    if (!links.has(key) || rank[via] < rank[links.get(key)]) links.set(key, via);
  };
  for (const p of Object.values(pulls)) {
    for (const n of p.closes) add(n, p.number, "closes");
    for (const e of p.events) if (e.type === "CrossReferenced" && e.sourceKind === "Issue") add(e.source, p.number, "event");
    for (const m of (p.body ?? "").matchAll(/#(\d+)/g)) add(Number(m[1]), p.number, "mention");
  }
  for (const i of Object.values(issues)) {
    for (const e of i.events) if (e.type === "CrossReferenced" && e.sourceKind === "PullRequest") add(i.number, e.source, "event");
  }
  return [...links].map(([key, via]) => {
    const [issue, pull] = key.split(":").map(Number);
    return { issue, pull, via };
  });
}

/**
 * Parent -> child edges between issues: GitHub's native sub-issues, and the checklists an epic keeps in its body
 * ("- [ ] #12 title" / "- [x] #13"): that is how most epics are written. `checked` is the box of a checklist item.
 */
export function deriveTree(issues) {
  const edges = new Map(); // "parent:child" -> { via, checked }
  const add = (parent, child, via, checked) => {
    if (parent === child || !issues[parent] || !issues[child]) return;
    const key = `${parent}:${child}`;
    if (!edges.has(key) || via === "native") edges.set(key, { via, checked });
  };
  for (const i of Object.values(issues)) {
    for (const c of i.subIssues ?? []) add(i.number, c, "native", null);
    if (i.parent) add(i.parent, i.number, "native", null);
    for (const m of (i.body ?? "").matchAll(/^[ \t]*(?:[-*]|\d+\.)[ \t]+\[([ xX])\][^\n]*?#(\d+)/gm)) add(i.number, Number(m[2]), "checklist", m[1] !== " ");
  }
  return [...edges].map(([key, v]) => {
    const [parent, child] = key.split(":").map(Number);
    return { parent, child, ...v };
  });
}

/**
 * Dependencies between issues, blocker -> blocked: the native "blocked by" / "blocking" (GitHub, and GitLab's issue links),
 * and what is written in a body in the words of the vocabulary ("Depends on #12", "Bloqueado por #9", "Bloquea #64"...).
 * The phrase is read to the end of its sentence, so "Depends on F2a and the outbox (#95)" finds #95 and a later "#7" in
 * another sentence does not.
 */
export function deriveDeps(issues, vocab = compile()) {
  const edges = new Map(); // "blocker:blocked" -> via
  const add = (blocker, blocked, via) => {
    if (blocker === blocked || !issues[blocker] || !issues[blocked]) return;
    const key = `${blocker}:${blocked}`;
    if (!edges.has(key) || via === "native") edges.set(key, via);
  };
  for (const i of Object.values(issues)) {
    for (const n of i.blockedBy ?? []) add(n, i.number, "native");
    for (const n of i.blocking ?? []) add(i.number, n, "native");
    const body = i.body ?? "";
    for (const m of body.matchAll(vocab.blockedBy)) for (const r of m[1].matchAll(/#(\d+)/g)) add(Number(r[1]), i.number, "text");
    for (const m of body.matchAll(vocab.blocks)) for (const r of m[1].matchAll(/#(\d+)/g)) add(i.number, Number(r[1]), "text");
  }
  return [...edges].map(([key, via]) => {
    const [blocker, blocked] = key.split(":").map(Number);
    return { blocker, blocked, via };
  });
}

export function buildPayload(data, vocabulary = vocabularyDefaults) {
  const issues = Object.values(data.issues).map(light).sort((a, b) => a.number - b.number);
  const pulls = Object.values(data.pulls).map(light).sort((a, b) => a.number - b.number);
  return {
    meta: data.meta, issues, pulls, links: deriveLinks(data.issues, data.pulls), tree: deriveTree(data.issues),
    deps: deriveDeps(data.issues, compile(vocabulary)), vocabulary,
  };
}
