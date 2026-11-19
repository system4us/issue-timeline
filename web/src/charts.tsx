import { useLayoutEffect, useRef, useState, type ReactNode } from "react"
import type { Bin, CyclePoint, OpenPoint, WeekPoint } from "./metrics"
import { refOf } from "./keys"

// Hand-rolled SVG charts that share one frame: measured at their real width (so text is drawn at its real size, not
// scaled down with a viewBox), round axis ticks, one tooltip, one empty state, the colors from the CSS tokens
// (--chart-a, --chart-b, --chart-c), so light and dark mode come with the theme.

const H = 170
const M = { l: 34, r: 12, t: 10, b: 22 }

/** The width of an element, followed as it resizes (a widget made wider or narrower redraws its chart). */
function useWidth<T extends HTMLElement>(fallback = 480) {
  const ref = useRef<T>(null)
  const [w, setW] = useState(fallback)
  useLayoutEffect(() => {
    const el = ref.current
    if (!el) return
    const read = () => { const cw = el.clientWidth; if (cw > 0) setW(cw) }
    read()
    if (typeof ResizeObserver === "undefined") return
    const ro = new ResizeObserver(read)
    ro.observe(el)
    return () => ro.disconnect()
  }, [])
  return [ref, w] as const
}

/** 0 and a few round steps (1, 2, 5 × 10ⁿ) that cover `max`. */
export function niceTicks(max: number, count = 3): number[] {
  if (!(max > 0)) return [0, 1]
  const raw = max / count
  const mag = 10 ** Math.floor(Math.log10(raw))
  const step = [1, 2, 5, 10].map((f) => f * mag).find((s) => s >= raw)!
  const top = Math.ceil(max / step) * step
  const out: number[] = []
  for (let v = 0; v <= top + step / 2; v += step) out.push(Number(v.toPrecision(12)))
  return out
}

const fmtTick = (v: number) => (v >= 10_000 ? `${Math.round(v / 1000)}k` : v >= 1000 ? `${(v / 1000).toFixed(1)}k` : String(v))
const fmtDay = (t: number) => new Date(t).toISOString().slice(0, 10)
const shortDay = (t: number, withYear: boolean) =>
  new Date(t).toLocaleDateString("en-GB", { month: "short", day: "numeric", ...(withYear ? { year: "2-digit" } : {}), timeZone: "UTC" })

export function Empty({ children = "No data in this view." }: { children?: ReactNode }) {
  return <p className="empty">{children}</p>
}

interface Tip { x: number; y: number; text: string }

/** The frame every chart draws in: measured width, the horizontal grid with its ticks, the tooltip. */
function Frame({ max, label, children }: {
  max: number
  label: string
  children: (g: { w: number; iw: number; ih: number; y: (v: number) => number; tip: (t: Tip | null) => void }) => ReactNode
}) {
  const [ref, w] = useWidth<HTMLDivElement>()
  const [tip, setTip] = useState<Tip | null>(null)
  const ticks = niceTicks(max)
  const top = ticks.at(-1)!
  const iw = Math.max(40, w - M.l - M.r)
  const ih = H - M.t - M.b
  const y = (v: number) => M.t + ih - (top ? (v / top) * ih : 0)
  return (
    <div className="chart-wrap" ref={ref} onPointerLeave={() => setTip(null)}>
      <svg width={w} height={H} className="chart" role="img" aria-label={label}>
        {ticks.map((v) => (
          <g key={v}>
            <line x1={M.l} x2={w - M.r} y1={y(v)} y2={y(v)} className={v === 0 ? "axis" : "grid"} />
            <text x={M.l - 6} y={y(v) + 4} textAnchor="end" className="tick">{fmtTick(v)}</text>
          </g>
        ))}
        {children({ w, iw, ih, y, tip: setTip })}
      </svg>
      {tip && <div className="chart-tip" style={{ left: Math.min(tip.x, w - 8), top: tip.y }}>{tip.text}</div>}
    </div>
  )
}

/** Date labels under a series of times, one every so many px so they never overlap. */
function dateLabels(times: number[], at: (i: number) => number, iw: number) {
  if (times.length === 0) return []
  const withYear = new Date(times[0]!).getUTCFullYear() !== new Date(times.at(-1)!).getUTCFullYear()
  const room = Math.max(2, Math.floor(iw / (withYear ? 90 : 70)))
  const step = Math.max(1, Math.ceil((times.length - 1) / (room - 1)))
  const idx: number[] = []
  for (let i = 0; i < times.length; i += step) idx.push(i)
  // the last time always gets its label: it takes the place of the one before when the two would be too close
  if (idx.at(-1) !== times.length - 1) { if (idx.length > 1 && times.length - 1 - idx.at(-1)! < step) idx.pop(); idx.push(times.length - 1) }
  return idx.map((i) => ({ at: at(i), text: shortDay(times[i]!, withYear) }))
}

/** A slot the keyboard can reach: Enter or space picks it, focus shows its tooltip. */
function Hit({ x, y, width, height, text, onPick, tip }: { x: number; y: number; width: number; height: number; text: string; onPick?: () => void; tip: (t: Tip | null) => void }) {
  const show = () => tip({ x: x + width / 2, y: Math.max(0, y - 4), text })
  return (
    <rect
      x={x} y={y} width={width} height={height}
      className={onPick ? "hit" : "hit passive"}
      tabIndex={onPick ? 0 : -1}
      role={onPick ? "button" : undefined}
      aria-label={text}
      onPointerEnter={show} onFocus={show} onBlur={() => tip(null)}
      onClick={onPick}
      onKeyDown={(e) => { if (onPick && (e.key === "Enter" || e.key === " ")) { e.preventDefault(); onPick() } }}
    />
  )
}

function Legend({ items }: { items: [string, string][] }) {
  return <p className="legend">{items.map(([cls, text]) => <span key={cls}><i className={`sw ${cls}`} /> {text}</span>)}</p>
}

/** Issues open over time, stacked on the ones closed so far when `stacked` (a cumulative flow: the top is all created). */
export function OpenLine({ points, stacked = false }: { points: OpenPoint[]; stacked?: boolean }) {
  if (points.length < 2) return <Empty />
  const max = Math.max(1, ...points.map((p) => (stacked ? p.open + p.closed : p.open)))
  const t0 = points[0]!.t
  const t1 = points.at(-1)!.t
  return (
    <>
      <Frame max={max} label={stacked ? "Cumulative flow of issues" : "Open issues over time"}>
        {({ iw, ih, y, tip }) => {
          const x = (t: number) => M.l + ((t - t0) / (t1 - t0 || 1)) * iw
          const path = (v: (p: OpenPoint) => number) => points.map((p, i) => `${i ? "L" : "M"}${x(p.t).toFixed(1)},${y(v(p)).toFixed(1)}`).join(" ")
          const area = (hi: (p: OpenPoint) => number, lo: (p: OpenPoint) => number) =>
            `${path(hi)} ${[...points].reverse().map((p) => `L${x(p.t).toFixed(1)},${y(lo(p)).toFixed(1)}`).join(" ")} Z`
          const slot = iw / points.length
          const labels = dateLabels(points.map((p) => p.t), (i) => x(points[i]!.t), iw)
          return (
            <>
              {labels.map((l, i) => <text key={`x${i}`} x={l.at} y={H - 5} textAnchor={i === 0 ? "start" : i === labels.length - 1 ? "end" : "middle"} className="tick">{l.text}</text>)}
              {stacked && <path d={area((p) => p.open + p.closed, (p) => p.open)} className="area-b" />}
              <path d={area((p) => p.open, () => 0)} className="area-a" />
              <path d={path((p) => p.open)} className="line-a" />
              {stacked && <path d={path((p) => p.open + p.closed)} className="line-b" />}
              {points.map((p) => (
                <Hit key={p.t} x={x(p.t) - slot / 2} y={M.t} width={slot} height={ih} tip={tip}
                  text={`${fmtDay(p.t)}: ${p.open} open${stacked ? `, ${p.closed} closed` : ""}`} />
              ))}
            </>
          )
        }}
      </Frame>
      {stacked && <Legend items={[["a", "open"], ["b", "closed"]]} />}
    </>
  )
}

/** Two counts per week side by side (created and closed, opened and merged). */
export function WeeklyBars({ weeks, aLabel, bLabel, onPick }: { weeks: WeekPoint[]; aLabel: string; bLabel: string; onPick?: (week: number) => void }) {
  if (weeks.length === 0) return <Empty />
  const max = Math.max(1, ...weeks.flatMap((w) => [w.a, w.b]))
  return (
    <>
      <Frame max={max} label={`${aLabel} and ${bLabel} per week`}>
        {({ iw, ih, y, tip }) => {
          const slot = iw / weeks.length
          const gap = slot > 8 ? Math.min(4, slot * 0.2) : 0
          const bw = Math.max(1, (slot - gap) / 2)
          const labels = dateLabels(weeks.map((w) => w.week), (i) => M.l + i * slot + slot / 2, iw)
          return (
            <>
              {labels.map((l, i) => <text key={`x${i}`} x={l.at} y={H - 5} textAnchor="middle" className="tick">{l.text}</text>)}
              {weeks.map((w, i) => (
                <g key={w.week}>
                  <rect x={M.l + i * slot + gap / 2} y={y(w.a)} width={bw} height={y(0) - y(w.a)} className="bar-a" />
                  <rect x={M.l + i * slot + gap / 2 + bw} y={y(w.b)} width={bw} height={y(0) - y(w.b)} className="bar-b" />
                </g>
              ))}
              {weeks.map((w, i) => (
                <Hit key={`h${w.week}`} x={M.l + i * slot} y={M.t} width={slot} height={ih} tip={tip}
                  text={`Week of ${fmtDay(w.week)}: ${aLabel} ${w.a}, ${bLabel} ${w.b}${onPick ? " · click to list" : ""}`}
                  onPick={onPick && (() => onPick(w.week))} />
              ))}
            </>
          )
        }}
      </Frame>
      <Legend items={[["a", aLabel], ["b", bLabel]]} />
    </>
  )
}

export function Histogram({ bins, unit, onPick }: { bins: Bin[]; unit: string; onPick?: (bin: Bin) => void }) {
  if (bins.every((b) => b.count === 0)) return <Empty />
  const max = Math.max(1, ...bins.map((b) => b.count))
  const label = (b: Bin) => (b.to === Infinity ? `≥ ${b.from}` : `< ${b.to}`)
  return (
    <Frame max={max} label={`Histogram in ${unit}`}>
      {({ iw, ih, y, tip }) => {
        const slot = iw / bins.length
        return (
          <>
            {bins.map((b, i) => (
              <g key={i}>
                <rect x={M.l + i * slot + slot * 0.12} y={y(b.count)} width={slot * 0.76} height={y(0) - y(b.count)} className="bar-a" />
                {b.count > 0 && <text x={M.l + i * slot + slot / 2} y={y(b.count) - 4} textAnchor="middle" className="value">{b.count}</text>}
                <text x={M.l + i * slot + slot / 2} y={H - 5} textAnchor="middle" className="tick">{label(b)}</text>
              </g>
            ))}
            {bins.map((b, i) => (
              <Hit key={`h${i}`} x={M.l + i * slot} y={M.t} width={slot} height={ih} tip={tip}
                text={`${b.to === Infinity ? `${b.from} ${unit} or more` : `${b.from} to ${b.to} ${unit}`}: ${b.count}${onPick ? " · click to list" : ""}`}
                onPick={onPick && (() => onPick(b))} />
            ))}
          </>
        )
      }}
    </Frame>
  )
}

/** Throughput (issues closed per week, bars) under the median days those issues had taken (a line, its own scale on the right). */
export function CycleTrend({ points, onPick }: { points: CyclePoint[]; onPick?: (week: number) => void }) {
  if (points.length === 0 || points.every((p) => p.closed === 0)) return <Empty />
  const maxN = Math.max(1, ...points.map((p) => p.closed))
  const meds = points.map((p) => p.median).filter((v): v is number => v !== null)
  const maxD = Math.max(1, ...meds)
  const dTop = niceTicks(maxD).at(-1)!
  return (
    <>
      <Frame max={maxN} label="Throughput and median days to close, per week">
        {({ w, iw, ih, y, tip }) => {
          const slot = iw / points.length
          const yd = (d: number) => M.t + ih - (d / dTop) * ih
          const cx = (i: number) => M.l + i * slot + slot / 2
          let d = ""
          points.forEach((p, i) => { if (p.median !== null) d += `${d ? "L" : "M"}${cx(i).toFixed(1)},${yd(p.median).toFixed(1)} ` })
          const labels = dateLabels(points.map((p) => p.week), cx, iw)
          return (
            <>
              {labels.map((l, i) => <text key={`x${i}`} x={l.at} y={H - 5} textAnchor="middle" className="tick">{l.text}</text>)}
              <text x={w - 2} y={M.t + 4} textAnchor="end" className="tick">{fmtTick(dTop)} d</text>
              {points.map((p, i) => <rect key={p.week} x={M.l + i * slot + slot * 0.15} y={y(p.closed)} width={Math.max(1, slot * 0.7)} height={y(0) - y(p.closed)} className="bar-soft" />)}
              <path d={d} className="line-c" />
              {points.map((p, i) => p.median !== null && <circle key={`c${p.week}`} cx={cx(i)} cy={yd(p.median)} r={2.5} className="dot-c" />)}
              {points.map((p, i) => (
                <Hit key={`h${p.week}`} x={M.l + i * slot} y={M.t} width={slot} height={ih} tip={tip}
                  text={`Week of ${fmtDay(p.week)}: ${p.closed} closed${p.median === null ? "" : `, median ${p.median.toFixed(1)} d, p90 ${p.p90!.toFixed(1)} d`}${onPick ? " · click to list" : ""}`}
                  onPick={onPick && (() => onPick(p.week))} />
              ))}
            </>
          )
        }}
      </Frame>
      <Legend items={[["soft", "issues closed (left)"], ["c", "median days to close them (right)"]]} />
    </>
  )
}

export function Scatter({ points, onPick }: { points: { key: string; size: number; days: number }[]; onPick?: (key: string) => void }) {
  if (points.length === 0) return <Empty />
  if (points.every((p) => p.size === 0)) return <Empty>The tracker gives no size for these PRs.</Empty>
  const maxX = Math.max(1, ...points.map((p) => Math.log10(p.size + 1)))
  const maxY = Math.max(1, ...points.map((p) => p.days))
  const xTicks = [1, 10, 100, 1000, 10_000, 100_000].filter((v) => Math.log10(v + 1) <= maxX + 0.01)
  return (
    <>
      <Frame max={maxY} label="PR size against days to merge">
        {({ iw, y, tip }) => {
          const x = (s: number) => M.l + (Math.log10(s + 1) / maxX) * iw
          return (
            <>
              {xTicks.map((v) => <text key={v} x={x(v)} y={H - 5} textAnchor="middle" className="tick">{fmtTick(v)}</text>)}
              {points.map((p) => (
                <circle key={p.key} cx={x(p.size)} cy={y(p.days)} r={3.5} className="dot"
                  tabIndex={onPick ? 0 : -1} role={onPick ? "button" : undefined}
                  aria-label={`${refOf(p.key)}: ${p.size} lines, ${p.days.toFixed(1)} days`}
                  onPointerEnter={() => tip({ x: x(p.size), y: y(p.days) - 8, text: `${refOf(p.key)}: ${p.size} lines, ${p.days.toFixed(1)} d to merge` })}
                  onClick={() => onPick?.(p.key)}
                  onKeyDown={(e) => { if (e.key === "Enter") onPick?.(p.key) }} />
              ))}
            </>
          )
        }}
      </Frame>
      <p className="legend">Across: lines changed (log scale) · up: days to merge · click a dot to open the PR</p>
    </>
  )
}
