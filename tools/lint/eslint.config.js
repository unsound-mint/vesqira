// Run from the repository root (npm run lint here does that). Its own npm project because typescript-eslint cannot use TypeScript 7,
// which apps/web builds with; the TypeScript 5 here only parses. Formatting is not ESLint's job in this repo.
// Type-aware rules are limited to the promise rules: the recommended type-checked set adds the no-unsafe-* family,
// which flags every JSON and DOM boundary without finding bugs.
import { fileURLToPath } from "node:url";
import js from "@eslint/js";
import globals from "globals";
import reactHooks from "eslint-plugin-react-hooks";
import tseslint from "typescript-eslint";

const root = fileURLToPath(new URL("../..", import.meta.url));

export default tseslint.config(
  { ignores: ["**/node_modules/", "**/dist/", "apps/web/src/api/schema.d.ts"] },
  js.configs.recommended,
  tseslint.configs.recommended,
  { linterOptions: { reportUnusedDisableDirectives: "error" } },
  {
    // The same convention tsc's noUnusedLocals and noUnusedParameters use: a leading underscore marks it as deliberate.
    rules: {
      "@typescript-eslint/no-unused-vars": [
        "error",
        { argsIgnorePattern: "^_", varsIgnorePattern: "^_", destructuredArrayIgnorePattern: "^_", caughtErrorsIgnorePattern: "^_", ignoreRestSiblings: true },
      ],
    },
  },
  {
    files: ["apps/web/**/*.{ts,tsx}"],
    languageOptions: {
      globals: globals.browser,
      parserOptions: { project: "apps/web/tsconfig.json", tsconfigRootDir: root },
    },
    plugins: { "react-hooks": reactHooks },
    rules: {
      "react-hooks/rules-of-hooks": "error",
      "react-hooks/exhaustive-deps": "error",
      // The compiler rules (refs, set-state-in-effect, purity, preserve-manual-memoization, immutability, globals) are not enabled:
      // the app reads refs in render and sets state in effects by design, and plugin 7.1.1 reports about 170 sites for them.
      "@typescript-eslint/await-thenable": "error",
      "@typescript-eslint/no-floating-promises": "error",
      // An async handler on a JSX prop is the normal React shape; the handlers here catch into component state.
      // Promises in conditions and in void callbacks (forEach, addEventListener) are still flagged.
      "@typescript-eslint/no-misused-promises": ["error", { checksVoidReturn: { attributes: false } }],
    },
  },
  {
    // Tests capture request bodies as parsed JSON and read them by path; a type there would only be a cast.
    files: ["**/*.test.{ts,tsx}"],
    rules: { "@typescript-eslint/no-explicit-any": "off" },
  },
  {
    files: ["e2e/**/*.ts", "packages/**/*.mjs"],
    languageOptions: { globals: globals.node },
    // Playwright fixtures must destructure their first argument, even when empty, to know which fixtures to set up.
    rules: { "no-empty-pattern": ["error", { allowObjectPatternsAsParameters: true }] },
  },
);
