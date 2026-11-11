// The only definition of the e2e ports. VESQIRA_E2E_PORT_BASE moves the whole block so several worktrees can run e2e at once
// (`make e2e-isolated` picks a free base); the default base keeps the historical ports (API 8787, 8788, 8790, Vite 5198, 5199).
const base = Number(process.env.VESQIRA_E2E_PORT_BASE ?? 8787);
if (!Number.isInteger(base) || base < 4000 || base > 65000) throw new Error(`VESQIRA_E2E_PORT_BASE out of range: ${process.env.VESQIRA_E2E_PORT_BASE}`);

export const PORTS = { api: base, review: base + 1, publish: base + 3, dev: base - 3589, preview: base - 3588 };
export const origin = (port: number) => `http://127.0.0.1:${port}`;
export const API = origin(PORTS.api);
export const REVIEW = origin(PORTS.review);
export const PUBLISH = origin(PORTS.publish);
export const DEV = origin(PORTS.dev);
export const PREVIEW = origin(PORTS.preview);
