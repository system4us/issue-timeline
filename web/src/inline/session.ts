import { useEffect, useRef, useState } from "react"
import { editIssue, editPull, fetchEditOptions, fetchIssueRecord, type EditOptions } from "../api"
import type { Body } from "./changes"

export interface Saved { warnings?: string[]; [k: string]: unknown }

export interface EditSession {
  /** The record as the store keeps it (body, relations, watchers, attachments, custom fields), or null while it loads. */
  rec: any | null
  /** The lists the controls offer (members, labels, milestones, trackers...), or null while they load. */
  opts: EditOptions | null
  loadError: string | null
  /** Sends one PATCH with only `changes`; then the record is read again and `onSaved` gets what the host answered. */
  save: (changes: Body) => Promise<Saved>
}

/**
 * The record and the lists an open panel edits with. Both are read when the panel opens for a writable repo (the
 * lists give the names the panel shows), and kept for the panel's life; each save reads the record again.
 */
export function useEditSession({ repo, key, pull, enabled, onSaved }: {
  repo: string; key: string; pull: boolean; enabled: boolean; onSaved: (out: Saved) => void
}): EditSession {
  const [rec, setRec] = useState<any | null>(null)
  const [opts, setOpts] = useState<EditOptions | null>(null)
  const [loadError, setLoadError] = useState<string | null>(null)
  const savedRef = useRef(onSaved)
  savedRef.current = onSaved

  useEffect(() => {
    setRec(null)
    setOpts(null)
    setLoadError(null)
    if (!enabled) return
    let live = true
    Promise.all([fetchEditOptions(repo), fetchIssueRecord(repo, key)]).then(
      ([o, r]) => { if (live) { setOpts(o); setRec(r) } },
      (e) => { if (live) setLoadError(String((e as Error).message ?? e)) },
    )
    return () => { live = false }
  }, [repo, key, enabled])

  const save = async (changes: Body): Promise<Saved> => {
    const out: Saved = pull ? await editPull(repo, key, changes) : await editIssue(repo, key, changes)
    try {
      setRec(await fetchIssueRecord(repo, key))
    } catch {
      // the record stays as it was; the next open reads it again
    }
    savedRef.current(out)
    return out
  }

  return { rec, opts, loadError, save }
}
