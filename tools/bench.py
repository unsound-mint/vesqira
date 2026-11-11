#!/usr/bin/env python3
"""Worker benchmark: cold and warm timings and memory on a large sparse cube.

Drives services/scientific-worker/plane_worker.py over its stdin/stdout protocol, the same way the API does, so the
numbers are the worker's own cost without HTTP. The cube is a sparse file: its logical size is large, a few channels
hold random data and the rest are holes, so it measures access pattern and software overhead, not a disk. State that
limit whenever quoting a number from it.

  python3 tools/bench.py                  # print a table
  python3 tools/bench.py --check          # also fail when a budget in tools/bench_budgets.json is exceeded
  python3 tools/bench.py --json out.json  # keep the numbers
"""
import argparse
import json
import os
import statistics
import subprocess
import sys
import tempfile
import time

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
WORKER = os.path.join(ROOT, "services", "scientific-worker", "plane_worker.py")
BUDGETS = os.path.join(ROOT, "tools", "bench_budgets.json")


def make_cube(path: str, nx: int, ny: int, nc: int) -> None:
    import numpy as np
    from astropy.io import fits
    h = fits.Header()
    for k, v in [("SIMPLE", True), ("BITPIX", -32), ("NAXIS", 3), ("NAXIS1", nx), ("NAXIS2", ny), ("NAXIS3", nc),
                 ("CTYPE1", "RA---SIN"), ("CTYPE2", "DEC--SIN"), ("CTYPE3", "VRAD"), ("CUNIT3", "m/s"),
                 ("CRVAL1", 83.0), ("CRVAL2", -5.0), ("CRVAL3", 0.0), ("CRPIX1", nx / 2), ("CRPIX2", ny / 2), ("CRPIX3", 1),
                 ("CDELT1", -0.001), ("CDELT2", 0.001), ("CDELT3", 1000.0), ("BUNIT", "K")]:
        h[k] = v
    head = h.tostring(padding=True).encode()
    rng = np.random.default_rng(1)
    with open(path, "wb") as f:
        f.write(head)
        f.truncate(-(-(len(head) + nx * ny * nc * 4) // 2880) * 2880)
        for c in (0, nc // 2, nc - 1):
            f.seek(len(head) + c * nx * ny * 4)
            f.write(rng.random((ny, nx), dtype=np.float32).astype(">f4").tobytes())


class Worker:
    def __init__(self) -> None:
        self.p = subprocess.Popen([sys.executable, "-P", WORKER], stdin=subprocess.PIPE, stdout=subprocess.PIPE)

    def ask(self, **req) -> tuple[float, dict]:
        t = time.perf_counter()
        self.p.stdin.write((json.dumps(req) + "\n").encode())
        self.p.stdin.flush()
        head = json.loads(self.p.stdout.readline())
        self.p.stdout.read(head.get("nbytes", 0))
        return (time.perf_counter() - t) * 1000.0, head

    def rss_mb(self) -> int:
        with open(f"/proc/{self.p.pid}/status") as f:
            return int(f.read().split("VmRSS:")[1].split()[0]) // 1024

    def close(self) -> None:
        self.p.stdin.close()
        self.p.wait(timeout=10)


def p95(xs: list[float]) -> float:
    s = sorted(xs)
    return s[min(len(s) - 1, int(round(0.95 * (len(s) - 1))))]


def run(path: str, nx: int, ny: int, nc: int, repeats: int) -> dict:
    w = Worker()
    out: dict = {"cube": {"nx": nx, "ny": ny, "channels": nc, "logical_gib": nx * ny * nc * 4 / 2**30}, "ops": {}}
    mid = nc // 2
    c = min(nx, ny) // 2
    side = min(nx, ny) // 8                       # a region 1/8 of the shorter side: a typical hand-drawn box
    scenarios = {
        "view": dict(op="view", path=path),
        "plane": [dict(op="plane", path=path, channel=ch) for ch in (0, mid, mid + 1, nc - 1, mid - 1)],
        "coord": dict(op="coord", path=path, channel=mid, x=10, y=10),
        "spectrum_small": dict(op="spectrum", path=path, shape="rect", x0=c, y0=c, x1=c + 9, y1=c + 9),
        "spectrum_box": dict(op="spectrum", path=path, shape="rect", x0=c, y0=c, x1=c + side - 1, y1=c + side - 1),
    }
    for name, req in scenarios.items():
        reqs = req if isinstance(req, list) else [req]
        times: list[float] = []
        err = None
        for i in range(repeats if not isinstance(req, list) else 1):
            for r in reqs:
                ms, head = w.ask(**r)
                if not head.get("ok"):
                    err = head.get("error") or head.get("code") or "failed"
                    break
                times.append(ms)
            if err:
                break
        entry: dict = {"rss_mb": w.rss_mb()}
        if err:
            entry["error"] = err
        else:
            entry.update(first_ms=round(times[0], 1), median_ms=round(statistics.median(times), 1), p95_ms=round(p95(times), 1), n=len(times))
        out["ops"][name] = entry
    w.close()
    return out


def load_bench(path: str, nx: int, ny: int, nc: int, repeats: int) -> dict:
    """Planes on their own worker process while a heavy worker (large-region spectra, back to back) and a background worker
    (header reads, back to back) are busy: the workload classes of ADR 0055, each with its own process. What it measures is
    what separate processes do not isolate: CPU, memory bandwidth and the page cache."""
    import threading
    inter, heavy, back = Worker(), Worker(), Worker()
    mid, c = nc // 2, min(nx, ny) // 2
    side = min(nx, ny) // 4
    stop = threading.Event()
    served = {"heavy": 0, "background": 0}

    def flood(w: Worker, key: str, **req) -> None:
        while not stop.is_set():
            w.ask(**req)
            served[key] += 1

    threads = [
        threading.Thread(target=flood, args=(heavy, "heavy"), kwargs=dict(op="spectrum", path=path, shape="rect", x0=c, y0=c, x1=c + side - 1, y1=c + side - 1)),
        threading.Thread(target=flood, args=(back, "background"), kwargs=dict(op="describe", path=path)),
    ]
    for t in threads:
        t.start()
    time.sleep(0.5)
    times: list[float] = []
    err = None
    for i in range(max(repeats, 4)):
        for ch in (0, mid, mid + 1, nc - 1, mid - 1):
            ms, head = inter.ask(op="plane", path=path, channel=ch)
            if not head.get("ok"):
                err = head.get("error") or head.get("code") or "failed"
            times.append(ms)
    stop.set()
    for t in threads:
        t.join()
    entry: dict = {"rss_mb": inter.rss_mb()}
    if err:
        entry["error"] = err
    else:
        entry.update(first_ms=round(times[0], 1), median_ms=round(statistics.median(times), 1), p95_ms=round(p95(times), 1), n=len(times),
                     heavy_served=served["heavy"], background_served=served["background"])
    for w in (inter, heavy, back):
        w.close()
    return entry


HEADER_SCRIPT = os.path.join(ROOT, "services", "scientific-worker", "fits_header.py")


def register_bench(directory: str, files: int, baseline: int) -> tuple[dict, dict]:
    """Header registration: the persistent worker's `describe` op over many small files (what a project scan does),
    against the previous path of one `fits_header.py` process per file, timed on a sample of the same files."""
    import numpy as np
    from astropy.io import fits
    paths = []
    for i in range(files):
        p = os.path.join(directory, f"f{i:04d}.fits")
        h = fits.PrimaryHDU(np.zeros((4, 4, 2 + i % 5), dtype=np.float32))
        h.header.update({"CTYPE1": "RA---SIN", "CTYPE2": "DEC--SIN", "CTYPE3": "VRAD", "CRVAL3": float(i), "BUNIT": "K", "BMAJ": 0.01})
        h.writeto(p)
        paths.append(p)
    w = Worker()
    w.ask(op="ping")                                   # warm: Python and astropy are loaded
    times = []
    for p in paths:
        ms, head = w.ask(op="describe", path=p)
        if not head.get("ok"):
            raise SystemExit(f"describe failed on {p}: {head}")
        times.append(ms)
    rss = w.rss_mb()
    w.close()
    old = []
    for p in paths[:baseline]:
        t = time.perf_counter()
        subprocess.run([sys.executable, "-P", HEADER_SCRIPT, p], capture_output=True, check=True)
        old.append((time.perf_counter() - t) * 1000.0)
    new_e = {"first_ms": round(times[0], 1), "median_ms": round(statistics.median(times), 1), "p95_ms": round(p95(times), 1),
             "n": len(times), "rss_mb": rss}
    old_e = {"median_ms": round(statistics.median(old), 1), "n": len(old)}
    return new_e, old_e


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--size", default="1500x1500x600", help="NXxNYxNC of the sparse cube")
    ap.add_argument("--register-files", type=int, default=300, help="small files for the registration benchmark")
    ap.add_argument("--repeats", type=int, default=5)
    ap.add_argument("--dir", default=None, help="where to put the cube (default: a temporary directory)")
    ap.add_argument("--check", action="store_true", help="fail when a budget is exceeded")
    ap.add_argument("--json", default=None)
    a = ap.parse_args()
    nx, ny, nc = (int(v) for v in a.size.split("x"))
    with tempfile.TemporaryDirectory(dir=a.dir) as d:
        path = os.path.realpath(os.path.join(d, "bench.fits"))
        make_cube(path, nx, ny, nc)
        res = run(path, nx, ny, nc, a.repeats)
        res["ops"]["register"], res["register_one_process_per_file"] = register_bench(d, a.register_files, 20)
        res["ops"]["plane_under_load"] = load_bench(path, nx, ny, nc, a.repeats)
    print(f"sparse cube {nx}x{ny}x{nc} ({res['cube']['logical_gib']:.1f} GiB logical); page-cache speed, not disk")
    print(f"{'op':18s} {'first ms':>9s} {'median':>8s} {'p95':>8s} {'rss MB':>7s}")
    for name, e in res["ops"].items():
        if "error" in e:
            print(f"{name:18s} {'refused: ' + str(e['error']):>34s} {e['rss_mb']:7d}")
        else:
            print(f"{name:18s} {e['first_ms']:9.1f} {e['median_ms']:8.1f} {e['p95_ms']:8.1f} {e['rss_mb']:7d}")
    pl = res["ops"]["plane_under_load"]
    if "error" not in pl:
        print(f"plane_under_load: planes while the heavy worker served {pl['heavy_served']} spectra and the background worker {pl['background_served']} header reads")
    old = res["register_one_process_per_file"]
    print(f"register: {res['ops']['register']['n']} small files per request through the persistent worker; "
          f"previously one process per file: median {old['median_ms']} ms (n={old['n']})")
    if a.json:
        with open(a.json, "w") as f:
            json.dump(res, f, indent=1)
    if not a.check:
        return 0
    with open(BUDGETS) as f:
        budgets = json.load(f)
    bad = []
    for name, b in budgets["ops"].items():
        e = res["ops"].get(name)
        if e is None:
            bad.append(f"{name}: not measured")
        elif "error" in e:
            bad.append(f"{name}: {e['error']}")
        else:
            if e["p95_ms"] > b["p95_ms"]:
                bad.append(f"{name}: p95 {e['p95_ms']} ms over the budget {b['p95_ms']} ms")
            if e["rss_mb"] > b["rss_mb"]:
                bad.append(f"{name}: worker memory {e['rss_mb']} MB over the budget {b['rss_mb']} MB")
    if bad:
        print("BUDGET EXCEEDED:\n  " + "\n  ".join(bad))
        return 1
    print("all budgets met")
    return 0


if __name__ == "__main__":
    sys.exit(main())
