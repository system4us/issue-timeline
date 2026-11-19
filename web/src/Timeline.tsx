import { memo, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react"
import type { DepEdge, Issue, Item, Link, Pull, TreeEdge } from "./types"
import { packLanes } from "./lanes"
import type { Scale } from "./timeScale"
import { groupOf, treeOrder, type TimelineGroup } from "./grouping"
import { NONE, type Filters, type LinkIndex } from "./filters"
import { ContextMenu, type MenuEntry } from "./ContextMenu"
import { BAR, backgroundGroups, barModel, foregroundSingles, type BarModel, type Box, type LayerInput } from "./timelineLayers"

export type ColorBy = "estado" | "etiqueta" | "autor"

const ROW = 18
const MIN_BAR = 16 // an item that lived for minutes is still a bar you can see and click
const MIN_ISSUE_BAR = 56 // an issue closed in seconds still has room for its "#number"
const MIN_PR_BAR = 64 // a PR merged in seconds still has room for its "#number" (its extent in time is exact, its bar is not)
const MIN_EPIC = 78 // a bar with children keeps room for its counter
const OPEN_TAIL = 150 // an item still open fades out after this many px instead of running to "hoy"
const HEAD = 34
const BAND_GAP = 56 // room between blocks of rows: a family stands apart from the next one

const hash = (s: string) => [...s].reduce((h, c) => (h * 31 + c.charCodeAt(0)) >>> 0, 7)
const authorColor = (a: string | null) => `hsl(${hash(a ?? "?") % 360} 55% 52%)`

function colorOf(item: Item, by: ColorBy): string {
  if (by === "autor") return authorColor(item.author)
  if (by === "etiqueta") return item.labels[0] ? `#${item.labels[0].color}` : "var(--muted-bar)"
  if (item.kind === "issue") return item.state === "OPEN" ? "var(--open)" : "var(--closed)"
  return item.state === "MERGED" ? "var(--merged)" : item.state === "OPEN" ? "var(--open)" : "var(--rejected)"
}

export const endOf = (item: Item, now: number) => {
  if (item.kind === "pull") return Date.parse(item.mergedAt ?? item.closedAt ?? "") || now
  return Date.parse(item.closedAt ?? "") || now
}

import { CI_LABEL, type CiChange, type CiRun, type CiSummary } from "./CiBadge"
import { refOf } from "./keys"
import { byKey } from "./keys"

interface Props {
  ci?: CiSummary["byPr"]
  ciChanges?: CiChange[]
  issues: Issue[]
  pulls: Pull[]
  links: Link[]
  scale: Scale
  now: number
  colorBy: ColorBy
  showAllLinks: boolean
  openToNow: boolean
  deps: DepEdge[]
  tree: TreeEdge[]
  showDeps: boolean
  showTree: boolean
  groupBy: TimelineGroup
  prefer: string[]
  focusKey: string
  idx: LinkIndex
  selected: Item | null
  onSelect: (item: Item | null) => void
  onFilters?: (p: Partial<Filters>) => void
  onExpand?: (item: Item) => void
}

/** The shortest a bar may be on screen: PRs enough for their label, epics for their children, the rest a few px. */
function minWidthOf(i: Item, idx: { childrenOf: Map<string, unknown[]> }) {
  if (i.kind === "pull") return MIN_PR_BAR
  if (idx.childrenOf.has(i.key)) return MIN_EPIC
  return MIN_ISSUE_BAR
}

/** The next paint, where there is one; a timer otherwise. */
const nextFrame = (cb: () => void): number => (typeof requestAnimationFrame === "function" ? requestAnimationFrame(cb) : window.setTimeout(cb, 16))

interface BarProps {
  m: BarModel
  rel: string | undefined // how the focus relates to this bar: "closes", "touches", "child", "parent", "blocker", "blocked"
  sel: boolean // the focus itself (hovered, or selected)
  dim: boolean // there is a focus and this bar is not part of it
  opened: boolean // its detail is open
  onEnter: (i: Item, e: React.MouseEvent) => void
  onLeave: () => void
  onMove: (e: React.MouseEvent) => void
  onClick: (i: Item, opened: boolean) => void
  onMenu: (i: Item, e: React.MouseEvent) => void
}

/**
 * One item's bar and its CI rail. Memoized on its model and on the few props the focus changes, so the focus re-renders
 * the bars it touches, not all of them.
 */
const Bar = memo(function Bar({ m, rel, sel, dim, opened, onEnter, onLeave, onMove, onClick, onMenu }: BarProps) {
  const i = m.item
  const w = m.w
  const relClass = rel === "closes" ? "closes" : rel === "touches" ? "linked" : rel ? `rel-${rel}` : ""
  return (
    <>
      <div
        className={`bar ${i.kind} ${m.open ? "ongoing" : ""} ${sel ? "selected" : ""} ${relClass} ${dim ? "dim" : ""} ${m.epic ? "epic" : ""} ${m.summary ? "summary" : ""} ${m.blockedNow ? "is-blocked" : ""}`}
        style={{ left: m.x1, top: m.y, width: w, height: BAR, background: m.color }}
        onMouseEnter={(e) => onEnter(i, e)}
        onMouseMove={onMove}
        onMouseLeave={onLeave}
        onClick={(e) => { e.stopPropagation(); onClick(i, opened) }}
        onContextMenu={(e) => { e.preventDefault(); e.stopPropagation(); onMenu(i, e) }}
      >
        {i.kind === "pull" && i.state === "MERGED" && <span className="merge-mark" />}
        {m.epic && <span className="kids-progress" style={{ width: `${(m.kidsClosed / m.kids) * 100}%` }} />}
        {w > 34 && (
          <span className="bar-label">
            {refOf(i.key)}
            {m.epic && ` ▸${m.kidsClosed}/${m.kids}`}
            {m.blockedNow && " ⛔"}
            {m.blocksNow > 0 && ` 🔒${m.blocksNow}`}
            {m.summary && w > 260 && <span className="bar-title"> · {i.title}</span>}
            {/* an issue's name follows its number; the bar cuts it to its width, and the hover has it whole */}
            {i.kind === "issue" && !m.epic && <span className="bar-title"> · {i.title}</span>}
          </span>
        )}
      </div>
      {/* CI state of a PR: a thin rail under its bar. Informational only (no clicks), so it cannot be hit by mistake. */}
      {m.ciState && (
        <div className={`ci-rail ci-${m.ciState}`} title={`CI ${CI_LABEL[m.ciState] ?? m.ciState} · open the PR's detail for the run`} style={{ left: m.x1, top: m.y + BAR + 2, width: w }} />
      )}
    </>
  )
})

export function Timeline({ issues, pulls, links, scale, now, colorBy, showAllLinks, openToNow, deps, tree, showDeps, showTree, groupBy, prefer, focusKey, idx, selected, onSelect, onFilters, onExpand, ci, ciChanges = [] }: Props) {
  const scroller = useRef<HTMLDivElement>(null)
  const dockHeadRef = useRef<HTMLDivElement>(null)
  // The hovered item changes only when the pointer enters or leaves a bar. The tooltip's place is not state: it
  // follows the pointer through a ref, written to the element once per frame.
  const [hover, setHover] = useState<Item | null>(null)
  const pointer = useRef({ x: 0, y: 0 })
  const tipRef = useRef<HTMLDivElement>(null)
  const frame = useRef(0)
  // The right-click menu of a bar: what is done with that item, at the pointer.
  const [menu, setMenu] = useState<{ item: Item; x: number; y: number } | null>(null)
  const closeMenu = useCallback(() => setMenu(null), [])

  // Beside the pointer and kept on screen: the tooltip measures itself and turns to the left of the pointer (or above
  // it) when there is no room to the right (or below), so it never covers what is under the pointer.
  const placeTip = useCallback(() => {
    const el = tipRef.current
    if (!el) return
    const { x, y } = pointer.current
    const gap = 14
    const margin = 8
    const w = el.offsetWidth
    const h = el.offsetHeight
    const vw = window.innerWidth
    const vh = window.innerHeight
    let left = x + gap
    if (left + w > vw - margin) left = x - gap - w
    left = Math.max(margin, Math.min(left, vw - w - margin))
    let top = y + gap
    if (top + h > vh - margin) top = y - gap - h
    top = Math.max(margin, Math.min(top, vh - h - margin))
    el.style.left = `${left}px`
    el.style.top = `${top}px`
  }, [])
  const moveBar = useCallback((e: React.MouseEvent) => {
    pointer.current = { x: e.clientX, y: e.clientY }
    if (!frame.current) frame.current = nextFrame(() => { frame.current = 0; placeTip() })
  }, [placeTip])
  const enterBar = useCallback((i: Item, e: React.MouseEvent) => {
    pointer.current = { x: e.clientX, y: e.clientY }
    setHover(i)
  }, [])
  const leaveBar = useCallback(() => setHover(null), [])
  const menuBar = useCallback((i: Item, e: React.MouseEvent) => setMenu({ item: i, x: e.clientX, y: e.clientY }), [])
  // The bars' handlers are stable, so a bar whose props did not change is not rendered again.
  const latest = useRef({ onSelect })
  latest.current = { onSelect }
  const clickBar = useCallback((i: Item, opened: boolean) => latest.current.onSelect(opened ? null : i), [])
  // placed after each render that shows the tooltip or hides it for the menu
  useLayoutEffect(() => { placeTip() }, [hover, menu, placeTip])
  const { x, width } = scale
  // The CI change lines, in order along the axis, and the one the "jump" button went to last.
  const ciXs = useMemo(() => ciChanges.map((c) => (c.at ? x(Date.parse(c.at)) : NaN)).filter((v) => Number.isFinite(v)).sort((a, b) => a - b), [ciChanges, x])
  const [ciJump, setCiJump] = useState(-1)
  const jumpToCiChange = () => {
    if (!ciXs.length || !scroller.current) return
    const next = (ciJump + 1) % ciXs.length
    setCiJump(next)
    scroller.current.scrollTo({ left: Math.max(0, ciXs[next] - 120), behavior: "smooth" })
  }
  // The tree layout (a parent on top, its children staggered under it) is the layout of "Agrupar por épica" and of the
  // hierarchy toggle: it needs room, so each family gets its own block of rows instead of being packed in with the rest.
  const treeMode = groupBy === "epica" || (groupBy === "ninguno" && showTree)

  // With the tree layout a parent is a summary bar: it spans from where it starts to where its last descendant ends,
  // so it sits on top of its children like a bracket. Without it a parent is just its own lifespan.
  const extents = useMemo(() => {
    const m = new Map<string, { x1: number; x2: number }>()
    if (!treeMode) return m
    const visible = new Map(issues.map((i) => [i.key, i]))
    const own = (i: Issue) => {
      const x1 = x(Date.parse(i.createdAt))
      return { x1, x2: i.state === "OPEN" && !openToNow ? x1 + OPEN_TAIL : Math.max(x(endOf(i, now)), x1 + MIN_BAR) }
    }
    const visit = (n: string, seen: Set<string>): { x1: number; x2: number } => {
      let { x1, x2 } = own(visible.get(n)!)
      for (const k of idx.childrenOf.get(n) ?? []) {
        if (!visible.has(k.key) || seen.has(k.key)) continue
        seen.add(k.key)
        const e = visit(k.key, seen)
        x1 = Math.min(x1, e.x1)
        x2 = Math.max(x2, e.x2)
      }
      return { x1, x2 }
    }
    for (const i of issues) if ((idx.childrenOf.get(i.key) ?? []).some((k) => visible.has(k.key))) m.set(i.key, visit(i.key, new Set([i.key])))
    return m
  }, [issues, treeMode, x, now, openToNow, idx])

  // Sections of rows. Ungrouped: the issues, then the pull requests. Grouped (by milestone, label or author): one
  // section per group with both kinds in it, the biggest group first.
  const layout = useMemo(() => {
    const span = (i: Item) => {
      const ext = i.kind === "issue" ? extents.get(i.key) : undefined
      const x1 = ext ? ext.x1 : x(Date.parse(i.createdAt))
      const min = minWidthOf(i, idx)
      const x2 = ext ? Math.max(ext.x2, x1 + min) : i.state === "OPEN" && !openToNow ? x1 + Math.max(OPEN_TAIL, min) : Math.max(x(endOf(i, now)), x1 + min)
      return { id: `${i.kind}-${i.key}`, start: x1, end: x2 }
    }
    let defs: { title: string; items: Item[]; tree?: boolean; kind?: "issue" | "pull" }[]
    if (groupBy === "ninguno" && showTree) {
      // one block per family (the earliest first, so it reads along the timeline), then the issues with no family, then the pull requests
      const fam = new Map<string, Item[]>()
      for (const i of issues) {
        const k = groupOf(i, "epica", idx, [])
        ;(fam.get(k) ?? fam.set(k, []).get(k)!).push(i)
      }
      const first = (items: Item[]) => Math.min(...items.map((i) => Date.parse(i.createdAt)))
      const trees = [...fam].filter(([k]) => k !== NONE).sort((a, b) => first(a[1]) - first(b[1]))
      defs = [
        ...trees.map(([k, items]) => ({ title: `${k} · ${items.length} issues`, items, tree: true, kind: "issue" as const })),
        ...(fam.has(NONE) ? [{ title: `Issues without epic (${fam.get(NONE)!.length})`, items: fam.get(NONE)!, kind: "issue" as const }] : []),
        { title: `Pull requests (${pulls.length})`, items: pulls, kind: "pull" as const },
      ]
    } else if (groupBy === "ninguno") {
      // A section with nothing in it (a repo without issues, say) is not drawn: it would only be an empty band.
      defs = [
        ...(issues.length ? [{ title: `Issues (${issues.length})`, items: issues, kind: "issue" as const }] : []),
        ...(pulls.length ? [{ title: `Pull requests (${pulls.length})`, items: pulls, kind: "pull" as const }] : []),
      ]
    } else {
      const map = new Map<string, Item[]>()
      for (const i of [...issues, ...pulls]) {
        const k = groupOf(i, groupBy, idx, prefer)
        ;(map.get(k) ?? map.set(k, []).get(k)!).push(i)
      }
      defs = [...map].sort((a, b) => b[1].length - a[1].length).map(([k, items]) => ({
        title: `${k} · ${items.filter((i) => i.kind === "issue").length} issues, ${items.filter((i) => i.kind === "pull").length} PRs`,
        items,
        tree: groupBy === "epica" && k !== NONE, // one row per issue: the parent on top, its children staggered below
      }))
    }
    const lane = new Map<string, { top: number; lane: number }>()
    const sections: { title: string; top: number; kind?: "issue" | "pull" }[] = []
    const elbows: { parent: string; child: string }[] = []
    let top = HEAD
    for (const d of defs) {
      sections.push({ title: d.title, top, kind: d.kind })
      if (d.tree) {
        // the parent first, then each child on its own row in the order they were opened (a staircase), the
        // children of a child right under it; the pull requests packed in a strip under the issues
        const { order, parentOf } = treeOrder(d.items.filter((i): i is Issue => i.kind === "issue"), idx)
        order.forEach((i, r) => lane.set(`issue-${i.key}`, { top, lane: r }))
        for (const [c, p] of parentOf) elbows.push({ parent: `issue-${p}`, child: `issue-${c}` })
        const prs = d.items.filter((i) => i.kind === "pull")
        const packed = packLanes(prs.map(span))
        const prStart = order.length + (prs.length ? 0.7 : 0)
        for (const [id, l] of packed.lane) lane.set(id, { top, lane: prStart + l })
        top += (prStart + packed.lanes) * ROW + BAND_GAP
      } else {
        const packed = packLanes(d.items.map(span))
        for (const [id, l] of packed.lane) lane.set(id, { top, lane: l })
        top += Math.max(1, packed.lanes) * ROW + BAND_GAP
      }
    }
    // where to bring the view when the layout changes: the first block, not the oldest item of everything
    const firstBlock = defs.find((d) => d.items.length > 0)
    const focusX = firstBlock ? Math.min(...firstBlock.items.map((i) => span(i).start)) : 0
    return { lane, sections, elbows, focusX, height: top + 20 }
  }, [issues, pulls, x, now, openToNow, groupBy, showTree, idx, prefer, extents])

  // Where every bar is, computed once per layout: the lines and the bars read it instead of working it out again.
  const boxes = useMemo(() => {
    const m = new Map<string, Box>()
    for (const i of [...issues, ...pulls]) {
      const l = layout.lane.get(`${i.kind}-${i.key}`) ?? { top: HEAD, lane: 0 }
      const ext = i.kind === "issue" ? extents.get(i.key) : undefined
      const x1 = ext ? ext.x1 : x(Date.parse(i.createdAt))
      const min = minWidthOf(i, idx)
      const x2 = ext ? Math.max(ext.x2, x1 + min) : i.state === "OPEN" && !openToNow ? x1 + Math.max(OPEN_TAIL, min) : Math.max(x(endOf(i, now)), x1 + min)
      m.set(`${i.kind}-${i.key}`, { x1, x2, y: l.top + l.lane * ROW })
    }
    return m
  }, [issues, pulls, layout, extents, x, now, openToNow, idx])

  // Start at the present (that is where the work is) and zoom around it: the distance from the right edge, as a
  // fraction of the whole axis, is what stays put when the width changes.
  const lastWidth = useRef(0)
  useEffect(() => {
    const el = scroller.current
    if (!el || width === lastWidth.current) return
    const fromRight = lastWidth.current ? (lastWidth.current - el.scrollLeft - el.clientWidth) / lastWidth.current : 0
    el.scrollLeft = Math.max(0, width - el.clientWidth - fromRight * width)
    lastWidth.current = width
  }, [width])

  // When the filters or the grouping change, bring what is left into view: the filtered items can be anywhere on the axis.
  const lastFocus = useRef(focusKey)
  useEffect(() => {
    const el = scroller.current
    if (!el || lastFocus.current === focusKey) return
    lastFocus.current = focusKey
    const xs = [...issues, ...pulls].map((i) => x(Date.parse(i.createdAt)))
    if (xs.length) el.scrollTo({ left: Math.max(0, (treeMode ? layout.focusX : Math.min(...xs)) - 60), top: 0 })
  }, [focusKey, issues, pulls, x, treeMode, layout.focusX])

  const drag = useRef<{ x: number; left: number } | null>(null)

  const all = useMemo(() => [...issues, ...pulls], [issues, pulls])
  const byKey = useMemo(() => new Map(all.map((i) => [`${i.kind}-${i.key}`, i])), [all])
  // What the hovered item (or, failing that, the selected one) is tied to: for a pull request, the issues it closes
  // and the ones it only touches; for an issue, the pull requests that close it or touch it.
  const focus = hover ?? selected
  type Rel = "closes" | "touches" | "child" | "parent" | "blocker" | "blocked"
  const relation = useMemo(() => {
    const m = new Map<string, Rel>()
    if (!focus) return m
    const set = (key: string, r: Rel) => { if (!m.has(key) || r === "closes") m.set(key, r) }
    for (const l of links) {
      const mine = focus.kind === "issue" ? focus.key === l.issue : focus.key === l.pull
      if (mine) set(focus.kind === "issue" ? `pull-${l.pull}` : `issue-${l.issue}`, l.via === "closes" ? "closes" : "touches")
    }
    if (focus.kind === "issue") {
      for (const e of tree) {
        if (e.parent === focus.key) set(`issue-${e.child}`, "child")
        if (e.child === focus.key) set(`issue-${e.parent}`, "parent")
      }
      for (const d of deps) {
        if (d.blocker === focus.key) set(`issue-${d.blocked}`, "blocked")
        if (d.blocked === focus.key) set(`issue-${d.blocker}`, "blocker")
      }
    }
    return m
  }, [focus, links, tree, deps])

  // The two line layers. The background is every link, tree edge and dependency the toggles draw (faint where it is not
  // the focus's): it depends on the data and the layout only, so it is built once and the pointer never touches it.
  // The foreground is the focused item's lines, the only part that changes with the focus.
  const lineBase = useMemo<LayerInput>(
    () => ({ boxes, items: byKey, elbows: layout.elbows, links, tree, deps, showAllLinks, showTree, treeLines: !treeMode, showDeps }),
    [boxes, byKey, layout.elbows, links, tree, deps, showAllLinks, showTree, treeMode, showDeps],
  )
  const background = useMemo(() => backgroundGroups(lineBase), [lineBase])
  const foreground = useMemo(() => foregroundSingles(lineBase, focus), [lineBase, focus])
  const backgroundSvg = useMemo(
    () => <g className="lines-bg">{background.map((g) => <path key={g.cls} d={g.d} className={g.cls} />)}</g>,
    [background],
  )

  // The bars, each with what it derives from the links and the states: a model per item, built per data or layout change.
  const models = useMemo(() => {
    const mk = (items: Item[]) =>
      items.map((i) => barModel(i, boxes.get(`${i.kind}-${i.key}`)!, colorOf(i, colorBy), idx, extents.has(i.key), i.kind === "pull" ? (ci?.[i.key]?.state ?? null) : null))
    return { issues: mk(issues), pulls: mk(pulls) }
  }, [issues, pulls, boxes, colorBy, idx, extents, ci])
  // A bar's props change with the focus only: its relation to it, whether it is the focus, and whether it is dimmed.
  const barRow = (ms: BarModel[]) =>
    ms.map((m) => {
      const isSel = !!focus && focus.kind === m.item.kind && focus.key === m.item.key // highlighted: hovered, or selected
      const rel = relation.get(m.key)
      return (
        <Bar
          key={m.key}
          m={m}
          rel={rel}
          sel={isSel}
          dim={!!focus && !isSel && !rel}
          opened={!!selected && selected.kind === m.item.kind && selected.key === m.item.key}
          onEnter={enterBar}
          onLeave={leaveBar}
          onMove={moveBar}
          onClick={clickBar}
          onMenu={menuBar}
        />
      )
    })

  /** What the right-click menu offers for an item: open it, copy it, or narrow the view to what it belongs to. */
  const menuEntries = (i: Item): MenuEntry[] => {
    const copy = (text: string) => { navigator.clipboard?.writeText(text).catch(() => {}) }
    const onGitLab = i.url.includes("gitlab")
    const entries: MenuEntry[] = [
      { kind: "item", label: "Open details", run: () => onSelect(i) },
      { kind: "item", label: "Open expanded (full screen)", run: () => (onExpand ? onExpand(i) : onSelect(i)) },
      { kind: "item", label: `Open on ${onGitLab ? "GitLab" : "GitHub"}`, href: i.url },
      { kind: "item", label: `Copy ${refOf(i.key)}`, run: () => copy(`${refOf(i.key)}`) },
      { kind: "item", label: "Copy link", run: () => copy(i.url) },
    ]
    if (onFilters) {
      const filter = onFilters
      const kids = i.kind === "issue" ? idx.childrenOf.get(i.key) ?? [] : []
      const milestone = i.milestone
      const labels = i.labels.slice(0, 3)
      if (kids.length || milestone || labels.length) entries.push({ kind: "sep" })
      if (kids.length) entries.push({ kind: "item", label: `Show only ${refOf(i.key)} and its children`, run: () => filter({ epics: [String(i.key)] }) })
      if (milestone) entries.push({ kind: "item", label: `Filter by milestone «${milestone.title}»`, run: () => filter({ milestones: [milestone.title] }) })
      for (const l of labels) entries.push({ kind: "item", label: `Filter by label «${l.name}»`, run: () => filter({ labels: [l.name] }) })
    }
    return entries
  }

  // The section at the top of the view, kept in a bar that stays while the timeline scrolls down (issues, then PRs).
  // Pull requests, when they form their own section, are pinned to the bottom of the view in a dock with its own scroll;
  // the issues scroll above it. The canvas stops where the dock starts.
  const prSection = layout.sections.find((s) => s.kind === "pull")
  // The CI change lines cross the pull requests: they start where that section starts.
  const ciTop = prSection ? prSection.top : 0
  // Each time the CI definition (its workflow file) changed: a vertical line through the pull requests where it happened.
  const ciLines = ciChanges.map((c, k) => {
          const t = c.at ? Date.parse(c.at) : NaN
          if (!Number.isFinite(t)) return null
          const left = x(t)
          if (left < 0 || left > width) return null
          return (
            <div key={`ci-${k}`} className="ci-change" style={{ left, top: ciTop, height: layout.height - ciTop }}>
              <span className="ci-change-mark" title={`CI definition changed · ${c.title}${c.author ? ` · ${c.author}` : ""} · ${c.at?.slice(0, 10)}`} />
            </div>
          )
  })
  const dock = !!prSection && pulls.length > 0
  const dockTop = prSection ? prSection.top - 22 : 0
  // `pullBelow`: the pull requests start lower in the view, so the bar says so even while the issues are on top.
  const [view, setView] = useState({ k: 0, pullBelow: false })
  const onScrollTimeline = (e: React.UIEvent<HTMLDivElement>) => {
    const el = e.currentTarget
    // The PR dock's label stays at the left edge while the view scrolls sideways (the dock scrolls on its own, so it cannot stick).
    if (dockHeadRef.current) dockHeadRef.current.style.transform = `translateX(${el.scrollLeft}px)`
    const y = el.scrollTop + HEAD
    let k = 0
    layout.sections.forEach((s, i) => { if (s.top <= y) k = i })
    const pull = layout.sections.find((s) => s.kind === "pull")
    const pullBelow = !dock && !!pull && pull.top > y && pull.top < el.scrollTop + el.clientHeight
    setView((v) => (v.k === k && v.pullBelow === pullBelow ? v : { k, pullBelow }))
  }
  const currentSection = layout.sections[view.k] ?? layout.sections[0]

  return (
    <div
      className="timeline"
      ref={scroller}
      onScroll={onScrollTimeline}
      // The browser's own right-click menu has nothing to offer on the timeline: the bars open the app's menu instead.
      onContextMenu={(e) => e.preventDefault()}
      onPointerDown={(e) => { if ((e.target as HTMLElement).closest(".bar")) return; drag.current = { x: e.clientX, left: scroller.current!.scrollLeft } }}
      onPointerMove={(e) => { if (drag.current) scroller.current!.scrollLeft = drag.current.left - (e.clientX - drag.current.x) }}
      onPointerUp={() => (drag.current = null)}
      onPointerLeave={() => (drag.current = null)}
      onClick={() => onSelect(null)}
    >
      {currentSection && (
        <div className={`section-bar ${currentSection.kind === "pull" ? "pr" : currentSection.kind === "issue" ? "issue" : ""}`} aria-live="polite">
          {currentSection.kind && (
            <span className={`section-tag ${currentSection.kind}`}>{currentSection.kind === "pull" ? "Pull requests" : "Issues"}</span>
          )}
          {/* the tag already says "Issues" / "Pull requests": the title shows only the count after it */}
          {currentSection.kind ? currentSection.title.replace(/^(Issues|Pull requests)\s*/, "") : currentSection.title}
          {!dock && view.pullBelow && currentSection.kind !== "pull" && <span className="section-next">{prSection?.title} ↓</span>}
        </div>
      )}
      <div className="canvas" style={{ width, height: dock ? dockTop : layout.height }}>
        {scale.breaks.map((b, i) => (
          <div key={i} className="gap" style={{ left: b.x0, width: Math.max(1, b.x1 - b.x0) }}><span>{b.days} days without activity</span></div>
        ))}
        {!dock && ciLines}
        {scale.ticks.map((t, i) => (
          <div key={i} className={`tick-line ${t.major ? "major" : ""}`} style={{ left: t.x }}><span>{t.label}</span></div>
        ))}
        <div className="today" style={{ left: x(now) }}><span>today</span></div>
        {layout.sections.slice(1).filter((sec) => !(dock && sec.kind === "pull")).map((sec, k) => (
          <div key={`rule-${k}`} className="section-rule" style={{ top: sec.top - 10 }} />
        ))}
        <div className="sticky-anchor">
          {layout.sections.filter((sec) => !(dock && (sec.kind === "pull" || (sec.title.startsWith("Issues (") && sec.kind === "issue")))).map((sec, k) => (
            <div key={k} className="band-title" style={{ top: sec.top - 18 }}>{sec.title}</div>
          ))}
        </div>
        <svg className="links" width={width} height={dock ? dockTop : layout.height}>
          <defs>
            <marker id="arrow-red" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M0,0 L10,5 L0,10 z" className="arrow-red" /></marker>
            <marker id="arrow-grey" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse"><path d="M0,0 L10,5 L0,10 z" className="arrow-grey" /></marker>
            <marker id="arrow-red-soft" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse"><path d="M0,0 L10,5 L0,10 z" className="arrow-red arrow-soft" /></marker>
            <marker id="arrow-grey-soft" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse"><path d="M0,0 L10,5 L0,10 z" className="arrow-grey arrow-soft" /></marker>
          </defs>
          {backgroundSvg}
          <g className="lines-fg">
            {foreground.map((s) => <path key={s.id} d={s.d} className={s.cls} markerEnd={s.marker} />)}
          </g>
        </svg>
        {barRow(models.issues)}
        {!dock && barRow(models.pulls)}
      </div>
      {dock && (
        <div className="pr-dock" style={{ width }}>
          <div className="pr-dock-head" ref={dockHeadRef}>
            <span className="section-tag pull">Pull requests</span> <span className="muted">({pulls.length})</span>
            {ciChanges.length > 0 && (
              <button className="ci-legend-chip ci-jump" onClick={jumpToCiChange} title="Scroll to the next moment the CI definition changed (the lines through the pull requests)">
                <i className="lg lg-change" /> CI definition changed · {ciJump + 1 > ciXs.length ? 0 : ciJump + 1}/{ciXs.length} ↦
              </button>
            )}
          </div>
          <div style={{ position: "relative", height: layout.height - dockTop, width }}>
            <div style={{ position: "absolute", top: -dockTop, left: 0, width, height: layout.height }}>{barRow(models.pulls)}{ciLines}</div>
          </div>
        </div>
      )}
      {menu && <ContextMenu x={menu.x} y={menu.y} entries={menuEntries(menu.item)} onClose={closeMenu} />}
      {hover && !menu && (
        <HoverBox boxRef={tipRef}>
          <b>{hover.kind === "issue" ? "Issue" : "PR"} {refOf(hover.key)}</b> · {hover.state}
          <div>{hover.title}</div>
          <div className="muted">
            {hover.author} · {hover.createdAt.slice(0, 16).replace("T", " ")} → {(hover.kind === "pull" ? hover.mergedAt ?? hover.closedAt : hover.closedAt)?.slice(0, 16).replace("T", " ") ?? "open"}
          </div>
          {hover.kind === "pull" && <div className="muted">+{hover.additions} −{hover.deletions} · {hover.changedFiles} files · {hover.reviewsCount} reviews</div>}
          {hover.labels.length > 0 && <div className="muted">{hover.labels.map((l) => l.name).join(", ")}</div>}
          {hover.kind === "issue" && <Family item={hover} idx={idx} />}
          {/* an issue with a family keeps its pull requests to one line: the family is what it is hovered for */}
          <Related item={hover} links={links} idx={idx} visible={byKey} compact={hover.kind === "issue" && hasFamily(hover, idx)} />
          {hover.kind === "pull" && ci && <CiHover item={hover} run={ci[hover.key]} changes={ciChanges} />}
        </HoverBox>
      )}
    </div>
  )
}

/** The other side of the links of an item: "cierra" first, then "toca", each with its state and title. */
function Related({ item, links, idx, visible, compact }: { item: Item; links: Link[]; idx: LinkIndex; visible: Map<string, Item>; compact?: boolean }) {
  const rows = new Map<string, { via: "closes" | "touches" }>()
  for (const l of links) {
    const mine = item.kind === "pull" ? l.pull === item.key : l.issue === item.key
    if (!mine) continue
    const other = item.kind === "pull" ? l.issue : l.pull
    const cur = rows.get(other)
    if (!cur || l.via === "closes") rows.set(other, { via: l.via === "closes" ? "closes" : "touches" })
  }
  if (rows.size === 0) return <div className="rel none">No {item.kind === "pull" ? "issues" : "PRs"} linked</div>
  const lookup = (n: string): Item | undefined =>
    item.kind === "pull" ? idx.issuesOf.get(item.key)?.find((i) => i.key === n) : idx.pullsOf.get(item.key)?.find((p) => p.key === n)
  const sorted = [...rows].sort((a, b) => (a[1].via === b[1].via ? byKey(a[0], b[0]) : a[1].via === "closes" ? -1 : 1))
  const closes = sorted.filter(([, r]) => r.via === "closes").length
  const MAX = 8
  const noun = item.kind === "pull" ? "issues" : "PRs"
  return (
    <div className="rel">
      <div className="rel-head">
        {item.kind === "pull"
          ? `${closes} ${closes === 1 ? "closed issue" : "closed issues"} · ${sorted.length - closes} touched`
          : `${closes} ${closes === 1 ? "PR closes it" : "PRs close it"} · ${sorted.length - closes} touch it`}
      </div>
      {!compact && sorted.slice(0, MAX).map(([n, r]) => {
        const o = lookup(n)
        const hidden = o && !visible.has(`${o.kind}-${o.key}`)
        return (
          <div key={n} className={`rel-row ${r.via}`}>
            <span className="rel-tag">{r.via === "closes" ? "closes" : "touches"}</span>
            <b>#{n}</b> <span className="rel-title">{o?.title ?? ""}</span>
            {o && <span className={`pill ${o.state.toLowerCase()}`}>{o.state}</span>}
            {hidden && <span className="muted"> (hidden by filters)</span>}
          </div>
        )
      })}
      {!compact && sorted.length > MAX && <div className="muted">… and {sorted.length - MAX} more {noun}</div>}
    </div>
  )
}

const hasFamily = (i: Issue, idx: LinkIndex) =>
  !!(idx.childrenOf.get(i.key)?.length || idx.parentsOf.get(i.key)?.length || idx.blockersOf.get(i.key)?.length || idx.blockedOf.get(i.key)?.length)

const mark = (i: Issue) => (i.state === "CLOSED" ? "✓" : "○")

/** Children, parents and blockers of an issue: each with its state, the ones still holding things back first. */
function Family({ item, idx }: { item: Issue; idx: LinkIndex }) {
  const kids = idx.childrenOf.get(item.key) ?? []
  const parents = idx.parentsOf.get(item.key) ?? []
  const blockers = idx.blockersOf.get(item.key) ?? []
  const blocks = idx.blockedOf.get(item.key) ?? []
  if (!kids.length && !parents.length && !blockers.length && !blocks.length) return null
  const closed = kids.filter((k) => k.state === "CLOSED").length
  const row = (i: Issue, tag: string, cls: string) => (
    <div key={`${tag}-${i.key}`} className={`rel-row ${cls}`}>
      <span className="rel-tag">{tag}</span> <span className={i.state === "CLOSED" ? "done" : ""}>{mark(i)}</span> <b>{refOf(i.key)}</b> <span className="rel-title">{i.title}</span>
    </div>
  )
  const MAX = 8
  return (
    <>
      {kids.length > 0 && (
        <div className="rel">
          <div className="rel-head">Children: {closed}/{kids.length} closed</div>
          <div className="progress"><div style={{ width: `${(closed / kids.length) * 100}%` }} /></div>
          {[...kids].sort((a, b) => Number(a.state === "CLOSED") - Number(b.state === "CLOSED") || byKey(a.key, b.key)).slice(0, MAX).map((k) => row(k, "child", "child"))}
          {kids.length > MAX && <div className="muted">… and {kids.length - MAX} more</div>}
        </div>
      )}
      {parents.length > 0 && <div className="rel"><div className="rel-head">Child of</div>{parents.map((p) => row(p, "parent", "parent"))}</div>}
      {blockers.length > 0 && (
        <div className="rel">
          <div className="rel-head">Blocked by</div>
          {[...blockers].sort((a, b) => Number(a.state === "CLOSED") - Number(b.state === "CLOSED")).map((b) => row(b, b.state === "OPEN" ? "blocks" : "resolved", b.state === "OPEN" ? "blocker" : "resolved"))}
        </div>
      )}
      {blocks.length > 0 && <div className="rel"><div className="rel-head">Blocks</div>{blocks.slice(0, MAX).map((b) => row(b, "blocked", "blocked"))}</div>}
    </>
  )
}

/** The tooltip's box: its place is set on the element (see placeTip), so moving it does not render anything. */
function HoverBox({ boxRef, children }: { boxRef: React.RefObject<HTMLDivElement>; children: React.ReactNode }) {
  return <div ref={boxRef} className="tooltip">{children}</div>
}

/** The CI of a PR, for its hover: the state, when, how many runs, and how often its CI definition changed meanwhile. */
function CiHover({ item, run, changes }: { item: Pull; run?: CiRun; changes: CiChange[] }) {
  const start = Date.parse(item.createdAt)
  const end = item.mergedAt ?? item.closedAt ? Date.parse((item.mergedAt ?? item.closedAt) as string) : Date.now()
  const during = changes.filter((c) => c.at && Date.parse(c.at) >= start && Date.parse(c.at) <= end).length
  if (!run) return <div className="tip-ci muted"><span className="ci-dot ci-none" /> no CI run for this PR</div>
  return (
    <div className="tip-ci">
      <span className={`ci-dot ci-${run.state}`} /> CI {CI_LABEL[run.state] ?? run.state}
      <span className="muted"> · last run {run.at?.slice(0, 16).replace("T", " ")}{run.count > 1 ? ` · ${run.count} runs` : ""}{during ? ` · CI definition changed ${during}×` : ""}</span>
    </div>
  )
}
