// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest"
import { createRoot, type Root } from "react-dom/client"
import { act } from "react"
import * as api from "./api"
import { BulkBar } from "./BulkBar"
import type { Issue } from "./types"

vi.mock("./api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./api")>()),
  fetchEditOptions: vi.fn(),
  editIssue: vi.fn(),
  createEntity: vi.fn(),
}))

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const issue = (key: string, extra: Partial<Issue> = {}) => ({
  kind: "issue", id: `i${key}`, key, number: Number(key), title: `Issue ${key}`, url: "", author: "carol", createdAt: "2026-01-01T00:00:00Z",
  updatedAt: "2026-01-01T00:00:00Z", closedAt: null, labels: [], assignees: [], milestone: null, commentsCount: 0, labelEvents: 0,
  state: "OPEN", stateReason: null, ...extra,
}) as Issue

const options = {
  members: [{ id: "alice", name: "alice" }, { id: "bob", name: "bob" }],
  labels: [{ name: "bug", color: "ff0000" }, { name: "ui", color: "00ff00" }],
  milestones: [{ id: 3, name: "v1" }], versions: [], categories: [], customFields: [], relationTypes: [], trackers: [], priorities: [],
}

const mounted: { host: HTMLElement; root: Root }[] = []
beforeEach(() => {
  vi.mocked(api.fetchEditOptions).mockResolvedValue(options as any)
  vi.mocked(api.editIssue).mockResolvedValue({})
})
afterEach(() => {
  for (const { host, root } of mounted.splice(0)) { act(() => root.unmount()); host.remove() }
  vi.clearAllMocks()
})

const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 20)) })

async function mount(items: Issue[], repo = "github:owner/repo") {
  const host = document.createElement("div")
  document.body.appendChild(host)
  const root = createRoot(host)
  mounted.push({ host, root })
  const onApplied = vi.fn()
  await act(async () => { root.render(<BulkBar repo={repo} items={items} onApplied={onApplied} onClear={() => {}} />) })
  await flush()
  return { host, onApplied }
}

const el = <T extends HTMLElement>(host: HTMLElement, label: string) => host.querySelector<T>(`[aria-label="${label}"]`)!
const button = (host: HTMLElement, text: string) => [...host.querySelectorAll("button")].find((b) => b.textContent?.startsWith(text))!

async function choose(select: HTMLSelectElement, value: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value")!.set!.call(select, value)
    select.dispatchEvent(new Event("change", { bubbles: true }))
  })
}
const click = (b: Element) => act(async () => { b.dispatchEvent(new MouseEvent("click", { bubbles: true })) })

describe("BulkBar", () => {
  test("adds a label to the issues that lack it, after showing how many would change and asking to confirm", async () => {
    const { host, onApplied } = await mount([issue("1"), issue("2", { labels: [{ name: "ui", color: "00ff00" }] }), issue("3", { labels: [{ name: "bug", color: "ff0000" }] })])
    expect(host.textContent).toContain("3 selected")
    expect(button(host, "Apply…").disabled).toBe(true) // nothing chosen yet
    await choose(el<HTMLSelectElement>(host, "What to change"), "addLabel")
    await choose(el<HTMLSelectElement>(host, "Value"), "ui")
    await click(button(host, "Apply…"))
    expect(host.textContent).toContain("2 of 3 would change") // issue 2 already has it
    expect(api.editIssue).not.toHaveBeenCalled() // nothing sent before the confirmation
    await click(button(host, "Confirm"))
    await flush()
    const sent = vi.mocked(api.editIssue).mock.calls.map(([, key, body]) => [key, body]).sort()
    expect(sent).toEqual([["1", { labels: ["ui"] }], ["3", { labels: ["bug", "ui"] }]])
    expect(host.textContent).toContain("2 changed, 1 already were")
    expect(onApplied).toHaveBeenCalledTimes(1)
    expect(onApplied.mock.calls[0]![0].changed.sort()).toEqual(["1", "3"])
  })

  test("assigns a person, and offers a due date only on hosts that have one", async () => {
    const { host } = await mount([issue("1"), issue("2")])
    expect([...el<HTMLSelectElement>(host, "What to change").options].map((o) => o.value)).not.toContain("dueDate") // GitHub
    await choose(el<HTMLSelectElement>(host, "Value"), "alice")
    await click(button(host, "Apply…"))
    await click(button(host, "Confirm"))
    await flush()
    expect(vi.mocked(api.editIssue).mock.calls.map(([, key, body]) => [key, body]).sort()).toEqual([["1", { assignees: ["alice"] }], ["2", { assignees: ["alice"] }]])
  })

  test("GitLab takes a due date, and Redmine's milestone is its version", async () => {
    const { host } = await mount([issue("1")], "gitlab:https://gitlab.example.com/g/p")
    await choose(el<HTMLSelectElement>(host, "What to change"), "dueDate")
    const date = el<HTMLInputElement>(host, "Due date")
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(date, "2026-12-01")
      date.dispatchEvent(new Event("input", { bubbles: true }))
    })
    await click(button(host, "Apply…"))
    await click(button(host, "Confirm"))
    await flush()
    expect(api.editIssue).toHaveBeenCalledWith("gitlab:https://gitlab.example.com/g/p", "1", { dueDate: "2026-12-01" })
  })

  test("reports the issues that failed with the host's reason", async () => {
    vi.mocked(api.editIssue).mockImplementation(async (_repo, key) => { if (key === "2") throw new Error("403 forbidden") })
    const { host } = await mount([issue("1"), issue("2"), issue("3")])
    await choose(el<HTMLSelectElement>(host, "What to change"), "state")
    await choose(el<HTMLSelectElement>(host, "State"), "CLOSED")
    await click(button(host, "Apply…"))
    await click(button(host, "Confirm"))
    await flush()
    expect(host.textContent).toContain("2 changed")
    expect(host.textContent).toContain("1 failed")
    expect(host.textContent).toContain("403 forbidden")
  })

  test("clearing is an explicit choice: an untouched control never unassigns anyone", async () => {
    const { host } = await mount([issue("1", { assignees: ["alice"] })])
    expect(button(host, "Apply…").disabled).toBe(true)
    await choose(el<HTMLSelectElement>(host, "Value"), "__clear__")
    await click(button(host, "Apply…"))
    await click(button(host, "Confirm"))
    await flush()
    expect(api.editIssue).toHaveBeenCalledWith("github:owner/repo", "1", { assignees: [] })
  })

  test("a label that does not exist yet is created from the bar, selected, and applied", async () => {
    vi.mocked(api.createEntity).mockResolvedValue({ name: "p1", color: "0e8a16" })
    const { host } = await mount([issue("1"), issue("2")])
    await choose(el<HTMLSelectElement>(host, "What to change"), "addLabel")
    expect(button(host, "Apply…").disabled).toBe(true)
    await click(button(host, "+ New label"))
    const input = host.querySelector<HTMLInputElement>('input[aria-label^="Name of the new"]')!
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "p1")
      input.dispatchEvent(new Event("input", { bubbles: true }))
    })
    await click(button(host, "Create"))
    await flush()
    expect(api.createEntity).toHaveBeenCalledWith("github:owner/repo", { kind: "label", name: "p1", color: "0e8a16" })
    expect(el<HTMLSelectElement>(host, "Value").value).toBe("p1") // chosen for the person
    await click(button(host, "Apply…"))
    await click(button(host, "Confirm"))
    await flush()
    expect(vi.mocked(api.editIssue).mock.calls.map(([, key, body]) => [key, body]).sort()).toEqual([["1", { labels: ["p1"] }], ["2", { labels: ["p1"] }]])
  })

  test("cancelling the confirmation sends nothing", async () => {
    const { host } = await mount([issue("1")])
    await choose(el<HTMLSelectElement>(host, "Value"), "bob")
    await click(button(host, "Apply…"))
    await click(button(host, "Cancel"))
    expect(api.editIssue).not.toHaveBeenCalled()
    expect(button(host, "Apply…")).toBeTruthy()
  })
})
