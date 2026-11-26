import { execFileSync } from "node:child_process";
import { cpSync, rmSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

// Builds the API and the fixtures once (prepare.sh) and keeps the result as .run-template, which fixtures.ts restores for each spec file.
export default function globalSetup() {
  const e2e = path.dirname(fileURLToPath(import.meta.url));
  execFileSync("bash", [path.join(e2e, "prepare.sh")], { stdio: "inherit" });
  rmSync(path.join(e2e, ".run-template"), { recursive: true, force: true });
  cpSync(path.join(e2e, ".run"), path.join(e2e, ".run-template"), { recursive: true, verbatimSymlinks: true });
}
