// Fails when apps/web/src/api/schema.d.ts is not what openapi-typescript generates from openapi.yaml today.
import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const committed = join(import.meta.dirname, "..", "..", "apps", "web", "src", "api", "schema.d.ts");
const dir = mkdtempSync(join(tmpdir(), "vesqira-contracts-"));
try {
  const fresh = join(dir, "schema.d.ts");
  execFileSync("npx", ["openapi-typescript", "openapi.yaml", "-o", fresh], { cwd: import.meta.dirname, stdio: "ignore" });
  if (readFileSync(fresh, "utf8") !== readFileSync(committed, "utf8")) {
    console.error("apps/web/src/api/schema.d.ts is out of date with packages/contracts/openapi.yaml: run `npm run gen:api` in apps/web");
    process.exit(1);
  }
} finally {
  rmSync(dir, { recursive: true, force: true });
}
