// The horizontal axes of the timeline. A repo's activity comes in bursts (hundreds of issues in a few days after
// months of nothing), so a plain linear time axis makes the bursts a few pixels wide and one issue open for years takes
// all the room. Two axes avoid that:
//  - "tiempo": linear time, but runs of idle days collapse into a short break marker;
//  - "actividad": events are spaced evenly in the order they happened (every creation, close or merge is one step).
import { DAY } from "./metrics"

export type Axis = "tiempo" | "actividad"

export interface Tick { x: number; label: string; major: boolean }
export interface Break { x0: number; x1: number; days: number }
export interface Scale {
  x: (t: number) => number
  width: number
  breaks: Break[]
  ticks: Tick[]
}

const LEFT = 24
const MIN_IDLE_DAYS = 3
const BREAK_PX = 44
const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"]

const dayLabel = (day: number) => {
  const d = new Date(day * DAY)
  return { text: `${d.getUTCDate()} ${MONTHS[d.getUTCMonth()]}`, month: d.getUTCDate() === 1, year: d.getUTCFullYear(), first: d.getUTCDate() === 1 && d.getUTCMonth() === 0 }
}

/** Linear time with the idle runs (≥ 3 days and wider than the marker) collapsed to a fixed-width break. */
export function timeScale(times: number[], pxPerDay: number): Scale {
  const days = [...new Set(times.map((t) => Math.floor(t / DAY)))].sort((a, b) => a - b)
  const first = days[0] ?? 0
  const runs: { a: number; b: number }[] = [] // idle spans in fractional days since `first`: [a, b)
  for (let i = 0; i + 1 < days.length; i++) {
    const gap = days[i + 1]! - days[i]! - 1
    if (gap >= MIN_IDLE_DAYS && gap * pxPerDay > BREAK_PX) runs.push({ a: days[i]! + 1 - first, b: days[i + 1]! - first })
  }
  const x = (t: number) => {
    const d = t / DAY - first
    let px = d * pxPerDay
    for (const r of runs) {
      const saved = (r.b - r.a) * pxPerDay - BREAK_PX
      if (d >= r.b) px -= saved
      else {
        if (d > r.a) px -= ((d - r.a) / (r.b - r.a)) * saved
        break
      }
    }
    return LEFT + px
  }
  const last = days.at(-1) ?? first
  const breaks = runs.map((r) => ({ x0: x((first + r.a) * DAY), x1: x((first + r.b) * DAY), days: r.b - r.a }))
  const ticks: Tick[] = []
  let lastX = -1e9
  const everyDay = pxPerDay >= 30
  let lastWeek = -1
  for (const d of days) {
    const week = Math.floor((d + 3) / 7) // Monday-based week number
    const wanted = everyDay || week !== lastWeek || dayLabel(d).month
    lastWeek = week
    const px = x(d * DAY)
    if (!wanted || px - lastX < 46) continue
    const l = dayLabel(d)
    ticks.push({ x: px, label: l.first ? String(l.year) : l.text, major: l.month })
    lastX = px
  }
  return { x, width: x((last + 1) * DAY) + 140, breaks, ticks }
}

/** Events spaced evenly in the order they happened: `pxPerStep` per distinct timestamp. */
export function activityScale(times: number[], pxPerStep: number): Scale {
  const sorted = [...new Set(times)].sort((a, b) => a - b)
  const x = (t: number) => {
    if (sorted.length === 0) return LEFT
    // rank of t among the events, interpolated between the two neighbours
    let lo = 0
    let hi = sorted.length - 1
    if (t <= sorted[0]!) return LEFT
    if (t >= sorted[hi]!) return LEFT + hi * pxPerStep
    while (hi - lo > 1) {
      const mid = (lo + hi) >> 1
      if (sorted[mid]! <= t) lo = mid
      else hi = mid
    }
    return LEFT + (lo + (t - sorted[lo]!) / (sorted[hi]! - sorted[lo]!)) * pxPerStep
  }
  const ticks: Tick[] = []
  let lastDay = -1
  let lastX = -1e9
  sorted.forEach((t, i) => {
    const day = Math.floor(t / DAY)
    if (day === lastDay) return
    lastDay = day
    const px = LEFT + i * pxPerStep
    if (px - lastX < 52) return
    const l = dayLabel(day)
    ticks.push({ x: px, label: l.first ? String(l.year) : l.text, major: l.month })
    lastX = px
  })
  return { x, width: LEFT + sorted.length * pxPerStep + 140, breaks: [], ticks }
}

export const makeScale = (axis: Axis, times: number[], zoom: number): Scale => (axis === "tiempo" ? timeScale(times, zoom) : activityScale(times, zoom))

/** The zoom (px per day, or per step) at which the whole axis fits `viewport` px, within [min, max]. */
export function fitZoom(axis: Axis, times: number[], viewport: number, [min, max]: [number, number]): number {
  let lo = min
  let hi = max
  for (let i = 0; i < 24; i++) {
    const mid = (lo + hi) / 2
    if (makeScale(axis, times, mid).width > viewport) hi = mid
    else lo = mid
  }
  return Math.max(min, Math.min(max, lo))
}

export const ZOOM_RANGE: Record<Axis, [number, number]> = { tiempo: [4, 900], actividad: [1, 80] }
