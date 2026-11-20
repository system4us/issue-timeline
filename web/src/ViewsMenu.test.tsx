// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest"
import { createRoot, type Root } from "react-dom/client"
import { act } from "react"
import * as api from "./api"
import { ViewsMenu } from "./ViewsMenu"
import type { ViewState } from "./views"

vi.mock("./api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./api")>()),
  fetchViews: vi.fn(),
  saveView: vi.fn(),
  deleteView: vi.fn(),
}))

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const REPO = "gitlab:https://gitlab.com/g/p"
const catalog = [{ id: "labels", w: 4 as const, min: 3 as const }]
const state = (over: Record<string, unknown> = {}) => ({ v: 1, query: "state=open", tab: "analisis", axis: "tiempo", colorBy: "estado", groupBy: "asignado", ...over })
const view = (id: string, name: string, owner: string, s: Record<string, unknown> = state()) => ({ id, name, owner, createdAt: "2026-11-20T10:00:00Z", updatedAt: "2026-11-20T10:00:00Z", state: s })

const current: ViewState = { v: 1, query: "label=bug", tab: "timeline", axis: "actividad", colorBy: "estado", groupBy: "ninguno", allLinks: false, openToNow: false, showTree: false, showDeps: false, showIssues: true, showPulls: true, dashboard: null }

const mounted: { host: HTMLElement; root: Root }[] = []
beforeEach(() => {
  vi.mocked(api.fetchViews).mockResolvedValue([view("a1", "Open by person", "ana"), view("b2", "Roadmap", "luis")])
})
afterEach(() => { for (const { host, root } of mounted.splice(0)) { act(() => root.unmount()); host.remove() } vi.clearAllMocks(); vi.restoreAllMocks() })

const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 15)) })

async function mount(props: Partial<Parameters<typeof ViewsMenu>[0]> = {}) {
  const host = document.createElement("div")
  document.body.appendChild(host)
  const root = createRoot(host)
  mounted.push({ host, root })
  const onApply = vi.fn()
  const onOpened = vi.fn()
  await act(async () => {
    root.render(<ViewsMenu repo={REPO} catalog={catalog} capture={() => current} onApply={onApply} owner="ana" canDeleteAll={false} onOpened={onOpened} {...props} />)
  })
  await flush()
  return { host, onApply, onOpened }
}

const button = (host: HTMLElement, text: string) => [...host.querySelectorAll("button")].find((b) => b.textContent?.startsWith(text))!
const click = (el: Element) => act(async () => { el.dispatchEvent(new MouseEvent("click", { bubbles: true })) })
const open = async (host: HTMLElement) => { await click(button(host, "Views")); await flush() }

describe("ViewsMenu", () => {
  test("lists the views of the repo with their owners, and the count on the button", async () => {
    const { host } = await mount()
    expect(api.fetchViews).toHaveBeenCalledWith(REPO)
    expect(button(host, "Views").textContent).toContain("(2)")
    await open(host)
    expect(host.textContent).toContain("Open by person")
    expect(host.textContent).toContain("luis")
  })

  test("opening a view applies its state, checked against what the page accepts", async () => {
    vi.mocked(api.fetchViews).mockResolvedValue([view("a1", "Odd", "ana", state({ groupBy: "<script>", tab: "nope", query: "?a=1" }))])
    const { host, onApply } = await mount()
    await open(host)
    await click(button(host, "Odd"))
    expect(onApply).toHaveBeenCalledTimes(1)
    const applied = onApply.mock.calls[0]![0] as ViewState
    expect(applied.groupBy).toBe("ninguno") // not one of the page's values: the default
    expect(applied.tab).toBe("timeline")
    expect(applied.query).toBe("a=1")
    expect(host.querySelector('[role="dialog"]')).toBeNull() // it closes
  })

  test("saving sends the name and what the page shows, and adds it to the list", async () => {
    vi.mocked(api.saveView).mockResolvedValue(view("c3", "Mine", "ana", current as unknown as Record<string, unknown>))
    const { host } = await mount()
    await open(host)
    const input = host.querySelector<HTMLInputElement>('input[aria-label="Name of the view"]')!
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "  Mine ")
      input.dispatchEvent(new Event("input", { bubbles: true }))
    })
    await click(button(host, "Save"))
    await flush()
    expect(api.saveView).toHaveBeenCalledWith(REPO, "Mine", current)
    expect(button(host, "Views").textContent).toContain("(3)")
    expect(host.textContent).toContain('Saved "Mine".')
  })

  test("a name of the person's own says it will replace it", async () => {
    const { host } = await mount()
    await open(host)
    const input = host.querySelector<HTMLInputElement>('input[aria-label="Name of the view"]')!
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "open BY person")
      input.dispatchEvent(new Event("input", { bubbles: true }))
    })
    expect(button(host, "Update")).toBeTruthy()
    expect(host.textContent).toContain("Replaces your view")
  })

  test("another person's name is refused by the server and the reason is shown", async () => {
    vi.mocked(api.saveView).mockRejectedValue(new Error('a view called "Roadmap" already exists (it is luis\'s)'))
    const { host } = await mount()
    await open(host)
    const input = host.querySelector<HTMLInputElement>('input[aria-label="Name of the view"]')!
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "Roadmap")
      input.dispatchEvent(new Event("input", { bubbles: true }))
    })
    await click(button(host, "Save"))
    await flush()
    expect(host.textContent).toContain("already exists")
    expect(button(host, "Views").textContent).toContain("(2)")
  })

  test("a person deletes their own views only; the local machine deletes any", async () => {
    const { host } = await mount()
    await open(host)
    expect(host.querySelector('[aria-label="Delete Open by person"]')).toBeTruthy() // ana's
    expect(host.querySelector('[aria-label="Delete Roadmap"]')).toBeNull() // luis's
    act(() => { mounted.pop()!.root.unmount() })
    const local = await mount({ owner: "local", canDeleteAll: true })
    await open(local.host)
    expect(local.host.querySelector('[aria-label="Delete Roadmap"]')).toBeTruthy()
  })

  test("deleting asks first, then shows what is left", async () => {
    vi.spyOn(window, "confirm").mockReturnValueOnce(false).mockReturnValueOnce(true)
    vi.mocked(api.deleteView).mockResolvedValue([view("b2", "Roadmap", "luis")])
    const { host } = await mount()
    await open(host)
    await click(host.querySelector('[aria-label="Delete Open by person"]')!)
    expect(api.deleteView).not.toHaveBeenCalled() // declined
    await click(host.querySelector('[aria-label="Delete Open by person"]')!)
    await flush()
    expect(api.deleteView).toHaveBeenCalledWith(REPO, "a1")
    expect(button(host, "Views").textContent).toContain("(1)")
  })

  test("copies the link of a view", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true })
    const { host } = await mount()
    await open(host)
    await click(host.querySelector('[aria-label="Copy the link of Roadmap"]')!)
    await flush()
    expect(writeText).toHaveBeenCalledWith(`${window.location.origin}/?repo=${encodeURIComponent(REPO)}&view=b2`)
  })

  test("a view named by the link the page opened with is applied once, and says so when it is gone", async () => {
    const found = await mount({ openId: "b2" })
    expect(found.onApply).toHaveBeenCalledTimes(1)
    expect(found.onOpened).toHaveBeenCalledTimes(1)
    const gone = await mount({ openId: "zzz" })
    expect(gone.onApply).not.toHaveBeenCalled()
    expect(gone.host.textContent).toContain("no longer exists")
    expect(gone.onOpened).toHaveBeenCalledTimes(1)
  })
})
