import hashlib
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

import numpy as np
from astropy.io import fits
from astropy.wcs import WCS

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parent.parent / "fixtures"))
import generate as g  # noqa: E402

SCRIPT = str(HERE / "analysis.py")


def run(*args):
    p = subprocess.run([sys.executable, "-P", SCRIPT, *map(str, args)], capture_output=True, text=True, timeout=120)
    return p.returncode, json.loads(p.stdout)


def sha(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


class Analysis(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        cls.dir = Path(cls.tmp.name)
        cls.f = g.write_all(cls.dir / "in")
        with fits.open(cls.f["hi_cube.fits"]) as h:
            cls.data = h[0].data.astype(np.float64)       # (channel, y, x), NaN box included

    @classmethod
    def tearDownClass(cls):
        cls.tmp.cleanup()

    def out(self, name):
        return self.dir / name

    def test_stats_match_numpy_and_count_what_is_missing(self):
        code, r = run("stats", "--input", self.f["hi_cube.fits"])
        self.assertEqual(code, 0)
        s = r["summary"]
        fin = self.data[np.isfinite(self.data)]
        self.assertEqual(s["finite"], fin.size)
        self.assertEqual(s["no_value"], self.data.size - fin.size)       # 9 NaN pixels in each of 64 channels
        self.assertEqual(s["no_value"], 9 * g.NC)
        self.assertAlmostEqual(s["mean"], fin.mean(), places=9)
        self.assertAlmostEqual(s["std"], fin.std(), places=9)
        self.assertEqual((s["peak"]["channel"], s["peak"]["x"], s["peak"]["y"]), (g.SOURCE_CHANNEL, g.SOURCE_PIX[0], g.SOURCE_PIX[1]))
        self.assertIn("lon", s["peak"]["sky"])
        self.assertEqual(len(s["per_channel"]), g.NC)

    def test_moment0_matches_an_independent_sum_and_missing_is_not_zero(self):
        out = self.out("m0.fits")
        code, r = run("moment", "--input", self.f["hi_cube.fits"], "--order", 0, "--out", out)
        self.assertEqual(code, 0, r)
        m0 = fits.getdata(out).astype(np.float64)
        expect = np.nansum(self.data, axis=0) * g.DV_MS                   # independent: plain sum times the channel width
        box = np.isnan(self.data).all(axis=0)
        np.testing.assert_allclose(m0[~box], expect[~box], rtol=1e-5)
        self.assertTrue(np.isnan(m0[box]).all())                         # a sum of nothing is unknown, never 0
        self.assertEqual(fits.getheader(out)["BUNIT"], "K m/s")
        self.assertEqual(fits.getheader(out)["CTYPE1"], "RA---TAN")

    def test_moments_1_and_2_of_a_selected_line_match_the_closed_form(self):
        thr = 1.0
        w = np.where(np.isfinite(self.data) & (self.data >= thr), self.data, 0.0)
        v = g.V0_MS + np.arange(g.NC) * g.DV_MS
        s0 = w.sum(axis=0)
        with np.errstate(all="ignore"):
            m1 = (w * v[:, None, None]).sum(axis=0) / s0
            m2 = np.sqrt((w * (v[:, None, None] - m1) ** 2).sum(axis=0) / s0)
        for order, expect in ((1, m1), (2, m2)):
            out = self.out(f"m{order}.fits")
            code, r = run("moment", "--input", self.f["hi_cube.fits"], "--order", order, "--threshold", thr, "--out", out)
            self.assertEqual(code, 0, r)
            got = fits.getdata(out).astype(np.float64)
            sel = s0 > 0
            np.testing.assert_allclose(got[sel], expect[sel], rtol=1e-4)
            self.assertTrue(np.isnan(got[~sel]).all())                    # no selected value: NaN, not 0
            self.assertEqual(fits.getheader(out)["BUNIT"], "m/s")
        # the synthetic source is centred on channel 40: its line centre is that channel's velocity
        c = fits.getdata(self.out("m1.fits"))[g.SOURCE_PIX[1], g.SOURCE_PIX[0]]
        self.assertAlmostEqual(float(c), g.V0_MS + g.SOURCE_CHANNEL * g.DV_MS, delta=200.0)

    def test_moment_one_on_a_reversed_axis_still_finds_the_line_centre(self):
        out = self.out("rev_m1.fits")
        code, r = run("moment", "--input", self.f["hi_cube_reverse_spectral.fits"], "--order", 1, "--threshold", 1.0, "--out", out)
        self.assertEqual(code, 0, r)
        centre = float(fits.getdata(out)[g.SOURCE_PIX[1], g.SOURCE_PIX[0]])
        self.assertAlmostEqual(centre, g.V0_MS + (g.NC - 1 - g.SOURCE_CHANNEL) * g.DV_MS, delta=200.0)   # channel 40 now sits at the velocity the header gives it
        m0 = self.out("rev_m0.fits")
        run("moment", "--input", self.f["hi_cube_reverse_spectral.fits"], "--order", 0, "--out", m0)
        ref = self.out("fwd_m0.fits")
        run("moment", "--input", self.f["hi_cube.fits"], "--order", 0, "--out", ref)
        a, b = fits.getdata(m0), fits.getdata(ref)
        np.testing.assert_allclose(np.nan_to_num(a), np.nan_to_num(b), rtol=1e-4)           # moment 0 is positive either way round

    def test_moment_refuses_when_the_spectral_axis_is_not_declared(self):
        code, r = run("moment", "--input", self.f["hi_cube_no_spectral_ref.fits"], "--out", self.out("bad.fits"))
        self.assertEqual(code, 2)
        self.assertIn("not fully declared", r["error"])
        self.assertFalse(self.out("bad.fits").exists())

    def test_cutout_keeps_every_coordinate_exact(self):
        out = self.out("cut.fits")
        code, r = run("cutout", "--input", self.f["hi_cube.fits"], "--x0", 10, "--x1", 25, "--y0", 4, "--y1", 15, "--out", out)
        self.assertEqual(code, 0, r)
        cut = fits.getdata(out)
        np.testing.assert_array_equal(cut, self.data[:, 4:16, 10:26].astype(np.float32))
        w0, w1 = WCS(fits.getheader(self.f["hi_cube.fits"])), WCS(fits.getheader(out))
        a = w0.all_pix2world([[10 + 3, 4 + 2, 5]], 0)[0]                  # a pixel in the original
        b = w1.all_pix2world([[3, 2, 5]], 0)[0]                           # the same pixel in the cutout
        np.testing.assert_allclose(a, b, rtol=0, atol=1e-9)

    def test_slab_keeps_the_spectral_axis_exact(self):
        out = self.out("slab.fits")
        code, r = run("slab", "--input", self.f["hi_cube.fits"], "--chan-start", 30, "--chan-end", 49, "--out", out)
        self.assertEqual(code, 0, r)
        slab = fits.getdata(out)
        np.testing.assert_array_equal(slab, self.data[30:50].astype(np.float32))
        w0, w1 = WCS(fits.getheader(self.f["hi_cube.fits"])), WCS(fits.getheader(out))
        self.assertAlmostEqual(w0.all_pix2world([[5, 5, 33]], 0)[0][2], w1.all_pix2world([[5, 5, 3]], 0)[0][2], places=6)

    def test_align_puts_a_cube_on_the_reference_grid_with_nan_outside_never_zero(self):
        out = self.out("aligned.fits")
        code, r = run("align", "--input", self.f["hi_cube.fits"], "--reference", self.f["hi_cube_coarser.fits"], "--out", out)
        self.assertEqual(code, 0, r)
        a = fits.getdata(out)
        self.assertEqual(a.shape, (g.NC, g.NY, g.NX))                      # the reference's celestial grid, same channels
        ref_w = WCS(fits.getheader(self.f["hi_cube_coarser.fits"])).celestial
        out_w = WCS(fits.getheader(out)).celestial
        np.testing.assert_allclose(ref_w.pixel_to_world_values(7, 9), out_w.pixel_to_world_values(7, 9), atol=1e-9)
        self.assertGreater(int(np.isnan(a).sum()), 0)                      # the coarser grid is wider: outside the input is NaN
        self.assertEqual(int((a == 0).sum()), int((self.data.astype(np.float32) == 0).sum()))   # no zeros were invented
        self.assertIn("resolution", r["summary"]["policy"])
        self.assertIn("reproject", r["software"])
        # the spectral axis is carried over unchanged
        w = WCS(fits.getheader(out))
        self.assertAlmostEqual(w.all_pix2world([[0, 0, 40]], 0)[0][2], g.V0_MS + 40 * g.DV_MS, places=6)
        # the source peak is where the independent TAN positions say it should be after alignment
        ra, dec = out_w.pixel_to_world_values(*np.unravel_index(int(np.nanargmax(a[g.SOURCE_CHANNEL])), a[g.SOURCE_CHANNEL].shape)[::-1])
        src_w = WCS(fits.getheader(self.f["hi_cube.fits"])).celestial
        sra, sdec = src_w.pixel_to_world_values(*g.SOURCE_PIX)
        self.assertLess(abs(float(ra) - float(sra)), 0.06)                 # within about 2 coarse pixels of 0.02 deg
        self.assertLess(abs(float(dec) - float(sdec)), 0.06)

    def test_aligning_across_frames_uses_the_proper_transform(self):
        out = self.out("gal.fits")
        code, r = run("align", "--input", self.f["hi_cube.fits"], "--reference", self.f["hi_cube_galactic.fits"], "--out", out)
        self.assertEqual(code, 0, r)
        a = fits.getdata(out)
        self.assertEqual(r["summary"]["source_frame"], "ICRS")
        self.assertEqual(r["summary"]["reference_frame"], "Galactic")
        self.assertTrue(np.isnan(a).all())                                 # the fixtures do not overlap on the sky: all NaN, not zeros

    def test_axial_difference_is_modulo_180_and_keeps_missing_missing(self):
        a, b = self.f["vgt_psi_deg.fits"], self.f["vgt_psi_rad.fits"]
        out = self.out("diff.fits")
        code, r = run("axial_diff", "--input", a, "--unit", "deg", "--reference", b, "--unit-ref", "rad", "--out", out)
        self.assertEqual(code, 0, r)
        self.assertTrue(np.allclose(np.nan_to_num(fits.getdata(out), nan=0.0), 0.0, atol=1e-4))   # same map in two units: no difference
        # a shifted copy: 1 deg vs 179 deg must differ by 2 deg, and 0 vs 90 by 90 (the cancelling case is checked on the mean)
        base = g.vgt_angle_map()["angle_deg"]
        shifted = (base + 178.0) % 180.0
        hd = fits.PrimaryHDU(shifted, header=g._map_header(base.shape))
        sp = self.dir / "shifted.fits"
        fits.HDUList([hd]).writeto(sp)
        out2 = self.out("diff2.fits")
        code, r = run("axial_diff", "--input", sp, "--reference", a, "--out", out2)
        self.assertEqual(code, 0, r)
        d = fits.getdata(out2).astype(float)
        self.assertTrue(np.allclose(d[np.isfinite(d)], -2.0, atol=1e-4))   # 178 degrees more is 2 degrees less, axially
        self.assertEqual(int(np.isnan(d).sum()), 1)                          # the NaN pixel stays NaN
        self.assertAlmostEqual(r["summary"]["mean_difference_deg"], -2.0, places=4)
        self.assertGreater(r["summary"]["resultant"], 0.999)
        self.assertIn("1 and 179", r["summary"]["policy"])
        code, r = run("axial_diff", "--input", a, "--reference", self.f["hi_cube.fits"], "--out", self.out("bad.fits"))
        self.assertEqual(code, 2)

    def test_inputs_are_never_modified_and_failures_leave_no_output(self):
        before = sha(self.f["hi_cube.fits"])
        run("moment", "--input", self.f["hi_cube.fits"], "--order", 0, "--out", self.out("again.fits"))
        run("cutout", "--input", self.f["hi_cube.fits"], "--x0", 0, "--x1", 3, "--y0", 0, "--y1", 3, "--out", self.out("again2.fits"))
        self.assertEqual(sha(self.f["hi_cube.fits"]), before)
        code, r = run("cutout", "--input", self.f["hi_cube.fits"], "--x0", 0, "--x1", 999, "--y0", 0, "--y1", 3, "--out", self.out("nope.fits"))
        self.assertEqual(code, 2)
        self.assertFalse(self.out("nope.fits").exists())
        self.assertFalse(list(self.dir.glob("*.tmp")))                     # no temporary files left behind
        code, r = run("slab", "--input", self.f["hi_cube.fits"], "--chan-start", 70, "--chan-end", 80, "--out", self.out("nope2.fits"))
        self.assertEqual(code, 2)
        code, r = run("moment", "--input", "/nonexistent.fits", "--out", self.out("nope3.fits"))
        self.assertEqual(code, 2)


if __name__ == "__main__":
    unittest.main()
