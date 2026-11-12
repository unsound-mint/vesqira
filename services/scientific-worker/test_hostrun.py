"""One bounded write on a host (hostrun.py, ADR 0064): the only file written is a new one, of a fixed name shape, in the derived folder
fixed by the launcher; it appears whole or not at all, never replaces a file, and inputs outside the roots are refused."""
import contextlib
import io
import json
import os
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

import numpy as np
from astropy.io import fits

HERE = os.path.dirname(os.path.abspath(__file__))


class HostRun(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        base = os.path.realpath(self.tmp.name)
        self.root, self.outside = os.path.join(base, "root"), os.path.join(base, "outside")
        os.makedirs(self.root)
        os.makedirs(self.outside)
        data = np.arange(3 * 4, dtype="f4").reshape(3, 4)
        data[0, 0] = np.nan
        fits.PrimaryHDU(data).writeto(os.path.join(self.root, "cube.fits"))
        fits.PrimaryHDU(data).writeto(os.path.join(self.outside, "cube.fits"))
        self.derived = os.path.join(self.root, "vesqira_derived")

    def run_req(self, req, derived=None, roots=None):
        env = {**os.environ, "VESQIRA_ROOTS": json.dumps(roots if roots is not None else [self.root]), "VESQIRA_DERIVED": derived or self.derived}
        p = subprocess.run([sys.executable, "-P", os.path.join(HERE, "hostrun.py")], input=json.dumps(req), capture_output=True, text=True, env=env, timeout=120)
        return p.returncode, json.loads(p.stdout.strip().splitlines()[-1])

    def mask_req(self, name="mask-1-missing-0a1b.fits", src=None, extra=()):
        return {"op": "mask", "name": name, "argv": ["--input", src or os.path.join(self.root, "cube.fits"), "--params", json.dumps({"method": "missing", "type": "missing"}), *extra]}

    def files(self):
        return sorted(os.listdir(self.derived)) if os.path.isdir(self.derived) else []

    def test_writes_one_new_file_in_the_derived_folder(self):
        code, res = self.run_req(self.mask_req())
        self.assertEqual(code, 0, res)
        self.assertEqual(res["file"], os.path.join(self.derived, "mask-1-missing-0a1b.fits"))
        self.assertEqual(res["summary"]["flagged"], 1)
        self.assertEqual(self.files(), ["mask-1-missing-0a1b.fits"])

    def test_never_replaces_a_file(self):
        os.makedirs(self.derived)
        with open(os.path.join(self.derived, "mask-1-missing-0a1b.fits"), "w") as f:
            f.write("the researcher's own")
        code, res = self.run_req(self.mask_req())
        self.assertNotEqual(code, 0)
        self.assertIn("left as it is", res["error"])
        with open(os.path.join(self.derived, "mask-1-missing-0a1b.fits")) as f:
            self.assertEqual(f.read(), "the researcher's own")
        self.assertEqual(self.files(), ["mask-1-missing-0a1b.fits"])

    def test_refuses_other_writes_names_and_ops(self):
        for req in (
            self.mask_req(extra=("--missing-out", os.path.join(self.root, "x.fits"))),
            self.mask_req(extra=("--out=" + os.path.join(self.root, "x.fits"),)),
            self.mask_req(extra=("--carry-masks", "[]")),
            self.mask_req(extra=("--missing=" + os.path.join(self.root, "x.fits"),)),   # argparse abbreviations of the write options
            self.mask_req(extra=("--carry=[]",)),
            self.mask_req(extra=("--rel", os.path.join(self.root, "x.fits"))),
            self.mask_req(extra=("--window={}",)),                                     # not an option of a mask
            self.mask_req(extra=("--params", "--out=" + os.path.join(self.root, "x.fits"))),
            self.mask_req(extra=("stray",)),
            self.mask_req(name="../mask-1-x.fits"),
            self.mask_req(name="mask-1-x.fits/../../y.fits"),
            self.mask_req(name="subset-1-x.fits"),          # the name must be the op's
            self.mask_req(name="mask-1 x.fits"),
            {**self.mask_req(), "op": "moment"},
            {**self.mask_req(), "argv": "--input /etc/passwd"},
        ):
            code, res = self.run_req(req)
            self.assertNotEqual(code, 0, req)
            self.assertFalse(res["ok"])
        self.assertEqual(self.files(), [])

    def test_inputs_outside_the_roots_are_refused_and_leave_nothing(self):
        code, res = self.run_req(self.mask_req(src=os.path.join(self.outside, "cube.fits")))
        self.assertNotEqual(code, 0)
        self.assertFalse(res["ok"])
        self.assertEqual(self.files(), [])          # no temporary file left behind either

    # --- several outputs: the result and the masks it carries (ADR 0064, masks carried through a subset) ---

    def make_source_mask(self):
        """A saturated mask of cube.fits in the derived folder, as a mask made earlier on the host."""
        code, res = self.run_req({"op": "mask", "name": "mask-src-saturated-aa11.fits", "argv": ["--input", os.path.join(self.root, "cube.fits"), "--params",
                                                                                                json.dumps({"method": "saturated", "type": "saturated", "threshold": 5.0, "source": "datasheet"})]})
        self.assertEqual(code, 0, res)
        return res["file"]

    def subset_req(self, carry=(), missing=None, name="subset-1-v1-0a1b.fits", extra=()):
        req = {"op": "subset", "name": name, "argv": ["--input", os.path.join(self.root, "cube.fits"), "--window", json.dumps({"x0": 1, "x1": 3, "y0": 0, "y1": 1}), *extra]}
        if carry:
            req["carry"] = list(carry)
        if missing:
            req["missing"] = missing
        return req

    def carry_one(self, src, name="mask-carried-1-saturated-0001.fits"):
        return {"name": name, "path": src, "type": "saturated"}

    def part_files(self):
        return [f for f in self.files() if f.startswith(".part-")]

    def test_a_subset_writes_the_carried_masks_and_the_no_value_mask_together(self):
        src = self.make_source_mask()
        code, res = self.run_req(self.subset_req([self.carry_one(src)], missing="mask-missing-1-missing-0002.fits"))
        self.assertEqual(code, 0, res)
        self.assertEqual(res["file"], os.path.join(self.derived, "subset-1-v1-0a1b.fits"))
        self.assertEqual(res["carried_files"], [os.path.join(self.derived, "mask-carried-1-saturated-0001.fits")])
        self.assertEqual(res["missing_file"], os.path.join(self.derived, "mask-missing-1-missing-0002.fits"))
        self.assertEqual(len(res["summary"]["mask_carry"]["carried"]), 1)
        self.assertEqual(self.files(), ["mask-carried-1-saturated-0001.fits", "mask-missing-1-missing-0002.fits", "mask-src-saturated-aa11.fits", "subset-1-v1-0a1b.fits"])
        with fits.open(res["carried_files"][0]) as h:        # the source mask cut by the same window: 2 x 3 of the 3 x 4 plane
            self.assertEqual(h[0].data.shape, (2, 3))
            self.assertEqual(h[0].header["VESQMTYP"], "saturated")
        with fits.open(res["missing_file"]) as h:            # the cube's one NaN is outside the window (x 1-3), so nothing is flagged
            self.assertEqual(int(h[0].data.sum()), 0)

    def test_one_output_failing_links_nothing_and_removes_every_temporary(self):
        src = self.make_source_mask()
        before = self.files()
        # the second carried mask is not a mask of the right type: the program fails after the result was already written to its temporary name
        bad = {"name": "mask-carried-1-low_snr-0003.fits", "path": src, "type": "low_snr"}
        code, res = self.run_req(self.subset_req([self.carry_one(src), bad], missing="mask-missing-1-missing-0002.fits"))
        self.assertNotEqual(code, 0)
        self.assertFalse(res["ok"])
        self.assertEqual(self.files(), before)
        # a source mask outside the roots is refused the same way
        outside_mask = os.path.join(self.outside, "m.fits")
        with fits.open(src) as h:
            h.writeto(outside_mask)
        code, res = self.run_req(self.subset_req([self.carry_one(outside_mask)]))
        self.assertNotEqual(code, 0)
        self.assertEqual(self.files(), before)

    def test_a_failure_while_linking_leaves_none_of_the_outputs(self):
        src = self.make_source_mask()
        before = self.files()
        real_link = os.link
        # the host's folder refuses the third link (a name that appeared meanwhile, a full quota): the two already placed are taken back
        import importlib.util
        spec = importlib.util.spec_from_file_location("hostrun_under_test", os.path.join(HERE, "hostrun.py"))
        mod = importlib.util.module_from_spec(spec)
        sys.path.insert(0, HERE)
        self.addCleanup(sys.path.remove, HERE)
        spec.loader.exec_module(mod)
        calls = []

        def flaky(a, b):
            calls.append(b)
            if len(calls) == 3:
                raise FileExistsError(b)
            real_link(a, b)
        env = {"VESQIRA_ROOTS": json.dumps([self.root]), "VESQIRA_DERIVED": self.derived}
        req = self.subset_req([self.carry_one(src)], missing="mask-missing-1-missing-0002.fits")
        out = io.StringIO()
        old_env = {k: os.environ.get(k) for k in env}
        os.environ.update(env)
        self.addCleanup(lambda: [os.environ.pop(k, None) if v is None else os.environ.__setitem__(k, v) for k, v in old_env.items()])
        import srcopen
        old_roots = srcopen.ROOTS
        srcopen.ROOTS = [self.root]
        self.addCleanup(setattr, srcopen, "ROOTS", old_roots)
        with mock.patch.object(mod.os, "link", flaky), mock.patch.object(mod.sys, "stdin", io.StringIO(json.dumps(req))), contextlib.redirect_stdout(out):
            code = mod.main()
        self.assertNotEqual(code, 0)
        self.assertEqual(len(calls), 3)
        self.assertIn("appeared", out.getvalue().strip().splitlines()[-1])
        self.assertEqual(self.files(), before)

    def test_a_name_collision_on_any_output_refuses_before_anything_is_made(self):
        src = self.make_source_mask()
        with open(os.path.join(self.derived, "mask-missing-1-missing-0002.fits"), "w") as f:
            f.write("the researcher's own")
        before = self.files()
        code, res = self.run_req(self.subset_req([self.carry_one(src)], missing="mask-missing-1-missing-0002.fits"))
        self.assertNotEqual(code, 0)
        self.assertIn("left as it is", res["error"])
        self.assertEqual(self.files(), before)
        with open(os.path.join(self.derived, "mask-missing-1-missing-0002.fits")) as f:
            self.assertEqual(f.read(), "the researcher's own")
        # two outputs of one request may not share a name either
        code, res = self.run_req(self.subset_req([self.carry_one(src), self.carry_one(src)]))
        self.assertNotEqual(code, 0)
        self.assertEqual(self.files(), before)

    def test_carried_masks_are_structured_fields_and_never_options(self):
        src = self.make_source_mask()
        before = self.files()
        out_elsewhere = os.path.join(self.root, "x.fits")
        for req in (
            self.subset_req(extra=("--carry-masks=[]",)),                                   # not in the allow-list, by exact name
            self.subset_req(extra=("--missing-out", out_elsewhere)),
            self.subset_req(extra=("--carry=[]",)),
            self.subset_req([{"name": "../mask-x.fits", "path": src, "type": "saturated"}]),
            self.subset_req([{"name": "subset-x.fits", "path": src, "type": "saturated"}]),   # a carried file has the mask shape
            self.subset_req([{"name": "mask-x.fits", "path": src, "type": "missing"}]),       # the no-value mask is never carried
            self.subset_req([{"name": "mask-x.fits", "path": "rel.fits", "type": "saturated"}]),
            self.subset_req([{"name": "mask-x.fits", "path": src, "type": "saturated", "out": out_elsewhere}]),   # no caller-chosen output
            self.subset_req(missing="subset-x.fits"),
            self.subset_req(missing="mask-x.fits/../../y.fits"),
            self.subset_req([self.carry_one(src, "mask-c%d-saturated-00.fits" % i) for i in range(17)]),
            {**self.mask_req(), "carry": [self.carry_one(src)]},                              # only a subset carries
            {**self.mask_req(), "missing": "mask-x.fits"},
        ):
            code, res = self.run_req(req)
            self.assertNotEqual(code, 0, req)
            self.assertFalse(res["ok"], req)
        self.assertEqual(self.files(), before)
        self.assertFalse(os.path.exists(out_elsewhere))

    def test_the_derived_folder_is_fixed_inside_the_roots_and_not_a_link(self):
        code, res = self.run_req(self.mask_req(), derived=os.path.join(self.outside, "vesqira_derived"))
        self.assertNotEqual(code, 0)
        self.assertIn("not inside", res["error"])
        os.symlink(self.outside, self.derived)
        code, res = self.run_req(self.mask_req())
        self.assertNotEqual(code, 0)
        self.assertIn("symbolic link", res["error"])
        self.assertEqual(os.listdir(self.outside), ["cube.fits"])
        os.unlink(self.derived)
        env = {k: v for k, v in os.environ.items() if k != "VESQIRA_ROOTS"}
        p = subprocess.run([sys.executable, "-P", os.path.join(HERE, "hostrun.py")], input=json.dumps(self.mask_req()), capture_output=True, text=True,
                           env={**env, "VESQIRA_DERIVED": self.derived}, timeout=60)
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("without its roots", p.stdout)


if __name__ == "__main__":
    unittest.main()
