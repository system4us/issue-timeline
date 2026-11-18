import { useEffect, useLayoutEffect, useRef, useState } from "react"

export type MenuEntry = { kind: "item"; label: string; href?: string; run?: () => void } | { kind: "sep" }

interface Props {
  x: number
  y: number
  entries: MenuEntry[]
  onClose: () => void
}

/** A small menu at the pointer. It closes on a click outside, Escape, a scroll or a resize, and stays inside the window. */
export function ContextMenu({ x, y, entries, onClose }: Props) {
  const box = useRef<HTMLDivElement>(null)
  const [pos, setPos] = useState({ left: x, top: y })

  useLayoutEffect(() => {
    const el = box.current
    if (!el) return
    const { width, height } = el.getBoundingClientRect()
    setPos({
      left: Math.max(4, Math.min(x, window.innerWidth - width - 4)),
      top: Math.max(4, Math.min(y, window.innerHeight - height - 4)),
    })
  }, [x, y])

  useEffect(() => {
    const onDown = (e: MouseEvent) => { if (!box.current?.contains(e.target as Node)) onClose() }
    const onKey = (e: KeyboardEvent) => { if (e.key === "Escape") onClose() }
    document.addEventListener("mousedown", onDown)
    window.addEventListener("keydown", onKey)
    window.addEventListener("resize", onClose)
    window.addEventListener("scroll", onClose, true)
    return () => {
      document.removeEventListener("mousedown", onDown)
      window.removeEventListener("keydown", onKey)
      window.removeEventListener("resize", onClose)
      window.removeEventListener("scroll", onClose, true)
    }
  }, [onClose])

  return (
    <div
      ref={box}
      className="ctx-menu"
      role="menu"
      style={{ left: pos.left, top: pos.top }}
      // the menu sits inside the timeline, whose own clicks and drags must not see what happens here
      onClick={(e) => e.stopPropagation()}
      onPointerDown={(e) => e.stopPropagation()}
      onContextMenu={(e) => { e.preventDefault(); e.stopPropagation() }}
    >
      {entries.map((e, k) => {
        if (e.kind === "sep") return <hr key={k} />
        if (e.href) return <a key={k} role="menuitem" href={e.href} target="_blank" rel="noreferrer" onClick={onClose}>{e.label}</a>
        return <button key={k} role="menuitem" onClick={() => { e.run?.(); onClose() }}>{e.label}</button>
      })}
    </div>
  )
}
