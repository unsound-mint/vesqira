import { expect, test as base } from "@playwright/test";
import { type ChildProcess, spawn } from "node:child_process";
import { cpSync, openSync, rmSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { API, REVIEW } from "./ports";

// Every spec file gets its own API: a fresh copy of the prepared instance (prepare.sh, copied to .run-template by
// global-setup.ts), a new database, and a restart on the same ports. Specs can therefore run in any order, alone or
// together, and none leaves anything behind for another. Import `test` from here instead of "@playwright/test".
const e2e = path.dirname(fileURLToPath(import.meta.url));
const run = path.join(e2e, ".run");
const template = path.join(e2e, ".run-template");
const webDist = path.resolve(e2e, "../apps/web/dist");

class Instance {
  private child?: ChildProcess;
  private file?: string;

  async resetFor(file: string) {
    if (this.file === file && this.child) return;
    await this.stop();
    rmSync(run, { recursive: true, force: true });
    cpSync(template, run, { recursive: true, verbatimSymlinks: true });
    this.file = file;
    const log = openSync(path.join(run, "api.log"), "a");
    // detached: the API starts ssh and worker processes; stop() ends the whole group
    this.child = spawn(
      path.join(run, "api"),
      [
        "--ssh", path.join(run, "fake-ssh"), "--addr", new URL(API).host, "--db", path.join(run, "t.db"), "--data-dir", run,
        "--root", path.join(run, "data"), "--review-addr", new URL(REVIEW).host, "--web-dist", webDist, "--ssh-config", "",
        "--enable-terminal", "--enable-vpn",
        "--run-profiles", path.resolve(e2e, "../services/api/internal/runs/testdata/synthetic_maps_profile.json"),
      ],
      { cwd: path.resolve(e2e, ".."), detached: true, stdio: ["ignore", log, log] },
    );
    await this.ready(this.child);
  }

  private async ready(child: ChildProcess) {
    let exited: number | null | undefined;
    child.once("exit", (code) => (exited = code ?? -1));
    for (let i = 0; i < 300; i++) {
      if (exited !== undefined) throw new Error(`the API exited with ${exited} while starting; see ${path.join(run, "api.log")}`);
      try {
        if ((await fetch(`${API}/api/roots`)).ok) return;
      } catch {
        // not listening yet
      }
      await new Promise((r) => setTimeout(r, 50));
    }
    throw new Error("the API did not answer in 15 s");
  }

  async stop() {
    const child = this.child;
    this.child = undefined;
    if (!child?.pid) return;
    const pid = -child.pid;
    const gone = new Promise<void>((resolve) => (child.exitCode !== null ? resolve() : child.once("exit", () => resolve())));
    signal(pid, "SIGTERM");
    const timer = setTimeout(() => signal(pid, "SIGKILL"), 5000);
    await gone;
    clearTimeout(timer);
    signal(pid, "SIGKILL"); // whatever the API left running in its group
  }
}

function signal(pid: number, sig: NodeJS.Signals) {
  try {
    process.kill(pid, sig);
  } catch {
    // already gone
  }
}

export const test = base.extend<{ freshInstance: void }, { instance: Instance }>({
  instance: [
    async ({}, use) => {
      const instance = new Instance();
      await use(instance);
      await instance.stop();
    },
    { scope: "worker" },
  ],
  freshInstance: [
    async ({ instance }, use, testInfo) => {
      await instance.resetFor(testInfo.file);
      await use();
    },
    { auto: true },
  ],
});
export { expect };
