// Synthetic data for the timeline tests and benchmarks, at the size of the repo of issue #8:
// 264 issues, 398 pull requests, 749 issue-PR links, 120 tree edges and 49 dependencies (918 lines).
import type { DepEdge, Issue, Link, Pull, TreeEdge } from "./types"
import { linkIndex } from "./filters"
import { makeScale } from "./timeScale"
import type { ColorBy } from "./Timeline"

/** A small seeded generator, so the same data comes out on every run. */
function rng(seed: number) {
  let s = seed >>> 0
  return () => {
    s = (s + 0x6d2b79f5) >>> 0
    let t = s
    t = Math.imul(t ^ (t >>> 15), t | 1)
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61)
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}

const DAY = 86_400_000
const START = Date.parse("2025-01-01T00:00:00Z")
const NOW = START + 700 * DAY
const MILESTONES = ["v1.0", "v1.1", "v2.0", "backlog", "ops"]

export interface TimelineFixture {
  issues: Issue[]
  pulls: Pull[]
  links: Link[]
  tree: TreeEdge[]
  deps: DepEdge[]
}

export function makeFixture(seed = 8): TimelineFixture {
  const rand = rng(seed)
  const pick = <T,>(xs: T[]) => xs[Math.floor(rand() * xs.length)]
  const milestone = () => (rand() < 0.25 ? null : { title: pick(MILESTONES), dueOn: null })
  const base = (key: string, number: number, kind: "issue" | "pull") => ({
    id: `${kind}-${key}`,
    key,
    number,
    title: `${kind === "issue" ? "Issue" : "PR"} ${key}: synthetic title for the timeline benchmark`,
    url: `https://example.test/${kind}/${key}`,
    author: pick(["ana", "beto", "carla", "dani", "ernesto"]),
    labels: rand() < 0.4 ? [{ name: pick(["bug", "feature", "docs"]), color: pick(["d73a4a", "a2eeef", "0075ca"]) }] : [],
    assignees: [],
    milestone: milestone(),
    commentsCount: 0,
    labelEvents: 0,
  })

  const issues: Issue[] = []
  for (let n = 1; n <= 264; n++) {
    const created = START + Math.floor(rand() * 600) * DAY
    const closed = rand() < 0.6
    const closedAt = closed ? new Date(created + (1 + Math.floor(rand() * 60)) * DAY).toISOString() : null
    issues.push({
      ...base(String(n), n, "issue"),
      kind: "issue",
      state: closed ? "CLOSED" : "OPEN",
      stateReason: null,
      createdAt: new Date(created).toISOString(),
      updatedAt: closedAt ?? new Date(created).toISOString(),
      closedAt,
    })
  }

  const pulls: Pull[] = []
  for (let n = 265; n <= 662; n++) {
    const created = START + Math.floor(rand() * 600) * DAY
    const r = rand()
    const state: Pull["state"] = r < 0.7 ? "MERGED" : r < 0.8 ? "CLOSED" : "OPEN"
    const endAt = state === "OPEN" ? null : new Date(created + (1 + Math.floor(rand() * 20)) * DAY).toISOString()
    pulls.push({
      ...base(String(n), n, "pull"),
      kind: "pull",
      state,
      isDraft: false,
      createdAt: new Date(created).toISOString(),
      updatedAt: endAt ?? new Date(created).toISOString(),
      closedAt: state === "MERGED" ? null : endAt,
      mergedAt: state === "MERGED" ? endAt : null,
      mergedBy: null,
      reviewDecision: null,
      base: "main",
      head: `feature/${n}`,
      additions: Math.floor(rand() * 400),
      deletions: Math.floor(rand() * 200),
      changedFiles: 1 + Math.floor(rand() * 12),
      closes: [],
      commitsCount: 1 + Math.floor(rand() * 8),
      firstReviewAt: null,
      reviewsCount: Math.floor(rand() * 3),
    })
  }

  // 749 distinct issue-PR pairs: 70 "closes", 679 "event"
  const pairs = new Set<string>()
  const links: Link[] = []
  while (links.length < 749) {
    const issue = String(1 + Math.floor(rand() * 264))
    const pull = String(265 + Math.floor(rand() * 398))
    if (pairs.has(`${issue}|${pull}`)) continue
    pairs.add(`${issue}|${pull}`)
    links.push({ issue, pull, via: links.filter((l) => l.via === "closes").length < 70 && rand() < 0.1 ? "closes" : "event" })
  }

  // 120 parent -> child edges, always from a lower number to a higher one (no cycles)
  const treePairs = new Set<string>()
  const tree: TreeEdge[] = []
  while (tree.length < 120) {
    const a = 1 + Math.floor(rand() * 263)
    const b = a + 1 + Math.floor(rand() * (264 - a))
    if (treePairs.has(`${a}|${b}`)) continue
    treePairs.add(`${a}|${b}`)
    tree.push({ parent: String(a), child: String(b), via: rand() < 0.5 ? "native" : "checklist", checked: null })
  }

  // 49 blocker -> blocked edges
  const depPairs = new Set<string>()
  const deps: DepEdge[] = []
  while (deps.length < 49) {
    const a = 1 + Math.floor(rand() * 264)
    const b = 1 + Math.floor(rand() * 264)
    if (a === b || depPairs.has(`${a}|${b}`)) continue
    depPairs.add(`${a}|${b}`)
    deps.push({ blocker: String(a), blocked: String(b), via: rand() < 0.5 ? "native" : "text" })
  }

  return { issues, pulls, links, tree, deps }
}

/** The props of <Timeline> for the fixture, with "all links", the tree and the dependencies on (the case of issue #8). */
export function timelineProps(f: TimelineFixture, overrides: { showAllLinks?: boolean; showTree?: boolean; showDeps?: boolean; colorBy?: ColorBy } = {}) {
  const times = [...f.issues, ...f.pulls].flatMap((i) => [Date.parse(i.createdAt), Date.parse(i.closedAt ?? i.createdAt)])
  return {
    issues: f.issues,
    pulls: f.pulls,
    links: f.links,
    tree: f.tree,
    deps: f.deps,
    scale: makeScale("tiempo", times, 4),
    now: NOW,
    colorBy: overrides.colorBy ?? ("estado" as ColorBy),
    showAllLinks: overrides.showAllLinks ?? true,
    openToNow: false,
    showTree: overrides.showTree ?? true,
    showDeps: overrides.showDeps ?? true,
    groupBy: "milestone" as const,
    prefer: [],
    focusKey: "bench",
    idx: linkIndex(f.issues, f.pulls, f.links, f.tree, f.deps),
    selected: null,
    onSelect: () => {},
  }
}
