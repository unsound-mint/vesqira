export type Status = "declared" | "unresolved";

export type Axis = {
  axis: number;
  length: number;
  ctype: string | null;
  ctype_status: Status;
  cunit: string | null;
  cunit_status: Status;
  crval: number | null;
  crval_status: Status;
  crpix: number | null;
  crpix_status: Status;
  cdelt: number | null;
  cdelt_status: Status;
};

export type Header = {
  naxis: number;
  bitpix: number | null;
  axes: Axis[];
  bunit: string | null;
  bunit_status: Status;
  specsys: string | null;
  radesys: string | null;
};

export type Dataset = { id: number; path: string; name: string; sizeBytes: number; modifiedAt: string; addedAt: string; header: Header };

export class ApiError extends Error {}

async function call<T>(url: string, init?: RequestInit): Promise<T> {
  let res: Response;
  try {
    res = await fetch(url, init);
  } catch {
    throw new ApiError("Cannot reach the Vesqira API. Is it running on 127.0.0.1:8787?");
  }
  const body = (await res.json().catch(() => null)) as { error?: string } | null;
  if (!res.ok) throw new ApiError(body?.error ?? `Request failed (${res.status})`);
  return body as T;
}

export const listDatasets = () => call<{ datasets: Dataset[] }>("/api/datasets").then((r) => r.datasets);
export const listRoots = () => call<{ roots: string[] }>("/api/roots").then((r) => r.roots);
export const registerDataset = (path: string) =>
  call<Dataset>("/api/datasets", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ path }) });
