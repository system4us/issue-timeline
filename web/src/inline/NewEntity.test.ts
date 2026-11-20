import { describe, expect, test } from "vitest"
import { entityKinds, entityOption, withCreated } from "./NewEntity"
import type { EditOptions } from "../api"

const base: EditOptions = {
  trackers: [], priorities: [], members: [], labels: [{ name: "bug", color: "d73a4a" }], milestones: [{ id: 1, name: "v1" }],
  versions: [{ id: 3, name: "1.0" }], categories: [], customFields: [], relationTypes: [],
}

describe("what can be created", () => {
  test("labels and milestones on GitHub and GitLab, versions and categories on Redmine", () => {
    expect(entityKinds({ host: "github", pull: false })).toEqual(["label", "milestone"])
    expect(entityKinds({ host: "gitlab", pull: false })).toEqual(["label", "milestone"])
    expect(entityKinds({ host: "redmine", pull: false })).toEqual(["version", "category"])
  })
  test("a label is chosen by name (without the # GitLab adds), the others by id", () => {
    expect(entityOption("label", { name: "p1", color: "#0e8a16" })).toEqual({ key: "p1", label: "p1", value: "p1", color: "0e8a16" })
    expect(entityOption("milestone", { id: 7, name: "v2" })).toEqual({ key: "7", label: "v2", value: 7 })
  })
})

describe("withCreated", () => {
  test("adds each kind to its own list and leaves the rest", () => {
    expect(withCreated(base, "label", { name: "p1", color: "#abc123" }).labels).toEqual([{ name: "bug", color: "d73a4a" }, { name: "p1", color: "abc123" }])
    expect(withCreated(base, "milestone", { id: 2, name: "v2" }).milestones).toEqual([{ id: 1, name: "v1" }, { id: 2, name: "v2" }])
    expect(withCreated(base, "version", { id: 4, name: "2.0" }).versions).toHaveLength(2)
    expect(withCreated(base, "category", { id: 9, name: "API" }).categories).toEqual([{ id: 9, name: "API" }])
    expect(withCreated(base, "label", { name: "p1" }).milestones).toBe(base.milestones)
  })
  test("a label with the same name replaces the old one instead of repeating", () => {
    expect(withCreated(base, "label", { name: "bug", color: "ffffff" }).labels).toEqual([{ name: "bug", color: "ffffff" }])
  })
})
