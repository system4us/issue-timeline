// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, test } from "vitest"
import { createRoot, type Root } from "react-dom/client"
import { act } from "react"
import { Analysis } from "./Analysis"
import { makeFixture } from "./timelineFixture"
import { EMPTY_FILTERS } from "./filters"
import { CATALOG, DEFAULT_IDS } from "./widgets"
import type { Issue, Pull } from "./types"

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const mounted: { host: HTMLElement; root: Root }[] = []
afterEach(() => { for (const { host, root } of mounted.splice(0)) { act(() => root.unmount()); host.remove() } })
beforeEach(() => { localStorage.clear(); Element.prototype.scrollIntoView ??= () => {} })

function mount(over: { issues?: Issue[]; pulls?: Pull[]; hasCode?: boolean; repo?: string; writable?: boolean } = {}) {
  const f = makeFixture()
  const issues = over.issues ?? f.issues
  const pulls = over.pulls ?? f.pulls
  const host = document.createElement("div")
  document.body.appendChild(host)
  const root = createRoot(host)
  mounted.push({ host, root })
  act(() => root.render(
    <Analysis repo={over.repo ?? "o/r"} hasCode={over.hasCode ?? true} issues={issues} pulls={pulls} links={f.links} tree={f.tree} deps={f.deps}
      all={{ issues, pulls }} filters={EMPTY_FILTERS} onFilters={() => {}} onSelect={() => {}} writable={over.writable} />,
  ))
  return host
}

const widgetIds = (host: HTMLElement) => [...host.querySelectorAll<HTMLElement>("[data-widget]")].map((e) => e.dataset.widget)
const button = (host: HTMLElement, text: string) => [...host.querySelectorAll("button")].find((b) => b.textContent === text)!
const key = (el: Element, k: string, shiftKey = false) => act(() => { el.dispatchEvent(new KeyboardEvent("keydown", { key: k, shiftKey, bubbles: true })) })

describe("Analysis dashboard", () => {
  test("starts with the default layout, minus what needs CI runs this fixture has not", () => {
    const host = mount()
    expect(widgetIds(host)).toEqual(DEFAULT_IDS.filter((id) => id !== "workflows"))
  })

  test("every widget of the catalog renders on the fixture", () => {
    localStorage.setItem("issue-timeline-dashboard:o/r", JSON.stringify({ v: 1, widgets: CATALOG.map((w) => ({ id: w.id, w: w.w })) }))
    const host = mount()
    expect(widgetIds(host).length).toBe(CATALOG.length - 1) // workflows: no CI here
    expect(host.querySelectorAll("svg.chart").length).toBeGreaterThan(5)
  })

  test("a tracker without code hides the PR widgets and starts from the Trackers preset", () => {
    const host = mount({ hasCode: false, pulls: [] })
    const ids = widgetIds(host)
    expect(ids).toContain("cfd")
    expect(ids.some((id) => CATALOG.find((w) => w.id === id)!.needs)).toBe(false)
  })

  test("the keyboard moves, resizes and removes a widget, and the layout persists", () => {
    const host = mount()
    act(() => button(host, "Customize").click())
    const grip = host.querySelector('[data-grip="kpi-open"]')!
    key(grip, "ArrowRight")
    expect(widgetIds(host).slice(0, 2)).toEqual(["kpi-flow30", "kpi-open"])
    key(host.querySelector('[data-grip="kpi-open"]')!, "ArrowRight", true)
    const saved = JSON.parse(localStorage.getItem("issue-timeline-dashboard:o/r")!)
    expect(saved.widgets[1]).toEqual({ id: "kpi-open", w: 3 })
    key(host.querySelector('[data-grip="kpi-open"]')!, "Delete")
    expect(widgetIds(host)).not.toContain("kpi-open")
    expect(document.activeElement?.getAttribute("data-grip")).toBe(widgetIds(host)[1])

    // a new mount of the same repo reads it back; another repo keeps its own default
    const again = mount()
    expect(widgetIds(again)).not.toContain("kpi-open")
    expect(widgetIds(mount({ repo: "o/other" }))).toContain("kpi-open")
  })

  test("reset goes back to the default and forgets the saved layout", () => {
    const host = mount()
    act(() => button(host, "Customize").click())
    key(host.querySelector('[data-grip="labels"]')!, "Delete")
    expect(localStorage.getItem("issue-timeline-dashboard:o/r")).not.toBeNull()
    act(() => button(host, "Reset").click())
    expect(widgetIds(host)).toContain("labels")
    expect(localStorage.getItem("issue-timeline-dashboard:o/r")).toBeNull()
  })

  test("a widget's options persist with it (the breakdown's group)", () => {
    const host = mount()
    const sel = host.querySelector<HTMLSelectElement>('[data-widget="breakdown"] select')!
    act(() => { sel.value = "autor"; sel.dispatchEvent(new Event("change", { bubbles: true })) })
    const saved = JSON.parse(localStorage.getItem("issue-timeline-dashboard:o/r")!)
    expect(saved.widgets.find((w: { id: string }) => w.id === "breakdown").opts).toEqual({ groupBy: "autor" })
  })

  test("a click on a chart bar opens the detailed list", () => {
    const host = mount()
    const bar = host.querySelector('[data-widget="close-hist"] rect.hit[role="button"]')!
    act(() => { bar.dispatchEvent(new MouseEvent("click", { bubbles: true })) })
    expect(host.querySelector(".pick-panel h3")?.textContent).toMatch(/^Issues closed in: 0 to 1 days/)
  })

  test("renders the whole catalog over ~5,000 issues in reasonable time", () => {
    const f = makeFixture()
    const issues: Issue[] = []
    for (let copy = 0; copy < 19; copy++) for (const i of f.issues) issues.push({ ...i, id: `${i.id}-${copy}`, key: `${i.key}${copy ? `-${copy}` : ""}` })
    expect(issues.length).toBeGreaterThan(5000)
    localStorage.setItem("issue-timeline-dashboard:o/r", JSON.stringify({ v: 1, widgets: CATALOG.map((w) => ({ id: w.id, w: w.w })) }))
    const t0 = performance.now()
    mount({ issues })
    const ms = performance.now() - t0
    // jsdom is far slower than a browser; this guards against an accidental quadratic, not a frame budget
    expect(ms).toBeLessThan(4000)
  })
})

describe("the list a widget opens", () => {
  const openGaps = (writable: boolean) => {
    localStorage.setItem("issue-timeline-dashboard:o/r", JSON.stringify({ v: 1, widgets: [{ id: "data-quality", w: 4 }] }))
    const host = mount({ writable })
    act(() => button(host, "list them").click())
    return host
  }

  test("a read-only repo has no checkboxes to edit with", () => {
    const host = openGaps(false)
    expect(host.querySelector(".pick-panel")).toBeTruthy()
    expect(host.querySelectorAll('.pick-panel input[type="checkbox"]').length).toBe(0)
  })

  test("a repo in write mode ticks issues, and the bulk bar appears with how many", () => {
    const host = openGaps(true)
    const boxes = [...host.querySelectorAll<HTMLInputElement>('.pick-panel input[aria-label^="Select "]')].filter((b) => !b.getAttribute("aria-label")!.startsWith("Select all"))
    expect(boxes.length).toBeGreaterThan(1)
    expect(host.querySelector(".bulk-bar")).toBeNull()
    act(() => boxes[0]!.click())
    expect(host.querySelector(".bulk-bar")?.textContent).toContain("1 selected")
    const all = host.querySelector<HTMLInputElement>('input[aria-label="Select all the issues in the list"]')!
    act(() => all.click())
    expect(host.querySelector(".bulk-bar")?.textContent).toContain(`${boxes.length} selected`)
    act(() => all.click())
    expect(host.querySelector(".bulk-bar")).toBeNull()
  })
})

