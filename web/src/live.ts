import { useEffect, useRef, useState } from "react"
import type { components } from "./api/schema"

/** What changed in a repo, as GET /api/events sends it (an edit here, a sync, or a webhook that made the server read). */
export type ChangeEvent = components["schemas"]["ChangeEvent"]

/** How long a burst of events (a sync start then its end, several webhook deliveries) waits to become one reload. */
export const BURST_MS = 250

/**
 * Follows the repo's events and calls `onChange` once per burst, with the events of the burst. It also calls it when the
 * stream comes back after a drop, since what changed meanwhile was not heard. `live` is true while the stream is open:
 * the page can stop polling then. Without EventSource (an old browser, a test) it stays false and the page polls.
 */
export function useLiveEvents(repo: string, onChange: (events: ChangeEvent[]) => void): { live: boolean } {
  const [live, setLive] = useState(false)
  const cb = useRef(onChange)
  cb.current = onChange
  useEffect(() => {
    if (!repo || typeof EventSource === "undefined") return
    const es = new EventSource(`/api/events?repo=${encodeURIComponent(repo)}`)
    let pending: ChangeEvent[] = []
    let timer: ReturnType<typeof setTimeout> | null = null
    let opened = false
    const flush = () => { timer = null; const batch = pending; pending = []; cb.current(batch) }
    const queue = (e: ChangeEvent | null) => {
      if (e) pending.push(e)
      if (!timer) timer = setTimeout(flush, BURST_MS)
    }
    es.onopen = () => {
      setLive(true)
      // a reconnection: what changed while the stream was down is read now
      if (opened) queue(null)
      opened = true
    }
    es.onerror = () => setLive(false) // EventSource reconnects on its own (the server says retry: 3000)
    es.addEventListener("change", (m) => {
      try { queue(JSON.parse((m as MessageEvent<string>).data) as ChangeEvent) } catch { /* not ours: skip it */ }
    })
    return () => { es.close(); if (timer) clearTimeout(timer); setLive(false) }
  }, [repo])
  return { live }
}
