#!/usr/bin/env python3
"""Worker benchmark: cold and warm timings and memory on a large sparse cube.

Drives services/scientific-worker/plane_worker.py over its stdin/stdout protocol, the same way the API does, so the
numbers are the worker's own cost without HTTP. The cube is a sparse file: its logical size is large, a few channels
hold random data and the rest are holes, so it measures access pattern and software overhead, not a disk. State that
limit whenever quoting a number from it.

  python3 tools/bench.py                  # print a table
  python3 tools/bench.py --check          # also fail when a budget in tools/bench_budgets.json is exceeded
  python3 tools/bench.py --json out.json  # keep the numbers
  python3 tools/bench.py --real-disk DIR  # also: a dense cube in DIR, read cold (its pages evicted first) and warm
  python3 tools/bench.py --ssh            # also: the same reads through a real ssh to a user-mode sshd on loopback

Rows with a "_4d" suffix read a cube whose FITS header has a fourth, length-1 STOKES axis (what CASA writes). "first" is the first
call, which is the cold one in the --real-disk and --ssh rows; median and p95 are of the calls that follow.
"""
import argparse
import json
import os
import statistics
import subprocess
import sys
import shutil
import socket
import tempfile
import time

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
WORKER_DIR = os.path.join(ROOT, "services", "scientific-worker")
WORKER = os.path.join(WORKER_DIR, "plane_worker.py")
BUDGETS = os.path.join(ROOT, "tools", "bench_budgets.json")


def cube_header(nx: int, ny: int, nc: int, stokes: int | None = None) -> bytes:
    from astropy.io import fits
    h = fits.Header()
    pairs = [("SIMPLE", True), ("BITPIX", -32), ("NAXIS", 3 if stokes is None else 4), ("NAXIS1", nx), ("NAXIS2", ny), ("NAXIS3", nc)]
    if stokes is not None:
        pairs.append(("NAXIS4", stokes))
    pairs += [("CTYPE1", "RA---SIN"), ("CTYPE2", "DEC--SIN"), ("CTYPE3", "VRAD"), ("CUNIT3", "m/s"),
              ("CRVAL1", 83.0), ("CRVAL2", -5.0), ("CRVAL3", 0.0), ("CRPIX1", nx / 2), ("CRPIX2", ny / 2), ("CRPIX3", 1),
              ("CDELT1", -0.001), ("CDELT2", 0.001), ("CDELT3", 1000.0), ("BUNIT", "K")]
    if stokes is not None:
        pairs += [("CTYPE4", "STOKES"), ("CRVAL4", 1.0), ("CRPIX4", 1), ("CDELT4", 1.0)]
    for k, v in pairs:
        h[k] = v
    return h.tostring(padding=True).encode()


def make_cube(path: str, nx: int, ny: int, nc: int, stokes: int | None = None) -> None:
    """A sparse cube: a few channels hold random data, the rest are holes. stokes adds a fourth STOKES axis of that length."""
    import numpy as np
    head = cube_header(nx, ny, nc, stokes)
    rng = np.random.default_rng(1)
    with open(path, "wb") as f:
        f.write(head)
        f.truncate(-(-(len(head) + nx * ny * nc * (stokes or 1) * 4) // 2880) * 2880)
        for c in (0, nc // 2, nc - 1):
            f.seek(len(head) + c * nx * ny * 4)
            f.write(rng.random((ny, nx), dtype=np.float32).astype(">f4").tobytes())


def make_dense_cube(path: str, nx: int, ny: int, nc: int, stokes: int | None = None) -> None:
    """Every channel holds data (one random plane scaled per channel), so reading it really reads the disk."""
    import numpy as np
    head = cube_header(nx, ny, nc, stokes)
    plane = np.random.default_rng(2).random((ny, nx), dtype=np.float32)
    with open(path, "wb") as f:
        f.write(head)
        for c in range(nc * (stokes or 1)):
            f.write((plane * np.float32(1 + c * 1e-3)).astype(">f4").tobytes())
        pad = -f.tell() % 2880
        f.write(b"\0" * pad)
        f.flush()
        os.fsync(f.fileno())


def evict(path: str) -> None:
    """Drop the file's pages from the page cache without root: clean pages go with POSIX_FADV_DONTNEED once written back."""
    fd = os.open(path, os.O_RDONLY)
    try:
        os.posix_fadvise(fd, 0, 0, os.POSIX_FADV_DONTNEED)
    finally:
        os.close(fd)


def cached_fraction(path: str) -> float | None:
    """Share of the file resident in the page cache (util-linux fincore), None when the tool is missing."""
    if shutil.which("fincore") is None:
        return None
    r = subprocess.run(["fincore", "--raw", "--bytes", "--noheadings", "--output", "RES,SIZE", path], capture_output=True, text=True)
    try:
        res, size = (int(v) for v in r.stdout.split()[:2])
    except ValueError:
        return None
    return res / size if size else None


class Worker:
    def __init__(self, cmd: list[str] | None = None, pid_file: str | None = None) -> None:
        self.p = subprocess.Popen(cmd or [sys.executable, "-P", WORKER], stdin=subprocess.PIPE, stdout=subprocess.PIPE)
        self.pid_file = pid_file          # over ssh the worker is another process: the launcher wrote its pid here
        self.sent = self.received = 0     # bytes that crossed the pipe (over ssh: the link)

    def ask(self, **req) -> tuple[float, dict]:
        t = time.perf_counter()
        line = (json.dumps(req) + "\n").encode()
        self.p.stdin.write(line)
        self.p.stdin.flush()
        raw = self.p.stdout.readline()
        head = json.loads(raw)
        n = head.get("nbytes", 0)
        self.p.stdout.read(n)
        ms = (time.perf_counter() - t) * 1000.0
        self.sent += len(line)
        self.received += len(raw) + n
        return ms, head

    def _status(self, key: str) -> int:
        pid = int(open(self.pid_file).read()) if self.pid_file else self.p.pid
        with open(f"/proc/{pid}/status") as f:
            return int(f.read().split(key + ":")[1].split()[0]) // 1024

    def rss_mb(self) -> int:
        return self._status("VmRSS")

    def peak_mb(self) -> int:
        return self._status("VmHWM")

    def close(self) -> None:
        self.p.stdin.close()
        self.p.wait(timeout=10)


class UserSshd:
    """A real ssh to a real sshd, with no root: sshd runs as this user on a loopback port with its own host key and one
    authorised client key, in a temporary directory. It stands in for a host (crypto, channel and process costs; no latency or
    bandwidth limit of a real link)."""

    def __init__(self, directory: str) -> None:
        self.dir = directory
        self.port = None
        self.p = None

    def __enter__(self):
        sshd = shutil.which("sshd") or "/usr/sbin/sshd"
        if not (os.path.exists(sshd) and shutil.which("ssh") and shutil.which("ssh-keygen")):
            raise RuntimeError("ssh, sshd and ssh-keygen are needed")
        for name in ("hostkey", "clientkey"):
            subprocess.run(["ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", os.path.join(self.dir, name)], check=True)
        shutil.copy(os.path.join(self.dir, "clientkey.pub"), os.path.join(self.dir, "authorized_keys"))
        os.chmod(os.path.join(self.dir, "authorized_keys"), 0o600)
        with socket.socket() as s:
            s.bind(("127.0.0.1", 0))
            self.port = s.getsockname()[1]
        cfg = os.path.join(self.dir, "sshd_config")
        with open(cfg, "w") as f:
            f.write(f"Port {self.port}\nListenAddress 127.0.0.1\nHostKey {self.dir}/hostkey\nAuthorizedKeysFile {self.dir}/authorized_keys\n"
                    f"PasswordAuthentication no\nKbdInteractiveAuthentication no\nUsePAM no\nStrictModes no\nPidFile {self.dir}/pid\n")
        self.p = subprocess.Popen([sshd, "-D", "-e", "-f", cfg], stderr=open(os.path.join(self.dir, "sshd.log"), "w"))
        for _ in range(100):
            try:
                socket.create_connection(("127.0.0.1", self.port), timeout=0.2).close()
                return self
            except OSError:
                time.sleep(0.05)
        raise RuntimeError("sshd did not start (see sshd.log in the bench directory)")

    def __exit__(self, *a) -> None:
        if self.p:
            self.p.terminate()
            self.p.wait(timeout=10)

    def rsh(self) -> str:
        """The ssh command rsync runs (-e), the destination left to rsync."""
        import shlex
        return " ".join(shlex.quote(w) for w in ["ssh", "-i", os.path.join(self.dir, "clientkey"), "-p", str(self.port), "-o", "IdentitiesOnly=yes",
                                                "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=no", "-o", f"UserKnownHostsFile={self.dir}/known_hosts",
                                                "-o", "LogLevel=ERROR"])

    def worker_cmd(self, root: str, pid_file: str) -> list[str]:
        """ssh to the stand-in running the plane worker the way the API launches it on a host (hosts.go, worker())."""
        remote = f'echo $$ > {pid_file}; cd {WORKER_DIR} && VESQIRA_ROOTS=\'["{root}"]\' exec {sys.executable} -P plane_worker.py'
        return ["ssh", "-i", os.path.join(self.dir, "clientkey"), "-p", str(self.port), "-o", "IdentitiesOnly=yes", "-o", "BatchMode=yes",
                "-o", "StrictHostKeyChecking=no", "-o", f"UserKnownHostsFile={self.dir}/known_hosts", "-o", "LogLevel=ERROR",
                "127.0.0.1", "sh -c " + shlex_quote(remote)]


def shlex_quote(s: str) -> str:
    import shlex
    return shlex.quote(s)


def p95(xs: list[float]) -> float:
    s = sorted(xs)
    return s[min(len(s) - 1, int(round(0.95 * (len(s) - 1))))]


def run(path: str, nx: int, ny: int, nc: int, repeats: int, worker: "Worker | None" = None, drop=None, suffix: str = "") -> dict:
    """The worker's reads on one cube. With drop (a function that evicts the file from the page cache) every scenario evicts
    first, so its first call is a cold read, and the plane scenario is one request repeated (plane_scrub keeps the five
    neighbouring channels, each read from disk once). Each entry carries the bytes of the replies (what an ssh link would
    carry) and the worker's peak resident memory so far."""
    w = worker or Worker()
    out: dict = {"cube": {"nx": nx, "ny": ny, "channels": nc, "logical_gib": nx * ny * nc * 4 / 2**30}, "ops": {}}
    mid = nc // 2
    c = min(nx, ny) // 2
    side = min(nx, ny) // 8                       # a region 1/8 of the shorter side: a typical hand-drawn box
    five = [dict(op="plane", path=path, channel=ch) for ch in (0, mid, mid + 1, nc - 1, mid - 1)]
    scenarios = {
        "view": dict(op="view", path=path),
        "plane": five if drop is None else dict(op="plane", path=path, channel=mid),
        "coord": dict(op="coord", path=path, channel=mid, x=10, y=10),
        "spectrum_small": dict(op="spectrum", path=path, shape="rect", x0=c, y0=c, x1=c + 9, y1=c + 9),
        "spectrum_box": dict(op="spectrum", path=path, shape="rect", x0=c, y0=c, x1=c + side - 1, y1=c + side - 1),
    }
    if drop is not None:
        scenarios["plane_scrub"] = [dict(op="plane", path=path, channel=ch) for ch in range(mid - 2, mid + 3)]
    for name, req in scenarios.items():
        reqs = req if isinstance(req, list) else [req]
        times: list[float] = []
        err = None
        before = w.received
        if drop is not None:
            drop(path)
        for i in range(repeats if not isinstance(req, list) else 1):
            for r in reqs:
                ms, head = w.ask(**r)
                if not head.get("ok"):
                    err = head.get("error") or head.get("code") or "failed"
                    break
                times.append(ms)
            if err:
                break
        entry: dict = {"rss_mb": w.rss_mb(), "peak_mb": w.peak_mb()}
        if err:
            entry["error"] = err
        else:
            entry.update(first_ms=round(times[0], 1), median_ms=round(statistics.median(times[1:] if drop and len(times) > 1 else times), 1),
                         p95_ms=round(p95(times[1:] if drop and len(times) > 1 else times), 1), n=len(times),
                         reply_bytes=(w.received - before) // len(times))
        out["ops"][name + suffix] = entry
    if worker is None:
        w.close()
    return out


def load_bench(path: str, nx: int, ny: int, nc: int, repeats: int) -> dict:
    """Planes on their own worker process while a heavy worker (large-region spectra, back to back) and a background worker
    (header reads, back to back) are busy: the workload classes of ADR 0056, each with its own process. What it measures is
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


def sha256_of(path: str) -> str:
    import hashlib
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for block in iter(lambda: f.read(8 << 20), b""):
            h.update(block)
    return h.hexdigest()


def transfer_bench(d: str, sshd: "UserSshd", mib: int) -> dict:
    """A download through ssh that is cut at about 40 % and run again, with the flags the app uses for the copy itself
    (hosts.RsyncArgv: --partial-dir, no in-place writes). Reports how much the second run sent again against how much was
    already there, and whether the finished file hashes like the source. The cut kills the rsync client, which is what a
    dropped VPN or a closed app does to it; the link is loopback, so times are CPU and disk, not a network."""
    import re
    src_dir, dst_dir = os.path.join(d, "remote"), os.path.join(d, "local")
    os.makedirs(src_dir)
    os.makedirs(dst_dir)
    src = os.path.join(src_dir, "cube.fits")
    with open(src, "wb") as f:
        for _ in range(mib):
            f.write(os.urandom(1 << 20))                    # no repeated blocks: rsync's rolling checksum would match them anywhere
    total = os.path.getsize(src)
    base = ["rsync", "-rlt", "--partial-dir=.vesqira-partial", "--timeout=600", "--stats", "--bwlimit=262144", "-e", sshd.rsh(),
            "--", f"127.0.0.1:{src}", os.path.join(dst_dir, "cube.fits")]
    part = os.path.join(dst_dir, ".vesqira-partial", "cube.fits")
    t0 = time.perf_counter()
    p = subprocess.Popen(base, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    while p.poll() is None:
        # while it runs rsync writes a hidden temporary file next to the destination; SIGTERM (what the app sends on a cancel
        # or a quit) makes it move that file into the partial folder
        tmp = [os.path.getsize(os.path.join(dst_dir, n)) for n in os.listdir(dst_dir) if n.startswith(".cube.fits.")]
        if tmp and max(tmp) >= 0.4 * total:
            p.terminate()
            break
        time.sleep(0.005)
    p.wait()
    first_ms = (time.perf_counter() - t0) * 1000
    for _ in range(400):                                   # rsync's receiver is a child: it moves the file a moment after the client exits
        if os.path.exists(part):
            break
        time.sleep(0.01)
    time.sleep(0.3)
    kept = os.path.getsize(part) if os.path.exists(part) else 0
    final = os.path.join(dst_dir, "cube.fits")
    t1 = time.perf_counter()
    r = subprocess.run(base, capture_output=True, text=True)
    resume_ms = (time.perf_counter() - t1) * 1000

    def stat(name: str) -> int:
        m = re.search(name + r":\s+([\d,]+)", r.stdout)
        return int(m.group(1).replace(",", "")) if m else -1

    entry = {"file_mib": total // (1 << 20), "cut_ms": round(first_ms), "partial_kept_mib": round(kept / (1 << 20), 1),
             "resume_ms": round(resume_ms), "resume_exit": r.returncode, "literal_mib": round(stat("Literal data") / (1 << 20), 1),
             "matched_mib": round(stat("Matched data") / (1 << 20), 1), "received_mib": round(stat("Total bytes received") / (1 << 20), 1),
             "hash_equal": os.path.exists(final) and sha256_of(final) == sha256_of(src),
             "partial_gone": not os.path.exists(part)}
    # the cost of the whole file from nothing, for the comparison
    full = os.path.join(dst_dir, "whole.fits")
    t2 = time.perf_counter()
    subprocess.run(base[:-1] + [full], capture_output=True)
    entry["from_scratch_ms"] = round((time.perf_counter() - t2) * 1000)
    return entry


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--size", default="1500x1500x600", help="NXxNYxNC of the sparse cube")
    ap.add_argument("--register-files", type=int, default=300, help="small files for the registration benchmark")
    ap.add_argument("--repeats", type=int, default=5)
    ap.add_argument("--dir", default=None, help="where to put the cube (default: a temporary directory)")
    ap.add_argument("--real-disk", default=None, metavar="DIR", help="also read a dense cube made in DIR (a real file system, not tmpfs) cold and warm")
    ap.add_argument("--ssh", action="store_true", help="also read through a real ssh to a user-mode sshd on loopback (needs sshd; no root)")
    ap.add_argument("--transfer-mib", type=int, default=1024, help="size of the file of the interrupted-and-resumed transfer (with --ssh)")
    ap.add_argument("--check", action="store_true", help="fail when a budget is exceeded")
    ap.add_argument("--json", default=None)
    a = ap.parse_args()
    nx, ny, nc = (int(v) for v in a.size.split("x"))
    notes: list[str] = []
    with tempfile.TemporaryDirectory(dir=a.dir) as d:
        path = os.path.realpath(os.path.join(d, "bench.fits"))
        make_cube(path, nx, ny, nc)
        res = run(path, nx, ny, nc, a.repeats)
        # a fourth, length-1 STOKES axis: the same bytes, a 4D header
        path4 = os.path.realpath(os.path.join(d, "bench4d.fits"))
        make_cube(path4, nx, ny, nc, stokes=1)
        ops4 = run(path4, nx, ny, nc, a.repeats, suffix="_4d")["ops"]
        res["ops"].update({k: v for k, v in ops4.items() if k in ("plane_4d", "spectrum_small_4d", "spectrum_box_4d")})
        # a real Stokes axis (length 4) is outside what the viewer supports: say so in the numbers, not in a crash
        path4s = os.path.realpath(os.path.join(d, "stokes4.fits"))
        make_cube(path4s, 64, 64, 8, stokes=4)
        w = Worker()
        _, head = w.ask(op="plane", path=path4s, channel=0)
        w.close()
        notes.append("4D cube with 4 Stokes planes: " + ("read" if head.get("ok") else "refused (" + str(head.get("error") or head.get("code")) + ")"))
        res["ops"]["register"], res["register_one_process_per_file"] = register_bench(d, a.register_files, 20)
        res["ops"]["plane_under_load"] = load_bench(path, nx, ny, nc, a.repeats)
    if a.real_disk or a.ssh:
        with tempfile.TemporaryDirectory(dir=a.real_disk or a.dir) as d:
            dense = os.path.realpath(os.path.join(d, "dense.fits"))
            t = time.perf_counter()
            make_dense_cube(dense, nx, ny, nc)
            size = os.path.getsize(dense)
            fstype = subprocess.run(["df", "--output=fstype", d], capture_output=True, text=True).stdout.split()[-1]
            notes.append(f"dense cube {size / 2**30:.1f} GiB written in {time.perf_counter() - t:.0f} s in {os.path.dirname(dense)} ({fstype})")
            if a.real_disk:
                evict(dense)
                frac = cached_fraction(dense)
                notes.append("after eviction " + ("%.1f %% of the file was in the page cache (fincore)" % (100 * frac) if frac is not None else "residency unknown (no fincore)"))
                w = Worker()
                w.ask(op="ping")
                res["ops"].update({"disk_" + k: v for k, v in run(dense, nx, ny, nc, a.repeats, worker=w, drop=evict)["ops"].items()})
                w.close()
            if a.ssh:
                with UserSshd(d) as sshd:
                    pid_file = os.path.join(d, "worker.pid")
                    w = Worker(sshd.worker_cmd(d, pid_file), pid_file)
                    t = time.perf_counter()
                    w.ask(op="ping")
                    notes.append(f"ssh connection, login and worker start: {(time.perf_counter() - t) * 1000:.0f} ms (the worker's first answer)")
                    res["ops"].update({"ssh_" + k: v for k, v in run(dense, nx, ny, nc, a.repeats, worker=w, drop=evict if a.real_disk else None)["ops"].items()})
                    w.close()
                    if shutil.which("rsync"):
                        res["transfer_resume"] = transfer_bench(d, sshd, a.transfer_mib)
    print(f"sparse cube {nx}x{ny}x{nc} ({res['cube']['logical_gib']:.1f} GiB logical); page-cache speed, not disk, except the disk_ and ssh_ rows")
    print(f"{'op':22s} {'first ms':>9s} {'median':>8s} {'p95':>8s} {'rss MB':>7s} {'peak MB':>8s} {'reply KB':>9s}")
    for name, e in res["ops"].items():
        if "error" in e:
            print(f"{name:22s} {'refused: ' + str(e['error']):>38s} {e['rss_mb']:7d}")
        else:
            print(f"{name:22s} {e['first_ms']:9.1f} {e['median_ms']:8.1f} {e['p95_ms']:8.1f} {e['rss_mb']:7d} {e.get('peak_mb', 0):8d} {e.get('reply_bytes', 0) / 1024:9.1f}")
    for n in notes:
        print("note: " + n)
    if "transfer_resume" in res:
        t = res["transfer_resume"]
        print(f"transfer: {t['file_mib']} MiB download through ssh cut after {t['cut_ms']} ms with {t['partial_kept_mib']} MiB kept; resumed in {t['resume_ms']} ms "
              f"(exit {t['resume_exit']}, literal {t['literal_mib']} MiB sent again, matched {t['matched_mib']} MiB, received {t['received_mib']} MiB), "
              f"from scratch {t['from_scratch_ms']} ms; hash equal: {t['hash_equal']}; partial removed: {t['partial_gone']}")
    pl = res["ops"]["plane_under_load"]
    if "error" not in pl:
        print(f"plane_under_load: planes while the heavy worker served {pl['heavy_served']} spectra and the background worker {pl['background_served']} header reads")
    old = res["register_one_process_per_file"]
    print(f"register: {res['ops']['register']['n']} small files per request through the persistent worker; "
          f"previously one process per file: median {old['median_ms']} ms (n={old['n']})")
    res["notes"] = notes
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
