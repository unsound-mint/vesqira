"""The decomposition review on a host (hostrun.py, ADR 0068): the sigma-v diagram, the cell click and the class maps, run by the host's own
copy of the program. The numbers are the local program's (the same code, the hand-checked fit of test_gaussreview); what is tested here is
what the host adds: where the pixel index goes, what a request may name, the files class maps leave, and the stop when the caller leaves."""
import json
import os
import subprocess
import sys
import tempfile
import textwrap
import time
import unittest
from pathlib import Path

import numpy as np
from astropy.io import fits

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import test_gaussreview as tg  # noqa: E402

STEM = "rohsa-classes-7-0a1b2c3d"


class HostReview(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        base = Path(os.path.realpath(tmp.name))
        self.root, self.data = base / "root", base / "root" / "data"
        self.data.mkdir(parents=True)
        self.cache = base / "home" / ".cache" / "vesqira" / "rohsa"      # the launcher's folder, outside the data
        self.crop, self.dat = self.data / "crop.fits", self.data / "rohsa_n2.dat"
        tg.write_crop(self.crop)
        tg.write_dat(self.dat)
        self.derived = self.root / "vesqira_derived"

    def params(self, **extra):
        return {"dat_path": str(self.dat), "n_gauss": tg.N, "weight": "integrated", **extra}

    def run_host(self, op, params, name=None, argv_extra=(), cache="default", roots=None):
        env = {**os.environ, "VESQIRA_ROOTS": json.dumps(roots or [str(self.root)]), "VESQIRA_DERIVED": str(self.derived)}
        env.pop("VESQIRA_CACHE", None)
        if cache is not None:
            env["VESQIRA_CACHE"] = str(self.cache if cache == "default" else cache)
        req = {"op": op, "argv": ["--input", str(self.crop), "--params", json.dumps(params), *argv_extra]}
        if name:
            req["name"] = name
        p = subprocess.run([sys.executable, "-P", str(HERE / "hostrun.py")], input=json.dumps(req) + "\n", capture_output=True, text=True, env=env, timeout=120)
        return p.returncode, json.loads(p.stdout.strip().splitlines()[-1])

    def diagram(self, **kw):
        return {"velocity_bins": tg.V_BINS, "sigma_bins": tg.S_BINS, **kw}

    def cells_files(self):
        return sorted(p.name for p in self.cache.glob("*.cells")) if self.cache.is_dir() else []

    def test_the_diagram_and_the_cell_click_are_the_local_numbers_and_the_index_goes_to_the_cache_folder(self):
        code, got = self.run_host("rohsa_sigmav", self.params(**self.diagram()))
        self.assertEqual(code, 0, got)
        with tempfile.TemporaryDirectory() as local_cache:
            lc, want = tg.run("rohsa_sigmav", self.crop, self.dat, **self.diagram(), weight="integrated", cache_dir=local_cache)
        self.assertEqual(lc, 0, want)
        self.assertEqual(got["summary"]["cells"], want["summary"]["cells"])
        self.assertAlmostEqual(got["summary"]["weight_total"], 26.0 * tg.S, places=12)
        self.assertEqual(len(self.cells_files()), 1)
        self.assertEqual(list(self.data.glob("*.cells*")), [], "the index is never written next to the data")
        self.assertFalse(self.derived.exists(), "a diagram writes nothing in the derived folder")
        code, click = self.run_host("rohsa_cellmask", self.params(**self.diagram(), cell={"s": 1, "v": 1}))
        self.assertEqual(code, 0, click)
        self.assertEqual((click["summary"]["pixels"], click["summary"]["source"]), (2, "pixel index written by the diagram"))    # three components, in two pixels
        self.assertEqual(len(self.cells_files()), 1)

    def test_a_review_removes_only_stale_unfinished_indexes_from_its_cache_folder(self):
        self.cache.mkdir(parents=True)
        old, fresh, other, link_target = (self.cache / n for n in ("a.cells.tmp", "b.cells.tmp", "c.cells", "d.txt"))
        for f in (old, fresh, other, link_target):
            f.write_text("x")
        linked = self.cache / "e.cells.tmp"
        linked.symlink_to(link_target)
        long_ago = time.time() - 3600
        for f in (old, other):
            os.utime(f, (long_ago, long_ago))
        os.utime(linked, (long_ago, long_ago), follow_symlinks=False)
        code, got = self.run_host("rohsa_sigmav", self.params(**self.diagram()))
        self.assertEqual(code, 0, got)
        left = sorted(p.name for p in self.cache.iterdir() if not p.name.startswith("rohsa") and not p.name.endswith(".cells") or p.name == "c.cells")
        self.assertEqual(left, ["b.cells.tmp", "c.cells", "d.txt", "e.cells.tmp"])   # the old one is gone; a fresh one, other kinds and a link stay

    def test_a_click_without_the_diagram_says_to_draw_it_again_and_does_not_read_the_fit(self):
        code, got = self.run_host("rohsa_cellmask", self.params(**self.diagram(), cell={"s": 1, "v": 1}))
        self.assertNotEqual(code, 0)
        self.assertIn("draw the diagram again", got["error"])
        self.assertEqual(self.cells_files(), [])

    def test_a_request_never_names_the_cache_or_other_options_and_every_path_is_in_the_roots(self):
        elsewhere = self.cache.parent / "elsewhere"
        for what, params, extra, kw in (
            ("a cache folder named by the request", self.params(**self.diagram(), cache_dir=str(elsewhere)), (), {}),
            ("an output", self.params(**self.diagram()), ("--out", str(self.root / "x.fits")), {}),
            ("an abbreviated option", self.params(**self.diagram()), ("--param=1",), {}),
            ("a repeated option", self.params(**self.diagram()), ("--params", json.dumps(self.params())), {}),
            ("a fit outside the roots", self.params(**self.diagram()), (), {"roots": [str(self.root / "data" / "nope")]}),
        ):
            code, got = self.run_host("rohsa_sigmav", params, argv_extra=extra, **kw)
            self.assertNotEqual(code, 0, what)
            self.assertFalse(got["ok"], what)
        self.assertFalse(elsewhere.exists())
        self.assertEqual(self.cells_files(), [])

    def test_the_cache_folder_is_the_launchers_and_never_inside_the_data(self):
        code, got = self.run_host("rohsa_sigmav", self.params(**self.diagram()), cache=None)
        self.assertNotEqual(code, 0)
        self.assertIn("no cache folder", got["error"])
        code, got = self.run_host("rohsa_sigmav", self.params(**self.diagram()), cache=str(self.data / "cache"))
        self.assertNotEqual(code, 0)
        self.assertIn("never written next to the data", got["error"])
        self.assertFalse((self.data / "cache").exists())

    def test_a_review_op_takes_no_name_and_class_maps_a_fixed_one(self):
        for op, name in (("rohsa_sigmav", STEM), ("rohsa_classmaps", None), ("rohsa_classmaps", "mask-1-x"), ("rohsa_classmaps", "rohsa-classes-7-0A1B2C3D"),
                         ("rohsa_classmaps", "rohsa-classes-7-0a1b2c3d/../x")):
            code, got = self.run_host(op, self.params(edges=[2.0]), name=name)
            self.assertNotEqual(code, 0, (op, name))
            self.assertEqual(got["error"], "bad review request")
        self.assertFalse(self.derived.exists())

    def test_class_maps_are_written_whole_in_the_derived_folder_and_equal_the_local_maps(self):
        code, got = self.run_host("rohsa_classmaps", self.params(edges=[2.0]), name=STEM)
        self.assertEqual(code, 0, got)
        want = [str(self.derived / f"{STEM}-class{i}.fits") for i in (0, 1)]
        self.assertEqual(got["extra_files"], want)
        self.assertEqual(sorted(os.listdir(self.derived)), [f"{STEM}-class0.fits", f"{STEM}-class1.fits"])
        with tempfile.TemporaryDirectory() as out:
            lc, local = tg.run("rohsa_classmaps", self.crop, self.dat, out=Path(out) / "m.fits", edges=[2.0], weight="integrated")
            self.assertEqual(lc, 0, local)
            for i in (0, 1):
                a, b = fits.getdata(want[i]), fits.getdata(Path(out) / f"m-class{i}.fits")
                np.testing.assert_array_equal(a, b)
                self.assertEqual(fits.getheader(want[i])["VESQCLS"], i)
        self.assertEqual(got["summary"]["classes"], local["summary"]["classes"])
        self.assertEqual(list(self.data.glob("*.fits")), [self.crop], "the crop is the only map in the data folder")

    def test_class_maps_never_replace_a_file_and_a_refusal_leaves_nothing(self):
        self.derived.mkdir()
        mine = self.derived / f"{STEM}-class1.fits"
        mine.write_text("the researcher's own")
        code, got = self.run_host("rohsa_classmaps", self.params(edges=[2.0]), name=STEM)
        self.assertNotEqual(code, 0)
        self.assertIn("left as it is", got["error"])
        self.assertEqual(mine.read_text(), "the researcher's own")
        mine.unlink()
        code, got = self.run_host("rohsa_classmaps", self.params(edges=[-1.0]), name=STEM)    # the program refuses the edges
        self.assertNotEqual(code, 0)
        self.assertFalse(got["ok"])
        self.assertEqual(os.listdir(self.derived), [], "no class map and no temporary stays")

    def test_the_work_stops_and_leaves_nothing_when_the_caller_goes_away(self):
        marker, pending = self.dir_file("unwound"), self.dir_file("pending")
        pending.write_text("x")
        driver = textwrap.dedent(f"""
            import sys, time
            sys.path.insert(0, {str(HERE)!r})
            import hostrun
            hostrun.HEARTBEAT_S = 0.2
            hostrun._PENDING.append({str(pending)!r})
            hostrun.stop_when_caller_leaves()
            try:
                time.sleep(60)               # a long pass over a big fit
            finally:
                open({str(marker)!r}, "w").write("unwound")
        """)
        p = subprocess.Popen([sys.executable, "-P", "-c", driver], stdout=subprocess.PIPE, text=True)
        self.assertEqual(json.loads(p.stdout.readline()), {"stage": "working"})
        p.stdout.close()                     # the other end of the connection is gone
        t0 = time.time()
        self.assertEqual(p.wait(timeout=30), 3)
        self.assertLess(time.time() - t0, 3, "the main thread is interrupted, not waited for")
        self.assertEqual(marker.read_text(), "unwound", "the pass was unwound, so analysis.py's own cleanup runs")

    def dir_file(self, name):
        return self.root / name


if __name__ == "__main__":
    unittest.main()
