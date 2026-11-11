import { defineConfig } from "@playwright/test";
import { API, DEV, PORTS, PREVIEW, PUBLISH, REVIEW } from "./ports";

// The acceptance walk-through runs against the real API, the real Python worker and the built web app, on
// generated fixtures only (nothing committed, nothing from anyone's research data).
const env = {
  VESQIRA_API_PORT: String(PORTS.api),
  VESQIRA_REVIEW_PORT: String(PORTS.review),
  VESQIRA_PUBLISH_PORT: String(PORTS.publish),
};
export default defineConfig({
  testDir: "./tests",
  timeout: 180_000,
  expect: { timeout: 15_000 },
  workers: 1,
  retries: process.env.CI ? 1 : 0,
  reporter: [["list"]],
  use: {
    baseURL: PREVIEW,
    // the app defaults its review address to 127.0.0.1:8788 (BoardsView); point it at this run's reviewer listener
    storageState: { cookies: [], origins: [{ origin: PREVIEW, localStorage: [{ name: "vesqira.reviewBase", value: REVIEW }] }] },
    colorScheme: "dark",
    viewport: { width: 1440, height: 900 },
    trace: "retain-on-failure",
  },
  webServer: [
    { command: "bash ./start-api.sh", env, url: `${API}/api/roots`, timeout: 240_000, reuseExistingServer: false },
    { command: "bash ./start-publish.sh", env, url: `${PUBLISH}/publish-api/ping`, timeout: 240_000, reuseExistingServer: false },
    // the renderer equivalence test imports source modules into a real browser, which only the dev server serves
    { command: `npx vite --port ${PORTS.dev} --strictPort --host 127.0.0.1`, env, cwd: "../apps/web", url: DEV, timeout: 60_000, reuseExistingServer: false },
    { command: `npx vite preview --port ${PORTS.preview} --strictPort --host 127.0.0.1`, env, cwd: "../apps/web", url: PREVIEW, timeout: 60_000, reuseExistingServer: false },
  ],
});
