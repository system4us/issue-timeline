import { defineConfig } from "vite"
import react from "@vitejs/plugin-react"

// In dev the page is served by Vite (:5173) and /api goes to the issue-timeline server.
export default defineConfig({
  plugins: [react()],
  server: { port: 5173, proxy: { "/api": process.env.API_URL ?? "http://127.0.0.1:4310" } },
  test: { environment: "node" },
})
