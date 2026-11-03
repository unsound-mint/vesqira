import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

export default defineConfig({
  plugins: [react()],
  // design/tokens.css lives outside the app root and is the only token source; allow only that folder.
  server: { fs: { allow: [".", "../../design"] } },
  test: { environment: "jsdom", globals: true, setupFiles: ["./src/test-setup.ts"], css: false },
});
