import { beforeEach, describe, expect, it } from "vitest"
import { add, clampSize, dropIndex, fromIds, load, move, remove, resize, sanitize, save, setOpts, stepSize, type Layout, type WidgetMeta } from "./dashboard"
import { CATALOG, DEFAULT_IDS, PRESETS } from "./widgets"

const cat: WidgetMeta[] = [{ id: "a", w: 4, min: 3 }, { id: "b", w: 6, min: 4 }, { id: "c", w: 12, min: 6 }, { id: "k", w: 2, min: 2 }]
const ids = (l: Layout) => l.widgets.map((w) => w.id)

describe("dashboard layout", () => {
  it("widths snap to the allowed ones and never go under the minimum", () => {
    expect(clampSize(5, 2)).toBe(6) // a tie between 4 and 6 goes to the wider
    expect(clampSize(1, 3)).toBe(3)
    expect(clampSize(20, 2)).toBe(12)
    expect(stepSize(4, 1, 3)).toBe(6)
    expect(stepSize(4, -1, 3)).toBe(3)
    expect(stepSize(3, -1, 3)).toBe(3)
    expect(stepSize(12, 1, 2)).toBe(12)
  })

  it("moves, resizes, removes and adds without touching the input", () => {
    const l = fromIds(["a", "b", "c"], cat)
    expect(ids(move(l, 0, 2))).toEqual(["b", "c", "a"])
    expect(ids(move(l, 2, 0))).toEqual(["c", "a", "b"])
    expect(move(l, 1, 1)).toBe(l)
    expect(resize(l, 1, 12).widgets[1]!.w).toBe(12)
    expect(ids(remove(l, 1))).toEqual(["a", "c"])
    expect(ids(add(l, cat[3]!, 0))).toEqual(["k", "a", "b", "c"])
    expect(add(l, cat[0]!)).toBe(l) // at most once
    expect(setOpts(l, 2, { groupBy: "autor" }).widgets[2]!.opts).toEqual({ groupBy: "autor" })
    expect(ids(l)).toEqual(["a", "b", "c"])
  })

  it("a drop before or after the widget under the pointer lands at the right index", () => {
    // [a b c d]: dragging a (0) after c (2) ends at 2; dragging d (3) before b (1) ends at 1
    expect(dropIndex(0, 2, true)).toBe(2)
    expect(dropIndex(0, 2, false)).toBe(1)
    expect(dropIndex(3, 1, false)).toBe(1)
    expect(dropIndex(3, 1, true)).toBe(2)
    const l = fromIds(["a", "b", "c", "k"], cat)
    expect(ids(move(l, 0, dropIndex(0, 2, true)))).toEqual(["b", "c", "a", "k"])
  })

  it("sanitizes what comes back from storage", () => {
    expect(sanitize(null, cat)).toBeNull()
    expect(sanitize({ v: 2, widgets: [] }, cat)).toBeNull()
    const l = sanitize({ v: 1, widgets: [{ id: "a", w: 1 }, { id: "gone", w: 4 }, { id: "a", w: 6 }, "junk", { id: "b", w: 7, opts: { x: "1", y: 2 } }] }, cat)!
    expect(l.widgets).toEqual([{ id: "a", w: 3 }, { id: "b", w: 8, opts: { x: "1" } }])
  })

  describe("storage", () => {
    const mem = new Map<string, string>()
    beforeEach(() => {
      mem.clear()
      ;(globalThis as { localStorage?: unknown }).localStorage = {
        getItem: (k: string) => mem.get(k) ?? null, setItem: (k: string, v: string) => void mem.set(k, v), removeItem: (k: string) => void mem.delete(k),
      }
    })
    it("saves per repo and forgets on reset", () => {
      save("o/r", fromIds(["b", "a"], cat))
      expect(ids(load("o/r", cat)!)).toEqual(["b", "a"])
      expect(load("o/other", cat)).toBeNull()
      save("o/r", null)
      expect(load("o/r", cat)).toBeNull()
    })
    it("survives unreadable storage", () => {
      mem.set("issue-timeline-dashboard:o/r", "{not json")
      expect(load("o/r", cat)).toBeNull()
    })
  })

  it("the catalog is consistent: unique ids, defaults within limits, presets and default only name real widgets", () => {
    const seen = new Set(CATALOG.map((w) => w.id))
    expect(seen.size).toBe(CATALOG.length)
    for (const w of CATALOG) expect(w.w).toBeGreaterThanOrEqual(w.min)
    for (const id of [...DEFAULT_IDS, ...PRESETS.flatMap((p) => p.ids)]) expect(seen.has(id), id).toBe(true)
    for (const p of PRESETS) expect(new Set(p.ids).size).toBe(p.ids.length)
  })
})
