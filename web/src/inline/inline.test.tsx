// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest"
import { createRoot, type Root } from "react-dom/client"
import { act } from "react"
import * as api from "../api"
import { DetailPanel } from "../Detail"
import type { Issue } from "../types"

vi.mock("../api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api")>()),
  fetchDetail: vi.fn(),
  fetchMergeMethod: vi.fn(),
  refreshRecord: vi.fn(),
  fetchEditOptions: vi.fn(),
  fetchIssueRecord: vi.fn(),
  editIssue: vi.fn(),
  editPull: vi.fn(),
}))

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const REPO = "github:owner/repo"

const item = {
  kind: "issue", id: "i12", key: "12", number: 12, title: "Old title", url: "https://example.test/12", author: "carol",
  createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z", closedAt: null,
  labels: [{ name: "bug", color: "ff0000" }], assignees: [], milestone: null, commentsCount: 0, labelEvents: 0,
  state: "OPEN", stateReason: null,
} as Issue

const record = {
  title: "Old title", body: "Body text", state: "OPEN", labels: [{ name: "bug", color: "ff0000" }], assignees: [],
  milestone: null, parent: "", locked: false, issueType: null, stateReason: null, comments: [], blockedBy: [], blocking: [], reviewers: [],
}

const options = {
  members: [{ id: "alice", name: "alice" }, { id: "bob", name: "bob" }],
  labels: [{ name: "bug", color: "ff0000" }, { name: "ui", color: "00ff00" }],
  milestones: [], versions: [], categories: [], customFields: [], relationTypes: [], trackers: [], priorities: [],
}

const idx = { childrenOf: new Map(), parentsOf: new Map(), blockersOf: new Map(), blockedOf: new Map(), pullsOf: new Map(), issuesOf: new Map() } as any

const mounted: { host: HTMLElement; root: Root }[] = []
beforeEach(() => {
  vi.mocked(api.fetchDetail).mockResolvedValue({ ...item, body: "Body text", comments: [], events: [] } as any)
  vi.mocked(api.refreshRecord).mockResolvedValue({ changed: false })
  vi.mocked(api.fetchEditOptions).mockResolvedValue(options as any)
  vi.mocked(api.fetchIssueRecord).mockResolvedValue(structuredClone(record))
  vi.mocked(api.editIssue).mockResolvedValue({ ...record, title: "New title" })
})
afterEach(() => {
  for (const { host, root } of mounted.splice(0)) {
    act(() => root.unmount())
    host.remove()
  }
  vi.clearAllMocks()
})

const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 10)) })

async function mount(writable = true) {
  const host = document.createElement("div")
  document.body.appendChild(host)
  const root = createRoot(host)
  mounted.push({ host, root })
  await act(async () => {
    root.render(<DetailPanel item={item} repo={REPO} issues={[]} pulls={[]} links={[]} idx={idx} onSelect={() => {}} onClose={() => {}} writable={writable} />)
  })
  await flush()
  return host
}

const byLabel = (host: HTMLElement, prefix: string) =>
  [...host.querySelectorAll<HTMLElement>("[aria-label]")].find((el) => el.getAttribute("aria-label")!.startsWith(prefix))!

/** Types into a field the way a user does: the value is set, then the input event fires (React reads that). */
function typeInto(el: HTMLInputElement | HTMLTextAreaElement, value: string) {
  const proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype
  Object.getOwnPropertyDescriptor(proto, "value")!.set!.call(el, value)
  el.dispatchEvent(new Event("input", { bubbles: true }))
}

const key = (el: Element, k: string) => act(() => { el.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true })) })

describe("editing the title in place", () => {
  test("clicking the title edits it, and Enter saves with only the title", async () => {
    const host = await mount()
    await act(async () => { byLabel(host, "Title:").click() })
    const input = host.querySelector<HTMLInputElement>('input[aria-label="Title"]')!
    expect(input).not.toBeNull()
    expect(input.value).toBe("Old title")
    expect(document.activeElement).toBe(input)
    await act(async () => typeInto(input, "New title"))
    await key(input, "Enter")
    await flush()
    expect(api.editIssue).toHaveBeenCalledTimes(1)
    expect(api.editIssue).toHaveBeenCalledWith(REPO, "12", { title: "New title" })
    expect(host.querySelector(".detail-header-name")!.textContent).toBe("New title")
  })

  test("Escape restores the title and sends nothing", async () => {
    const host = await mount()
    await act(async () => { byLabel(host, "Title:").click() })
    const input = host.querySelector<HTMLInputElement>('input[aria-label="Title"]')!
    await act(async () => typeInto(input, "Changed my mind"))
    await key(input, "Escape")
    await flush()
    expect(api.editIssue).not.toHaveBeenCalled()
    expect(host.querySelector('input[aria-label="Title"]')).toBeNull()
    expect(host.querySelector(".detail-header-name")!.textContent).toBe("Old title")
  })

  test("an error shows under the title, with Retry, and the old title comes back", async () => {
    vi.mocked(api.editIssue).mockRejectedValueOnce(new Error("Host refused the change"))
    const host = await mount()
    await act(async () => { byLabel(host, "Title:").click() })
    const input = host.querySelector<HTMLInputElement>('input[aria-label="Title"]')!
    await act(async () => typeInto(input, "New title"))
    await key(input, "Enter")
    await flush()
    const alert = host.querySelector('[role="alert"]')!
    expect(alert.textContent).toContain("Host refused the change")
    expect(alert.querySelector("button")!.textContent).toBe("Retry")
    expect(host.querySelector(".detail-header-name")!.textContent).toBe("Old title")
    // Retry sends the same change again
    await act(async () => { [...alert.querySelectorAll("button")].find((b) => b.textContent === "Retry")!.click() })
    await flush()
    expect(api.editIssue).toHaveBeenCalledTimes(2)
    expect(api.editIssue).toHaveBeenLastCalledWith(REPO, "12", { title: "New title" })
  })
})

describe("the labels popover", () => {
  test("ticking a label and closing with Done saves the whole list", async () => {
    const host = await mount()
    await act(async () => { byLabel(host, "Labels:").click() })
    const dialog = host.querySelector('[role="dialog"]')!
    expect(dialog).not.toBeNull()
    const ui = [...dialog.querySelectorAll<HTMLButtonElement>('[role="checkbox"]')].find((b) => b.textContent!.includes("ui"))!
    await act(async () => ui.click())
    const done = [...dialog.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === "Done")!
    await act(async () => done.click())
    await flush()
    expect(api.editIssue).toHaveBeenCalledTimes(1)
    expect(api.editIssue).toHaveBeenCalledWith(REPO, "12", { labels: ["bug", "ui"] })
  })

  test("Escape closes the popover without a change", async () => {
    const host = await mount()
    await act(async () => { byLabel(host, "Labels:").click() })
    // three labels or fewer: no filter; Escape is taken by the popover itself
    expect(host.querySelector('input[aria-label="Filter Labels"]')).toBeNull()
    await key(host.querySelector('[role="dialog"]')!, "Escape")
    await flush()
    expect(host.querySelector('[role="dialog"]')).toBeNull()
    expect(api.editIssue).not.toHaveBeenCalled()
  })
})

describe("read-only", () => {
  test("a read-only repo shows the title as text, with no edit controls", async () => {
    const host = await mount(false)
    expect(host.querySelector(".detail-header-name")!.textContent).toBe("Old title")
    expect(host.querySelector(".inline-trigger")).toBeNull()
    expect(host.querySelector(".inline-props")).toBeNull()
    expect(host.querySelector("button.detail-edit")).toBeNull()
    expect(api.fetchIssueRecord).not.toHaveBeenCalled()
  })
})
