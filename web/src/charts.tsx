import type { Bin, OpenPoint, WeekPoint } from "./metrics"

// Small hand-rolled SVG charts: enough for a POC, no chart library.
const W = 560
const H = 180
const M = { l: 36, r: 10, t: 10, b: 24 }
const iw = W - M.l - M.r
const ih = H - M.t - M.b
const fmtDay = (t: number) => new Date(t).toISOString().slice(0, 10)

function Frame({ max, children, yLabel }: { max: number; children: React.ReactNode; yLabel?: string }) {
  const ticks = [0, 0.5, 1].map((f) => Math.round(max * f))
  return (
    <svg viewBox={`0 0 ${W} ${H}`} className="chart" role="img" aria-label={yLabel}>
      {ticks.map((v) => {
        const y = M.t + ih - (max ? (v / max) * ih : 0)
        return (
          <g key={v}>
            <line x1={M.l} x2={W - M.r} y1={y} y2={y} className="grid" />
            <text x={M.l - 4} y={y + 3} textAnchor="end" className="tick">{v}</text>
          </g>
        )
      })}
      {children}
    </svg>
  )
}

export function OpenLine({ points }: { points: OpenPoint[] }) {
  if (points.length < 2) return <p className="muted">No data.</p>
  const max = Math.max(1, ...points.map((p) => p.open))
  const t0 = points[0]!.t
  const t1 = points.at(-1)!.t
  const x = (t: number) => M.l + ((t - t0) / (t1 - t0 || 1)) * iw
  const y = (v: number) => M.t + ih - (v / max) * ih
  const d = points.map((p, i) => `${i ? "L" : "M"}${x(p.t).toFixed(1)},${y(p.open).toFixed(1)}`).join(" ")
  return (
    <Frame max={max} yLabel="Open issues over time">
      <path d={`${d} L${x(t1)},${y(0)} L${x(t0)},${y(0)} Z`} className="area" />
      <path d={d} className="line" />
      <text x={M.l} y={H - 6} className="tick">{fmtDay(t0)}</text>
      <text x={W - M.r} y={H - 6} textAnchor="end" className="tick">{fmtDay(t1)}</text>
    </Frame>
  )
}

export function WeeklyBars({ weeks, aLabel, bLabel, onPick }: { weeks: WeekPoint[]; aLabel: string; bLabel: string; onPick?: (week: number) => void }) {
  if (weeks.length === 0) return <p className="muted">No data.</p>
  const max = Math.max(1, ...weeks.flatMap((w) => [w.a, w.b]))
  const slot = iw / weeks.length
  const bw = Math.max(1, slot / 2 - 0.5)
  const y = (v: number) => M.t + ih - (v / max) * ih
  return (
    <div>
      <Frame max={max} yLabel={`${aLabel} y ${bLabel} por semana`}>
        {weeks.map((w, i) => (
          <g key={w.week}>
            <rect x={M.l + i * slot} y={y(w.a)} width={bw} height={M.t + ih - y(w.a)} className="bar-a"><title>{`${fmtDay(w.week)}: ${aLabel} ${w.a}`}</title></rect>
            <rect x={M.l + i * slot + bw} y={y(w.b)} width={bw} height={M.t + ih - y(w.b)} className="bar-b"><title>{`${fmtDay(w.week)}: ${bLabel} ${w.b}`}</title></rect>
          </g>
        ))}
        {onPick && weeks.map((w, i) => (
          <rect key={`hit-${w.week}`} x={M.l + i * slot} y={M.t} width={slot} height={ih} className="hit" onClick={() => onPick(w.week)}>
            <title>{`${fmtDay(w.week)}: ${aLabel} ${w.a}, ${bLabel} ${w.b} · click to list`}</title>
          </rect>
        ))}
        <text x={M.l} y={H - 6} className="tick">{fmtDay(weeks[0]!.week)}</text>
        <text x={W - M.r} y={H - 6} textAnchor="end" className="tick">{fmtDay(weeks.at(-1)!.week)}</text>
      </Frame>
      <p className="legend"><i className="sw bar-a" /> {aLabel} <i className="sw bar-b" /> {bLabel}</p>
    </div>
  )
}

export function Histogram({ bins, unit, onPick }: { bins: Bin[]; unit: string; onPick?: (bin: Bin) => void }) {
  const max = Math.max(1, ...bins.map((b) => b.count))
  const slot = iw / bins.length
  const y = (v: number) => M.t + ih - (v / max) * ih
  const label = (b: Bin) => (b.to === Infinity ? `≥${b.from}` : `<${b.to}`)
  return (
    <Frame max={max} yLabel={`Histogram in ${unit}`}>
      {bins.map((b, i) => (
        <g key={i}>
          <rect x={M.l + i * slot + 2} y={y(b.count)} width={slot - 4} height={M.t + ih - y(b.count)} className="bar-a"><title>{`${label(b)} ${unit}: ${b.count}`}</title></rect>
          <text x={M.l + i * slot + slot / 2} y={H - 8} textAnchor="middle" className="tick">{label(b)}</text>
        </g>
      ))}
      {onPick && bins.map((b, i) => (
        <rect key={`hit-${i}`} x={M.l + i * slot} y={M.t} width={slot} height={ih} className="hit" onClick={() => onPick(b)}>
          <title>{`${label(b)} ${unit}: ${b.count} · click to list`}</title>
        </rect>
      ))}
    </Frame>
  )
}

export function Scatter({ points, onPick }: { points: { number: number; size: number; days: number }[]; onPick?: (n: number) => void }) {
  if (points.length === 0) return <p className="muted">No data.</p>
  const maxX = Math.max(1, ...points.map((p) => Math.log10(p.size + 1)))
  const maxY = Math.max(1, ...points.map((p) => p.days))
  const x = (s: number) => M.l + (Math.log10(s + 1) / maxX) * iw
  const y = (d: number) => M.t + ih - (d / maxY) * ih
  return (
    <div>
      <Frame max={Math.ceil(maxY)} yLabel="PR size vs days to merge">
        {points.map((p) => (
          <circle key={p.number} cx={x(p.size)} cy={y(p.days)} r={3} className="dot" onClick={() => onPick?.(p.number)}>
            <title>{`#${p.number}: ${p.size} lines, ${p.days.toFixed(1)} days`}</title>
          </circle>
        ))}
        <text x={W - M.r} y={H - 6} textAnchor="end" className="tick">lines changed (log scale) →</text>
      </Frame>
      <p className="legend">Vertical axis: days to merge.</p>
    </div>
  )
}
