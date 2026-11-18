// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest"
import { createRoot, type Root } from "react-dom/client"
import { act } from "react"
import { BURST_MS, useLiveEvents, type ChangeEvent } from "./live"

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

// A stand-in for the browser's EventSource: the test opens it, drops it and sends it events.
class FakeSource {
  static last: FakeSource | null = null
  url: string
  closed = false
  onopen: (() => void) | null = null
  onerror: (() => void) | null = null
  listeners: Record<string, ((m: MessageEvent<string>) => void)[]> = {}
  constructor(url: string) { this.url = url; FakeSource.last = this }
  addEventListener(type: string, fn: (m: MessageEvent<string>) => void) { (this.listeners[type] ??= []).push(fn) }
  close() { this.closed = true }
  emit(e: Partial<ChangeEvent>) { for (const fn of this.listeners.change ?? []) fn({ data: JSON.stringify(e) } as MessageEvent<string>) }
}

let root: Root | null = null
let host: HTMLElement | null = null
const calls: ChangeEvent[][] = []
let live = false

function Probe({ repo }: { repo: string }) {
  live = useLiveEvents(repo, (batch) => calls.push(batch)).live
  return null
}

beforeEach(() => {
  vi.useFakeTimers()
  ;(globalThis as { EventSource?: unknown }).EventSource = FakeSource
  calls.length = 0
  host = document.createElement("div")
  root = createRoot(host)
  act(() => root!.render(<Probe repo="o/r" />))
})
afterEach(() => {
  act(() => root!.unmount())
  vi.useRealTimers()
})

describe("useLiveEvents", () => {
  test("follows the repo's stream, and a burst of events is one reload", () => {
    const src = FakeSource.last!
    expect(src.url).toBe("/api/events?repo=o%2Fr")
    act(() => src.onopen!())
    expect(live).toBe(true)
    act(() => { src.emit({ type: "sync-start" }); src.emit({ type: "sync-end", changed: 2 }); src.emit({ type: "edit" }) })
    expect(calls).toHaveLength(0)
    act(() => vi.advanceTimersByTime(BURST_MS))
    expect(calls).toHaveLength(1)
    expect(calls[0]!.map((e) => e.type)).toEqual(["sync-start", "sync-end", "edit"])
  })

  test("a drop turns live off, and coming back reads what was missed", () => {
    const src = FakeSource.last!
    act(() => src.onopen!())
    act(() => src.onerror!())
    expect(live).toBe(false)
    act(() => src.onopen!())
    act(() => vi.advanceTimersByTime(BURST_MS))
    expect(live).toBe(true)
    expect(calls).toHaveLength(1)
    expect(calls[0]).toEqual([])
  })

  test("another repo closes the old stream and opens its own", () => {
    const first = FakeSource.last!
    act(() => root!.render(<Probe repo="o/other" />))
    expect(first.closed).toBe(true)
    expect(FakeSource.last!.url).toBe("/api/events?repo=o%2Fother")
  })
})
