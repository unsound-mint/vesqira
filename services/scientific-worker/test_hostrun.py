"""One bounded write on a host (hostrun.py, ADR 0064): the only file written is a new one, of a fixed name shape, in the derived folder
fixed by the launcher; it appears whole or not at all, never replaces a file, and inputs outside the roots are refused."""
import json
import os
import subprocess
import sys
import tempfile
import unittest

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
