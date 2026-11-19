// The pure parts of the timeline's drawing: the routes of the lines, the two line layers, and what each bar derives
// from the links. No React here: Timeline memoizes these on the data and the layout, and the focus only picks the
// lines of the layer that changes with it.
import type { DepEdge, Item, Link, TreeEdge } from "./types"
import { openBlocked, openBlockers, type LinkIndex } from "./filters"

export const BAR = 13
export const BUS_GAP = 14 // how far left of the dependencies the main bus runs

/** Where a bar is on the canvas: its left and right ends, and its top. */
export interface Box { x1: number; x2: number; y: number }

/** A path through the points in straight runs: the lines read as routes between rows, not as tangles of curves. */
export const route = (pts: [number, number][]) => pts.map(([x, y], k) => `${k ? "L" : "M"}${x.toFixed(1)},${y.toFixed(1)}`).join(" ")

/** Links and tree edges: straight down from the item, across in the gap between the rows, down into the other one. */
export function linkRoute(a: Box, b: Box): string {
  const sx = Math.min(a.x2, Math.max(a.x1, b.x1))
  const sy = a.y + BAR
  const midY = (sy + b.y) / 2
  return route([[sx, sy], [sx, midY], [b.x1, midY], [b.x1, b.y]])
}

/** A dependency: out of the end of the blocker, along the bus, onto the left edge of the blocked bar. */
export function depRoute(a: Box, b: Box, busX: number): string {
  const sx = a.x2
  const sy = a.y + BAR / 2
  const ex = b.x1 - 2 // the arrow lands on the left edge of the blocked bar
  const ey = b.y + BAR / 2
  return route([[sx, sy], [busX, sy], [busX, ey], [ex, ey]])
}

/** The bracket from a parent down and across to where its child starts (the tree layout of a family). */
export function elbowRoute(a: Box, c: Box): string {
  const px = a.x1 + 6
  const cy = c.y + BAR / 2
  // down from the parent along its start, then across to where the child starts
  return `M${px},${a.y + BAR} V${cy} H${Math.max(px + 2, c.x1)}`
}

/** The main bus of the dependencies drawn: one trunk just left of all of them. Null when none is drawn. */
export function busOf(ends: { a: Box; b: Box }[]): { x: number; d: string } | null {
  if (!ends.length) return null
  const x = Math.min(...ends.flatMap(({ a, b }) => [a.x2, b.x1])) - BUS_GAP
  const top = Math.min(...ends.flatMap(({ a, b }) => [a.y, b.y])) + BAR / 2
  const bottom = Math.max(...ends.flatMap(({ a, b }) => [a.y, b.y])) + BAR / 2
  return { x, d: route([[x, top], [x, bottom]]) }
}

export interface Seg { cls: string; d: string }
export interface Group { cls: string; d: string; count: number }

/**
 * One path per class: the "M…" segments of every line with the same class, in the order they first appear. Within a
 * path a stroke that overlaps another of the same path is not painted twice, so overlaps of one class do not add up.
 */
export function combineByClass(segs: Seg[]): Group[] {
  const groups = new Map<string, { d: string[]; count: number }>()
  for (const s of segs) {
    const g = groups.get(s.cls) ?? { d: [], count: 0 }
    g.d.push(s.d)
    g.count++
    groups.set(s.cls, g)
  }
  return [...groups].map(([cls, g]) => ({ cls, d: g.d.join(" "), count: g.count }))
}

/** What the line layers are drawn from. Nothing here depends on the focus (the hovered or selected item). */
export interface LayerInput {
  boxes: Map<string, Box> // "issue-12", "pull-40" -> where the bar is (only the items on screen)
  items: Map<string, Item>
  elbows: { parent: string; child: string }[]
  links: Link[]
  tree: TreeEdge[]
  deps: DepEdge[]
  showAllLinks: boolean // every link is drawn, faint (the focused item's links are drawn over them)
  showTree: boolean // every tree edge is drawn
  treeLines: boolean // tree edges are drawn at all (not in the tree layout, where the elbows stand for them)
  showDeps: boolean // every dependency is drawn
}

/**
 * The background layer: what is drawn whatever is hovered. Memoized by the caller on the data and the layout only,
 * so it is built once and never touched while the pointer moves. A few paths: one per class.
 */
export function backgroundGroups(c: LayerInput): Group[] {
  const segs: Seg[] = []
  for (const e of c.elbows) {
    const a = c.boxes.get(e.parent)
    const b = c.boxes.get(e.child)
    if (a && b) segs.push({ cls: "line elbow", d: elbowRoute(a, b) })
  }
  if (c.showDeps) {
    const bus = busOf(allDepEnds(c))
    if (bus) segs.push({ cls: "line bus", d: bus.d })
  }
  if (c.showAllLinks) {
    for (const l of c.links) {
      const a = c.boxes.get(`issue-${l.issue}`)
      const b = c.boxes.get(`pull-${l.pull}`)
      if (a && b) segs.push({ cls: `line link ${l.via} faint`, d: linkRoute(a, b) })
    }
  }
  if (c.treeLines && c.showTree) {
    for (const e of c.tree) {
      const a = c.boxes.get(`issue-${e.parent}`)
      const b = c.boxes.get(`issue-${e.child}`)
      if (a && b) segs.push({ cls: `line tree ${e.via}`, d: linkRoute(a, b) })
    }
  }
  return combineByClass(segs)
}

/** A line drawn on its own: the foreground layer, and the dependencies (their arrow heads need their own paths). */
export interface Single { id: string; cls: string; d: string; marker?: string }

function allDepEnds(c: LayerInput): { a: Box; b: Box }[] {
  const out: { a: Box; b: Box }[] = []
  for (const d of c.deps) {
    const a = c.boxes.get(`issue-${d.blocker}`)
    const b = c.boxes.get(`issue-${d.blocked}`)
    if (a && b) out.push({ a, b })
  }
  return out
}

/**
 * The foreground layer: the lines of the focused item (its links, its tree edges and dependencies), drawn over the
 * background. The dependencies are included here whole: their faint or strong state is the focus, and each carries an
 * arrow head. Only the focused item's lines change when the pointer moves.
 */
export function foregroundSingles(c: LayerInput, focus: Item | null): Single[] {
  const out: Single[] = []
  const issueFocus = focus?.kind === "issue" ? focus : null
  const involved = (d: DepEdge) => !!issueFocus && (issueFocus.key === d.blocker || issueFocus.key === d.blocked)

  // the bus of the dependencies, when not all of them are drawn: then it runs along the focused item's only
  if (!c.showDeps) {
    const ends = c.deps.filter(involved).flatMap((d) => {
      const a = c.boxes.get(`issue-${d.blocker}`)
      const b = c.boxes.get(`issue-${d.blocked}`)
      return a && b ? [{ a, b }] : []
    })
    const bus = busOf(ends)
    if (bus) out.push({ id: "bus", cls: "line bus", d: bus.d })
  }

  if (focus) {
    for (const l of c.links) {
      const mine = focus.kind === "issue" ? focus.key === l.issue : focus.key === l.pull
      if (!mine) continue
      const a = c.boxes.get(`issue-${l.issue}`)
      const b = c.boxes.get(`pull-${l.pull}`)
      if (a && b) out.push({ id: `link-${l.issue}-${l.pull}`, cls: `line link ${l.via}`, d: linkRoute(a, b) })
    }
  }

  if (issueFocus && c.treeLines && !c.showTree) {
    for (const e of c.tree) {
      if (issueFocus.key !== e.parent && issueFocus.key !== e.child) continue
      const a = c.boxes.get(`issue-${e.parent}`)
      const b = c.boxes.get(`issue-${e.child}`)
      if (a && b) out.push({ id: `tree-${e.parent}-${e.child}`, cls: `line tree ${e.via}`, d: linkRoute(a, b) })
    }
  }

  // with every dependency drawn, the bus was in the background from all of them; here it is only the drawn ones
  const drawn = c.deps.filter((d) => c.showDeps || involved(d))
  const busX = busOf(
    drawn.flatMap((d) => {
      const a = c.boxes.get(`issue-${d.blocker}`)
      const b = c.boxes.get(`issue-${d.blocked}`)
      return a && b ? [{ a, b }] : []
    }),
  )?.x ?? 0
  for (const d of c.deps) {
    const isInvolved = involved(d)
    if (!c.showDeps && !isInvolved) continue
    const a = c.boxes.get(`issue-${d.blocker}`)
    const b = c.boxes.get(`issue-${d.blocked}`)
    if (!a || !b) continue
    const resolved = c.items.get(`issue-${d.blocker}`)?.state === "CLOSED"
    const faint = !isInvolved
    out.push({
      id: `dep-${d.blocker}-${d.blocked}`,
      cls: `line dep ${resolved ? "resolved" : ""}${faint ? " faint" : ""}`,
      d: depRoute(a, b, busX),
      marker: `url(#${resolved ? "arrow-grey" : "arrow-red"}${faint ? "-soft" : ""})`,
    })
  }
  return out
}

/** The per-bar data that comes from the links and the states: computed once per data change, not per render. */
export function barDerived(i: Item, idx: LinkIndex) {
  const kids = i.kind === "issue" ? (idx.childrenOf.get(i.key) ?? []) : []
  const open = i.state === "OPEN"
  return {
    epic: kids.length > 0,
    kids: kids.length,
    kidsClosed: kids.filter((k) => k.state === "CLOSED").length,
    blockedNow: i.kind === "issue" && open && openBlockers(i.key, idx).length > 0,
    blocksNow: i.kind === "issue" && open ? openBlocked(i.key, idx).length : 0,
  }
}

/** Everything one bar draws from, as values: no focus in it, so the bar's memo only changes with the data. */
export interface BarModel {
  key: string
  item: Item
  x1: number
  y: number
  w: number
  color: string
  open: boolean
  epic: boolean
  summary: boolean // an epic whose children are drawn as a bracket (the tree layout)
  kids: number
  kidsClosed: number
  blockedNow: boolean
  blocksNow: number
  ciState: string | null // a PR's last CI state, for its rail
}

export function barModel(i: Item, b: Box, color: string, idx: LinkIndex, hasExtent: boolean, ciState: string | null): BarModel {
  const d = barDerived(i, idx)
  return {
    key: `${i.kind}-${i.key}`,
    item: i,
    x1: b.x1,
    y: b.y,
    w: b.x2 - b.x1,
    color,
    open: i.state === "OPEN",
    ...d,
    summary: d.epic && hasExtent,
    ciState,
  }
}
