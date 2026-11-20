import { describe, expect, test } from "vitest"
import { linkIndex } from "./filters"
import { makeFixture, type TimelineFixture } from "./timelineFixture"
import {
  BAR,
  BUS_GAP,
  backgroundGroups,
  barDerived,
  barModel,
  busOf,
  combineByClass,
  depRoute,
  elbowRoute,
  foregroundSingles,
  linkRoute,
  route,
  type Box,
  type LayerInput,
} from "./timelineLayers"
import type { Item } from "./types"

// Bars at fixed places, one row each, the way the layout puts them (the positions are not the point here)
function boxesOf(f: TimelineFixture) {
  const boxes = new Map<string, Box>()
  ;[...f.issues, ...f.pulls].forEach((i, row) => {
    const x1 = 20 + ((Number(i.key) * 37) % 900)
    boxes.set(`${i.kind}-${i.key}`, { x1, x2: x1 + 40 + (Number(i.key) % 50), y: 30 + row * 18 })
  })
  return boxes
}

function inputOf(f: TimelineFixture, over: Partial<LayerInput> = {}): LayerInput {
  const items = new Map<string, Item>([...f.issues, ...f.pulls].map((i) => [`${i.kind}-${i.key}`, i]))
  return {
    boxes: boxesOf(f),
    items,
    elbows: [],
    links: f.links,
    tree: f.tree,
    deps: f.deps,
    showAllLinks: true,
    showTree: true,
    treeLines: true,
    showDeps: true,
    ...over,
  }
}

const segments = (d: string) => d.split(/ (?=M)/)

describe("routes", () => {
  test("a route is straight runs from point to point", () => {
    expect(route([[1, 2], [3.5, 4]])).toBe("M1.0,2.0 L3.5,4.0")
  })

  test("a link goes down from its issue, across the gap between the rows, and down into its PR", () => {
    const a = { x1: 10, x2: 50, y: 0 }
    const b = { x1: 100, x2: 150, y: 36 }
    expect(linkRoute(a, b)).toBe(`M50.0,${BAR}.0 L50.0,24.5 L100.0,24.5 L100.0,36.0`)
  })

  test("a dependency runs along the bus and lands on the left edge of the blocked bar", () => {
    const a = { x1: 10, x2: 50, y: 0 }
    const b = { x1: 100, x2: 150, y: 36 }
    expect(depRoute(a, b, 30)).toBe("M50.0,6.5 L30.0,6.5 L30.0,42.5 L98.0,42.5")
  })

  test("a tree bracket goes down from the parent and across to where the child starts", () => {
    expect(elbowRoute({ x1: 10, x2: 60, y: 0 }, { x1: 100, x2: 150, y: 36 })).toBe("M16,13 V42.5 H100")
  })

  test("the bus sits just left of every dependency drawn, from the top of the first to the bottom of the last", () => {
    const bus = busOf([{ a: { x1: 10, x2: 50, y: 0 }, b: { x1: 100, x2: 150, y: 36 } }, { a: { x1: 200, x2: 260, y: 72 }, b: { x1: 300, x2: 340, y: 18 } }])
    expect(bus?.x).toBe(50 - BUS_GAP) // the smallest end: the first bar's right edge
    expect(bus?.d).toBe(`M36.0,6.5 L36.0,78.5`)
    expect(busOf([])).toBeNull()
  })
})

describe("combining the lines of a class into one path", () => {
  test("one path per class, the segments in the order they first appear, and every segment kept", () => {
    const out = combineByClass([
      { cls: "a", d: "M1,1" },
      { cls: "b", d: "M2,2" },
      { cls: "a", d: "M3,3 L4,4" },
    ])
    expect(out).toEqual([
      { cls: "a", d: "M1,1 M3,3 L4,4", count: 2 },
      { cls: "b", d: "M2,2", count: 1 },
    ])
  })

  test("the background holds every link and tree edge as its own segment, grouped by class", () => {
    const f = makeFixture()
    const input = inputOf(f, { showDeps: false })
    const groups = backgroundGroups(input)
    const expected = new Map<string, string[]>()
    const add = (cls: string, d: string) => expected.set(cls, [...(expected.get(cls) ?? []), d])
    for (const l of f.links) add(`line link ${l.via} faint`, linkRoute(input.boxes.get(`issue-${l.issue}`)!, input.boxes.get(`pull-${l.pull}`)!))
    for (const e of f.tree) add(`line tree ${e.via}`, linkRoute(input.boxes.get(`issue-${e.parent}`)!, input.boxes.get(`issue-${e.child}`)!))

    // a handful of paths, not one per line
    expect(groups.length).toBeLessThanOrEqual(6)
    expect(groups.reduce((n, g) => n + g.count, 0)).toBe(f.links.length + f.tree.length)
    for (const [cls, ds] of expected) {
      const g = groups.find((x) => x.cls === cls)!
      expect(g.count).toBe(ds.length)
      expect(segments(g.d)).toEqual(ds) // the same "M…" segments as one path per line, in the same order
    }
  })

  test("with every dependency drawn, the bus is in the background", () => {
    const f = makeFixture()
    expect(backgroundGroups(inputOf(f, { showDeps: true })).some((g) => g.cls === "line bus")).toBe(true)
    expect(backgroundGroups(inputOf(f, { showDeps: false })).some((g) => g.cls === "line bus")).toBe(false)
  })
})

describe("the two layers", () => {
  test("the background does not depend on the focus: only the foreground changes with it", () => {
    const f = makeFixture()
    const input = inputOf(f, { showTree: false, showDeps: false })
    const a = f.issues.find((i) => f.deps.some((d) => d.blocker === i.key))!
    const p = f.pulls.find((x) => f.links.some((l) => l.pull === x.key))!
    const bg = backgroundGroups(input)
    const fgA = foregroundSingles(input, a)
    const fgB = foregroundSingles(input, p)
    expect(backgroundGroups(input)).toEqual(bg)
    expect(fgA).not.toEqual(fgB)
    expect(fgA.length).toBeGreaterThan(0)
  })

  test("with no focus and the dependencies drawn, only the dependencies are in the foreground", () => {
    const f = makeFixture()
    const singles = foregroundSingles(inputOf(f, { showTree: true, showDeps: true }), null)
    expect(singles.length).toBe(f.deps.length)
    expect(singles.every((s) => s.id.startsWith("dep-"))).toBe(true)
  })

  test("a focused item's links are drawn in the foreground at full strength, the others faint in the background", () => {
    const f = makeFixture()
    const p = f.pulls.find((x) => f.links.filter((l) => l.pull === x.key).length >= 2)!
    const mine = f.links.filter((l) => l.pull === p.key)
    const singles = foregroundSingles(inputOf(f, { showDeps: false, showTree: true }), p).filter((s) => s.id.startsWith("link-"))
    expect(singles.map((s) => s.id).sort()).toEqual(mine.map((l) => `link-${l.issue}-${l.pull}`).sort())
    for (const s of singles) expect(s.cls.endsWith("faint")).toBe(false)
  })

  test("a focused issue's dependencies are at full strength with an arrow; the others are faint, when they are drawn", () => {
    const f = makeFixture()
    const issue = f.issues.find((i) => f.deps.some((d) => d.blocker === i.key))!
    const involved = f.deps.filter((d) => d.blocker === issue.key || d.blocked === issue.key)
    const drawn = foregroundSingles(inputOf(f, { showDeps: false }), issue).filter((s) => s.id.startsWith("dep-"))
    expect(drawn.length).toBe(involved.length)
    for (const s of drawn) {
      expect(s.cls.endsWith("faint")).toBe(false)
      expect(s.marker).toMatch(/^url\(#arrow-(red|grey)\)$/)
    }
    const all = foregroundSingles(inputOf(f, { showDeps: true }), issue).filter((s) => s.id.startsWith("dep-"))
    expect(all.length).toBe(f.deps.length)
    expect(all.filter((s) => s.cls.endsWith("faint")).length).toBe(f.deps.length - involved.length)
    for (const s of all.filter((x) => x.cls.endsWith("faint"))) expect(s.marker).toMatch(/-soft\)$/)
  })
})

describe("per-bar data", () => {
  test("the derived counts match the links they come from", () => {
    const f = makeFixture()
    const idx = linkIndex(f.issues, f.pulls, f.links, f.tree, f.deps)
    for (const i of f.issues) {
      const d = barDerived(i, idx)
      const kids = idx.childrenOf.get(i.key) ?? []
      expect(d.kids).toBe(kids.length)
      expect(d.kidsClosed).toBe(kids.filter((k) => k.state === "CLOSED").length)
      expect(d.epic).toBe(kids.length > 0)
      expect(d.blockedNow).toBe(i.state === "OPEN" && (idx.blockersOf.get(i.key) ?? []).some((b) => b.state === "OPEN"))
      expect(d.blocksNow).toBe(i.state === "OPEN" ? (idx.blockedOf.get(i.key) ?? []).filter((b) => b.state === "OPEN").length : 0)
    }
    for (const p of f.pulls) {
      const d = barDerived(p, idx)
      expect([d.epic, d.kids, d.blockedNow, d.blocksNow]).toEqual([false, 0, false, 0])
    }
  })

  test("a bar is a summary only when it has children and the tree layout gives it an extent", () => {
    const f = makeFixture()
    const idx = linkIndex(f.issues, f.pulls, f.links, f.tree, f.deps)
    const epic = f.issues.find((i) => (idx.childrenOf.get(i.key) ?? []).length > 0)!
    const b = { x1: 10, x2: 200, y: 30 }
    expect(barModel(epic, b, "red", idx, true, null).summary).toBe(true)
    expect(barModel(epic, b, "red", idx, false, null).summary).toBe(false)
    const leaf = f.issues.find((i) => (idx.childrenOf.get(i.key) ?? []).length === 0)!
    expect(barModel(leaf, b, "red", idx, true, null).summary).toBe(false)
    expect(barModel(leaf, b, "red", idx, true, null).w).toBe(190)
  })
})
