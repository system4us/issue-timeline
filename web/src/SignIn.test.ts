import { describe, expect, it } from "vitest"
import { hostOf, methodFor } from "./SignIn"
import type { SignInMethod } from "./api"

const methods: SignInMethod[] = [
  { provider: "github", origin: "github", oauth: true, token: true },
  { provider: "gitlab", origin: "https://gitlab.com", oauth: false, token: true },
  { provider: "redmine", origin: "http://127.0.0.1:3300", oauth: false, token: true },
  { provider: "redmine", origin: "https://work.example.com/redmine", oauth: false, token: true },
]

describe("sign-in for the active repo", () => {
  it("names the host of each kind of repo key", () => {
    expect(hostOf("Sagpic/Sagpic-gestion")).toEqual({ provider: "github", at: "github" })
    expect(hostOf("gitlab:https://gitlab.com/system4us/antifaz")).toEqual({ provider: "gitlab", at: "https://gitlab.com/system4us/antifaz" })
    expect(hostOf("redmine:http://127.0.0.1:3300/backend")).toEqual({ provider: "redmine", at: "http://127.0.0.1:3300/backend" })
    expect(hostOf("jira:https://acme.atlassian.net/PROJ")).toBeNull()
    expect(hostOf("")).toBeNull()
  })

  it("picks the method of the repo's own host, a Redmine under a path included", () => {
    expect(methodFor("o/r", methods)?.provider).toBe("github")
    expect(methodFor("gitlab:https://gitlab.com/g/p", methods)?.origin).toBe("https://gitlab.com")
    expect(methodFor("redmine:http://127.0.0.1:3300/backend", methods)?.origin).toBe("http://127.0.0.1:3300")
    expect(methodFor("redmine:https://work.example.com/redmine/ops", methods)?.origin).toBe("https://work.example.com/redmine")
  })

  it("offers nothing for a host the server cannot sign into, or one that only shares a prefix", () => {
    expect(methodFor("jira:https://acme.atlassian.net/PROJ", methods)).toBeNull()
    expect(methodFor("gitlab:https://gitlab.example.com/g/p", methods)).toBeNull()
    expect(methodFor("redmine:http://127.0.0.1:33000/x", methods)).toBeNull()
    expect(methodFor("o/r", [])).toBeNull()
  })
})
