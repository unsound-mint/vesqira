import { defineConfig } from "@playwright/test";

// The acceptance walk-through runs against the real API, the real Python worker and the built web app, on
// generated fixtures only (nothing committed, nothing from anyone's research data).
export default defineConfig({
  testDir: "./tests",
  timeout: 180_000,
  expect: { timeout: 15_000 },
  workers: 1,
  retries: process.env.CI ? 1 : 0,
  reporter: [["list"]],
  use: {
    baseURL: "http://127.0.0.1:5199",
    colorScheme: "dark",
    viewport: { width: 1440, height: 900 },
    trace: "retain-on-failure",
  },
  webServer: [
    { command: "bash ./start-api.sh", url: "http://127.0.0.1:8787/api/roots", timeout: 240_000, reuseExistingServer: false },
    { command: "npx vite preview --port 5199 --strictPort --host 127.0.0.1", cwd: "../apps/web", url: "http://127.0.0.1:5199", timeout: 60_000, reuseExistingServer: false },
  ],
});
