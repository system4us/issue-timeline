// @vitest-environment jsdom
import { afterEach, describe, expect, test, vi } from "vitest"
import { createRoot, type Root } from "react-dom/client"
import { act } from "react"
import { Timeline } from "./Timeline"
import { makeFixture, timelineProps } from "./timelineFixture"
import { refOf } from "./keys"

// A render counter: a bar's label and the tooltip's header call refOf when they render, so refOf calls are renders.
vi.mock("./keys", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./keys")>()
  return { ...actual, refOf: vi.fn(actual.refOf) }
})
const renders = () => vi.mocked(refOf).mock.calls.length

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const mounted: { host: HTMLElement; root: Root }[] = []
afterEach(() => {
  for (const { host, root } of mounted.splice(0)) {
    act(() => root.unmount())
    host.remove()
  }
})

type Extra = Partial<Parameters<typeof timelineProps>[1]> & { selected?: unknown; onSelect?: (i: unknown) => void }

async function mount(extra: Extra = {}) {
  const host = document.createElement("div")
  document.body.appendChild(host)
  const root = createRoot(host)
  mounted.push({ host, root })
  const props = { ...timelineProps(makeFixture(), extra), ...extra } as ReturnType<typeof timelineProps>
  await act(async () => root.render(<Timeline {...props} />))
  return { host, root, props }
}

const mouse = (type: string, el: Element, at: { x?: number; y?: number; from?: Element | null } = {}) =>
  el.dispatchEvent(new MouseEvent(type, { bubbles: true, clientX: at.x ?? 300, clientY: at.y ?? 200, relatedTarget: at.from ?? null }))

/** The pointer goes from one bar to another, as a browser reports it: out of the first, into the second. */
async function enter(to: Element, from: Element | null = null, x = 300, y = 200) {
  await act(async () => {
    if (from) mouse("mouseout", from, { x, y, from: to })
    mouse("mouseover", to, { x, y, from })
  })
}

/** Lets the frame that places the tooltip run. */
const nextFrame = () => new Promise((r) => setTimeout(r, 60))

/** Every bar's class and inline style: what the bars look like, in order. */
const barLook = (host: HTMLElement) => [...host.querySelectorAll(".bar")].map((b) => `${b.getAttribute("class")}|${b.getAttribute("style")}`)

describe("the timeline's hover", () => {
  test("moving the mouse inside one bar changes no bar and no line", async () => {
    const { host } = await mount()
    const bars = [...host.querySelectorAll<HTMLElement>(".bar")]
    const target = bars.find((b) => b.classList.contains("issue") && b.textContent?.includes("·"))!
    await enter(target)
    const bg = host.querySelector("g.lines-bg")
    const fg = host.querySelector("g.lines-fg")
    const before = barLook(host)
    const bgChildren = bg ? [...bg.children].map((c) => c.getAttribute("d")) : []

    vi.mocked(refOf).mockClear()
    for (let k = 0; k < 40; k++) await act(async () => mouse("mousemove", target, { x: 300 + k, y: 200 + k }))

    expect(renders()).toBe(0) // no bar and no tooltip rendered by the moves
    expect(barLook(host)).toEqual(before)
    expect(host.querySelector("g.lines-bg")).toBe(bg) // the background layer is the same node
    expect(host.querySelector("g.lines-fg")).toBe(fg)
    expect(host.querySelector("g.lines-bg")!.children.length).toBe(bgChildren.length)
  })

  test("the tooltip follows the pointer, kept beside it", async () => {
    const { host } = await mount()
    const target = host.querySelector<HTMLElement>(".bar.issue")!
    await enter(target, null, 300, 200)
    await act(async () => mouse("mousemove", target, { x: 340, y: 260 }))
    await nextFrame()
    const tip = host.querySelector<HTMLElement>(".tooltip") ?? document.querySelector<HTMLElement>(".tooltip")!
    expect(tip.style.left).toBe("354px") // 340 + the gap of 14 (jsdom measures nothing, so the box is 0 wide)
    expect(tip.style.top).toBe("274px")
  })

  test("entering another bar changes only the bars that are related to the focus, and the background stays", async () => {
    const { host } = await mount()
    const bars = [...host.querySelectorAll<HTMLElement>(".bar")]
    const first = bars.find((b) => b.classList.contains("issue"))!
    const second = bars.find((b) => b.classList.contains("pull"))!
    await enter(first)
    const bg = host.querySelector("g.lines-bg")
    const before = barLook(host)
    vi.mocked(refOf).mockClear()
    await enter(second, first)
    const after = barLook(host)
    const changed = before.filter((v, k) => v !== after[k]).length
    expect(changed).toBeGreaterThan(0)
    expect(changed).toBeLessThan(bars.length / 4)
    expect(renders()).toBeLessThan(bars.length / 4) // a few bars and the tooltip, not all the bars
    expect(host.querySelector("g.lines-bg")).toBe(bg)
    expect(host.querySelectorAll(".bar.selected").length).toBe(1)
  })

  test("leaving the bars hides the tooltip and dims nothing", async () => {
    const { host } = await mount()
    const target = host.querySelector<HTMLElement>(".bar.issue")!
    await enter(target)
    expect(document.querySelector(".tooltip")).not.toBeNull()
    await act(async () => mouse("mouseout", target, { from: null }))
    expect(document.querySelector(".tooltip")).toBeNull()
    expect(host.querySelectorAll(".bar.dim").length).toBe(0)
  })
})

describe("the timeline's clicks", () => {
  test("a click on a bar selects it, and a click on the selected bar clears the selection", async () => {
    const onSelect = vi.fn()
    const { host, props, root } = await mount({ onSelect })
    const target = host.querySelector<HTMLElement>(".bar.issue")!
    await act(async () => target.dispatchEvent(new MouseEvent("click", { bubbles: true })))
    expect(onSelect).toHaveBeenLastCalledWith(expect.objectContaining({ kind: "issue" }))
    const item = onSelect.mock.calls[0][0]
    await act(async () => root.render(<Timeline {...props} selected={item} />))
    await act(async () => target.dispatchEvent(new MouseEvent("click", { bubbles: true })))
    expect(onSelect).toHaveBeenLastCalledWith(null)
  })

  test("a right-click on a bar opens the menu for it", async () => {
    const { host } = await mount()
    const target = host.querySelector<HTMLElement>(".bar.pull")!
    await act(async () => target.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 50, clientY: 50 })))
    expect(document.body.textContent).toContain("Open details")
    expect(document.body.textContent).toContain("Copy link")
  })
})
