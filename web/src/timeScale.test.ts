import { describe, expect, it } from "vitest"
import { activityScale, fitZoom, timeScale } from "./timeScale"
import { DAY } from "./metrics"

const t = (d: number, h = 0) => Date.UTC(2026, 0, 1) + d * DAY + h * 3_600_000

describe("timeScale", () => {
  const times = [t(0), t(1), t(2), t(200), t(201)]

  it("collapses a long idle run into a short break and keeps order", () => {
    const s = timeScale(times, 40)
    expect(s.breaks).toHaveLength(1)
    expect(s.breaks[0]!.days).toBe(197)
    const xs = times.map(s.x)
    expect(xs).toEqual([...xs].sort((a, b) => a - b))
    // a plain linear axis would put day 200 at 8000 px; here the gap is a marker
    expect(s.x(t(200)) - s.x(t(2))).toBeLessThan(40 * 3 + 100)
  })

  it("keeps short idle runs linear", () => {
    const s = timeScale([t(0), t(2)], 40)
    expect(s.breaks).toHaveLength(0)
    expect(s.x(t(2)) - s.x(t(0))).toBeCloseTo(80)
  })

  it("separates events of the same day by the hour", () => {
    const s = timeScale([t(0), t(1)], 480)
    expect(s.x(t(0, 12)) - s.x(t(0, 6))).toBeCloseTo(120)
  })
})

describe("activityScale", () => {
  it("spaces events evenly whatever the time between them", () => {
    const s = activityScale([t(0), t(0, 1), t(300)], 10)
    expect(s.x(t(0, 1)) - s.x(t(0))).toBeCloseTo(10)
    expect(s.x(t(300)) - s.x(t(0, 1))).toBeCloseTo(10)
    expect(s.x(t(150))).toBeGreaterThan(s.x(t(0, 1)))
  })
})

describe("fitZoom", () => {
  it("finds the zoom at which the axis fits the viewport", () => {
    const times = Array.from({ length: 40 }, (_, i) => t(i))
    const z = fitZoom("tiempo", times, 1200, [4, 900])
    expect(timeScale(times, z).width).toBeLessThanOrEqual(1200 + 1)
    expect(timeScale(times, z * 1.1).width).toBeGreaterThan(1200)
  })
})
