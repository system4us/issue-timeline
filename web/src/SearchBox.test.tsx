// @vitest-environment jsdom
import { afterEach, describe, expect, test, vi } from "vitest"
import { createRoot, type Root } from "react-dom/client"
import { act } from "react"
import { SearchBox } from "./SearchBox"
import { searchRepo, type SearchHit } from "./api"

vi.mock("./api", () => ({ searchRepo: vi.fn() }))

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const hits: SearchHit[] = [
  { key: "acme/app#1", kind: "issue", title: "Login fails", state: "open", snippet: null },
  { key: "acme/app#2", kind: "issue", title: "Login slow", state: "closed", snippet: "on mobile" },
  { key: "acme/app#3", kind: "pull", title: "Fix login", state: "merged", snippet: null },
]

const mounted: { host: HTMLElement; root: Root }[] = []
afterEach(() => {
  for (const { host, root } of mounted.splice(0)) {
    act(() => root.unmount())
    host.remove()
  }
  vi.mocked(searchRepo).mockReset()
})

async function mount(onPick = vi.fn()) {
  const host = document.createElement("div")
  document.body.appendChild(host)
  const root = createRoot(host)
  mounted.push({ host, root })
  await act(async () => root.render(<SearchBox repo="acme/app" onPick={onPick} />))
  return { host, onPick, input: host.querySelector<HTMLInputElement>("input")! }
}

/** Types into the input the way a user does: the value setter, then an input event React listens to. */
async function type(input: HTMLInputElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!
  await act(async () => {
    setter.call(input, value)
    input.dispatchEvent(new Event("input", { bubbles: true }))
  })
}
const key = (el: EventTarget, k: string) => act(async () => { el.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true })) })
/** Lets the 200 ms pause and the search promise finish. */
const settle = () => act(() => new Promise((r) => setTimeout(r, 260)))

describe("SearchBox", () => {
  test("typing shows the results", async () => {
    vi.mocked(searchRepo).mockResolvedValue(hits)
    const { host, input } = await mount()
    await type(input, "login")
    await settle()
    expect(searchRepo).toHaveBeenCalledWith("acme/app", "login")
    const options = host.querySelectorAll('[role="option"]')
    expect(options.length).toBe(3)
    expect(input.getAttribute("aria-expanded")).toBe("true")
    expect(input.getAttribute("aria-controls")).toBe(host.querySelector('[role="listbox"]')!.id)
  })

  test("the loading state shows while a query is in flight", async () => {
    vi.mocked(searchRepo).mockReturnValue(new Promise(() => {}))
    const { host, input } = await mount()
    await type(input, "login")
    await settle()
    expect(host.querySelector(".search-status")?.textContent).toBe("Searching…")
    expect(host.textContent).not.toContain("Nothing in this repo matches")
  })

  test("ArrowDown then Enter opens the second hit", async () => {
    vi.mocked(searchRepo).mockResolvedValue(hits)
    const { host, input, onPick } = await mount()
    await type(input, "login")
    await settle()
    await key(input, "ArrowDown")
    const options = host.querySelectorAll('[role="option"]')
    expect(options[1].getAttribute("aria-selected")).toBe("true")
    expect(input.getAttribute("aria-activedescendant")).toBe(options[1].id)
    await key(input, "Enter")
    expect(onPick).toHaveBeenCalledTimes(1)
    expect(onPick).toHaveBeenCalledWith("issue", "acme/app#2")
  })

  test("Enter without arrows opens the first hit", async () => {
    vi.mocked(searchRepo).mockResolvedValue(hits)
    const { input, onPick } = await mount()
    await type(input, "login")
    await settle()
    await key(input, "Enter")
    expect(onPick).toHaveBeenCalledWith("issue", "acme/app#1")
  })

  test("Escape closes the results and keeps the text", async () => {
    vi.mocked(searchRepo).mockResolvedValue(hits)
    const { host, input } = await mount()
    await type(input, "login")
    await settle()
    await key(input, "Escape")
    expect(host.querySelector(".search-results")).toBeNull()
    expect(input.getAttribute("aria-expanded")).toBe("false")
    expect(input.value).toBe("login")
  })

  test("the results panel is the box's child, so the CSS can position it absolutely over the page", async () => {
    vi.mocked(searchRepo).mockResolvedValue(hits)
    const { host, input } = await mount()
    await type(input, "login")
    await settle()
    expect(host.querySelector(".search-box > .search-results")).not.toBeNull()
  })

  test("\"/\" focuses the search from the page, but not from another text field", async () => {
    vi.mocked(searchRepo).mockResolvedValue([])
    const { host, input } = await mount()
    await key(document.body, "/")
    expect(document.activeElement).toBe(input)
    const other = document.createElement("textarea")
    document.body.appendChild(other)
    other.focus()
    await key(other, "/")
    expect(document.activeElement).toBe(other)
    other.remove()
    expect(host.querySelector(".search-kbd")?.textContent).toBe("/")
  })
})
