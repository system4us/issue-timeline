import { describe, expect, test } from "vitest"
import { readViewLink, sanitizeView, viewLink } from "./views"
import type { WidgetMeta } from "./dashboard"

const catalog: WidgetMeta[] = [{ id: "kpi-open", w: 3, min: 2 }, { id: "labels", w: 4, min: 3 }]

describe("sanitizeView", () => {
  test("keeps a good state as it is", () => {
    const good = { v: 1, query: "state=open&label=bug", tab: "analisis", axis: "tiempo", colorBy: "autor", groupBy: "asignado", allLinks: true, openToNow: true, showTree: true, showDeps: true, showIssues: false, showPulls: false, dashboard: null }
    expect(sanitizeView(good, catalog)).toEqual(good)
  })
  test("takes the default for what is missing or not one of the page's values", () => {
    expect(sanitizeView({ tab: "nope", axis: 3, colorBy: null, groupBy: "<script>", allLinks: "yes", showIssues: 0 }, catalog)).toEqual({
      v: 1, query: "", tab: "timeline", axis: "actividad", colorBy: "estado", groupBy: "ninguno", allLinks: false, openToNow: false,
      showTree: false, showDeps: false, showIssues: true, showPulls: true, dashboard: null,
    })
  })
  test("is not a view when it is not an object", () => {
    for (const bad of [null, undefined, "x", 3, [1], true]) expect(sanitizeView(bad, catalog)).toBeNull()
  })
  test("cuts the query and drops a leading ?", () => {
    expect(sanitizeView({ query: "?a=1" }, catalog)!.query).toBe("a=1")
    expect(sanitizeView({ query: "x".repeat(10_000) }, catalog)!.query).toHaveLength(4000)
    expect(sanitizeView({ query: 5 }, catalog)!.query).toBe("")
  })
  test("a dashboard layout is checked against the catalog: unknown widgets are dropped, rubbish is the default", () => {
    const withLayout = sanitizeView({ dashboard: { v: 1, widgets: [{ id: "labels", w: 4 }, { id: "not-a-widget", w: 4 }] } }, catalog)!
    expect(withLayout.dashboard?.widgets.map((w) => w.id)).toEqual(["labels"])
    expect(sanitizeView({ dashboard: "rubbish" }, catalog)!.dashboard).toBeNull()
  })
})

describe("the link of a view", () => {
  test("names the repo and the view, and reads back", () => {
    const link = viewLink("https://timeline.example.com", "gitlab:https://gitlab.com/g/p", "ab12")
    expect(link).toBe("https://timeline.example.com/?repo=gitlab%3Ahttps%3A%2F%2Fgitlab.com%2Fg%2Fp&view=ab12")
    expect(readViewLink(new URL(link).search)).toEqual({ repo: "gitlab:https://gitlab.com/g/p", view: "ab12" })
  })
  test("a link that names no view is none", () => {
    expect(readViewLink("?state=open")).toBeNull()
    expect(readViewLink("?repo=a%2Fb")).toBeNull()
  })
})
