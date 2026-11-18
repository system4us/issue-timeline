export interface Span { id: string; start: number; end: number }

/**
 * Greedy interval packing: each span goes in the first lane whose last span ended at least `gap` before it starts.
 * Returns the lane of every id and the number of lanes. Spans are placed in start order, so the result is stable.
 */
export function packLanes(spans: Span[], gap = 4): { lane: Map<string, number>; lanes: number } {
  const sorted = [...spans].sort((a, b) => a.start - b.start || a.end - b.end)
  const laneEnds: number[] = []
  const lane = new Map<string, number>()
  for (const s of sorted) {
    let i = laneEnds.findIndex((end) => end + gap <= s.start)
    if (i === -1) {
      i = laneEnds.length
      laneEnds.push(s.end)
    } else {
      laneEnds[i] = s.end
    }
    lane.set(s.id, i)
  }
  return { lane, lanes: laneEnds.length }
}
