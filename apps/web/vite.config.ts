import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

export default defineConfig({
  plugins: [react()],
  // design/tokens.css lives outside the app root and is the only token source; allow only that folder.
  server: { fs: { allow: [".", "../../design"] }, proxy: { "/api": "http://127.0.0.1:8787", "/review-api": "http://127.0.0.1:8788" } },
  test: { environment: "jsdom", globals: true, setupFiles: ["./src/test-setup.ts"], css: false },
});
