import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parent.parent / "fixtures"))
import generate as g  # noqa: E402

SCRIPT = str(HERE / "fits_header.py")


def run(path):
    p = subprocess.run([sys.executable, "-P", SCRIPT, str(path)], capture_output=True, text=True, timeout=20)
    return p.returncode, json.loads(p.stdout)


class Worker(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        cls.f = g.write_all(Path(cls.tmp.name))

    @classmethod
    def tearDownClass(cls):
        cls.tmp.cleanup()

    def test_native_axes_and_declared_values(self):
        code, r = run(self.f["hi_cube.fits"])
        self.assertEqual(code, 0)
        self.assertEqual([a["ctype"] for a in r["axes"]], ["RA---TAN", "DEC--TAN", "VRAD"])
        self.assertEqual([a["length"] for a in r["axes"]], [g.NX, g.NY, g.NC])
        self.assertEqual(r["axes"][2]["cdelt"], g.DV_MS)
        self.assertEqual(r["axes"][2]["cunit"], "m/s")
        self.assertEqual(r["axes"][2]["cdelt_status"], "declared")
        self.assertEqual(r["bunit"], "K")

    def test_beam_is_reported_separately_from_the_pixel_scale(self):
        _, r = run(self.f["hi_cube.fits"])
        self.assertEqual(r["beam"]["status"], "declared")
        self.assertEqual((r["beam"]["bmaj"], r["beam"]["bmin"], r["beam"]["bpa"]), (0.03, 0.02, 45.0))
        _, r2 = run(self.f["hi_cube_coarser.fits"])
        self.assertEqual(r2["beam"]["status"], "declared")
        _, r3 = run(self.f["vgt_psi_deg.fits"])
        self.assertEqual(r3["beam"]["status"], "unresolved")            # absent stays unresolved, never a default

    def test_missing_spectral_reference_is_unresolved_not_zero(self):
        _, r = run(self.f["hi_cube_no_spectral_ref.fits"])
        spec = r["axes"][2]
        self.assertIsNone(spec["crval"])
        self.assertIsNone(spec["cdelt"])
        self.assertEqual(spec["crval_status"], "unresolved")
        self.assertEqual(spec["cdelt_status"], "unresolved")

    def test_missing_celestial_wcs_is_unresolved(self):
        _, r = run(self.f["hi_cube_no_celestial_wcs.fits"])
        for a in r["axes"][:2]:
            self.assertIsNone(a["ctype"])
            self.assertIsNone(a["cdelt"])

    def test_rejects_non_fits_and_truncated_files(self):
        d = Path(self.tmp.name)
        (d / "x.fits").write_bytes(b"not a fits file" * 400)
        (d / "short.fits").write_bytes(b"SIMPLE  =                    T" + b" " * 50)
        for n in ("x.fits", "short.fits", "missing.fits"):
            code, r = run(d / n)
            self.assertEqual(code, 2)
            self.assertFalse(r["ok"])

    def test_rejects_absurd_axis_sizes_without_allocating(self):
        from astropy.io import fits
        h = fits.PrimaryHDU().header
        h["NAXIS"], h["NAXIS1"], h["NAXIS2"], h["NAXIS3"] = 3, 10**6, 10**6, 10**6
        p = Path(self.tmp.name) / "huge.fits"
        p.write_bytes(h.tostring(padding=True).encode("ascii"))
        code, r = run(p)
        self.assertEqual(code, 2)
        self.assertIn("limit", r["error"])

    def test_header_size_limit(self):
        p = Path(self.tmp.name) / "endless.fits"
        cards = ("SIMPLE  =                    T".ljust(80) + "COMMENT x".ljust(80) * 40)
        p.write_bytes((cards.ljust(2880 * 70)).encode("ascii"))
        code, r = run(p)
        self.assertEqual(code, 2)


if __name__ == "__main__":
    unittest.main()
