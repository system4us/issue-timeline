#!/usr/bin/env node
// Prints a short-lived installation token for a GitHub App (no dependencies).
// Env: GH_APP_ID, GH_INSTALLATION_ID, and the private key either as
//      GH_APP_PRIVATE_KEY (PEM; literal "\n" sequences are accepted) or GH_APP_PRIVATE_KEY_FILE (path).
import { createPrivateKey, createSign } from "node:crypto";
import { readFile } from "node:fs/promises";

const appId = process.env.GH_APP_ID;
const installationId = process.env.GH_INSTALLATION_ID;
if (!appId || !installationId) {
  console.error("GH_APP_ID and GH_INSTALLATION_ID are required");
  process.exit(1);
}

const pem = process.env.GH_APP_PRIVATE_KEY_FILE
  ? await readFile(process.env.GH_APP_PRIVATE_KEY_FILE, "utf8")
  : (process.env.GH_APP_PRIVATE_KEY ?? "").replace(/\\n/g, "\n");
if (!pem.includes("PRIVATE KEY")) {
  console.error("No private key found in GH_APP_PRIVATE_KEY / GH_APP_PRIVATE_KEY_FILE");
  process.exit(1);
}

const b64url = (buf) => Buffer.from(buf).toString("base64url");
const now = Math.floor(Date.now() / 1000);
const header = b64url(JSON.stringify({ alg: "RS256", typ: "JWT" }));
// iat is backdated 60s for clock skew; GitHub caps the JWT lifetime at 10 minutes.
const payload = b64url(JSON.stringify({ iat: now - 60, exp: now + 540, iss: appId }));
const signer = createSign("RSA-SHA256");
signer.update(`${header}.${payload}`);
const signature = signer.sign(createPrivateKey(pem), "base64url");
const jwt = `${header}.${payload}.${signature}`;

const res = await fetch(`https://api.github.com/app/installations/${installationId}/access_tokens`, {
  method: "POST",
  headers: {
    Authorization: `Bearer ${jwt}`,
    Accept: "application/vnd.github+json",
    "X-GitHub-Api-Version": "2022-11-28",
  },
});
if (!res.ok) {
  console.error(`GitHub returned ${res.status}: ${await res.text()}`);
  process.exit(1);
}
const { token } = await res.json();
process.stdout.write(token);
