import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

// The API refuses a write whose Origin differs from its Host (localOnly in services/api/internal/server). Vite's short
// proxy form ("/api": "http://...") means changeOrigin: true, which rewrites Host to the API's address while the browser
// keeps Origin as the page's, so every POST and PUT would be refused. changeOrigin must stay false: the API then sees
// the page's own Host (127.0.0.1:<page port>, a loopback name) next to the same Origin.
// Playwright (e2e/ports.ts) points the proxies at its own API block through these variables; unset, the dev defaults apply.
declare const process: { env: Record<string, string | undefined> };
const api ={ target: `http://127.0.0.1:${process.env.VESQIRA_API_PORT ?? 8787}`, changeOrigin: false };
const review = { target: `http://127.0.0.1:${process.env.VESQIRA_REVIEW_PORT ?? 8788}`, changeOrigin: false };
const proxy = { "/api": api, "/review-api": review };

export default defineConfig({
  plugins: [react()],
  // design/tokens.css lives outside the app root and is the only token source; allow only that folder.
  server: { fs: { allow: [".", "../../design"] }, proxy },
  preview: { proxy },
  // CSS is not processed in tests, except the token file, which a test reads as text to hold palette.ts to it
  test: { environment: "jsdom", globals: true, setupFiles: ["./src/test-setup.ts"], css: { include: [/design\/tokens\.css/] } },
});
