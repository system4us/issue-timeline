// @vitest-environment jsdom
import { afterEach, describe, expect, test, vi } from "vitest"
import { createRoot, type Root } from "react-dom/client"
import { act, useState } from "react"
import { RepoPicker } from "./RepoSwitcher"

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

describe("RepoPicker", () => {
  let root: Root | null = null
  let host: HTMLElement | null = null

  afterEach(() => {
    act(() => root?.unmount())
    host?.remove()
    vi.restoreAllMocks()
  })

  test("lists available repos, disables already added ones, filters, and adds on click", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({
        repos: [
          { provider: "gitlab", origin: "https://gitlab.com", repo: "gitlab:https://gitlab.com/g/a", name: "g/a", private: true, added: false },
          { provider: "gitlab", origin: "https://gitlab.com", repo: "gitlab:https://gitlab.com/g/b", name: "g/b", private: false, added: true },
        ],
      }),
    })
    vi.stubGlobal("fetch", fetchMock)

    const picked: string[] = []
    function Harness() {
      const [val, setVal] = useState("")
      return <RepoPicker provider="gitlab" value={val} onChange={setVal} onPickAdd={(r) => picked.push(r)} />
    }

    host = document.createElement("div")
    document.body.appendChild(host)
    root = createRoot(host)
    await act(async () => {
      root!.render(<Harness />)
    })

    expect(fetchMock).toHaveBeenCalledWith("/api/repos/available?provider=gitlab")
    const options = [...host.querySelectorAll<HTMLButtonElement>('[role="option"]')]
    expect(options.map((o) => o.textContent)).toEqual(["g/aprivateAdd", "g/bAdded"])
    expect(options[0].disabled).toBe(false)
    expect(options[1].disabled).toBe(true)

    await act(async () => {
      options[0].click()
    })
    expect(picked).toEqual(["gitlab:https://gitlab.com/g/a"])
  })
})
