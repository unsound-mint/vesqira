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

    def test_scalar_difference_is_exact_keeps_missing_missing_and_refuses_unit_guesses(self):
        shape = (4, 4)
        a = np.arange(16, dtype=np.float64).reshape(shape)
        b = np.full(shape, 1.5)
        a[0, 0] = np.nan                                   # no value in the first map
        b[3, 3] = np.nan                                   # no value in the second map
        a[1, 1] = 1.5                                      # a real zero difference
        def write(name, data, unit):
            hd = fits.PrimaryHDU(data, header=g._map_header(shape))
            if unit is not None:
                hd.header["BUNIT"] = unit
            path = self.dir / name
            fits.HDUList([hd]).writeto(path, overwrite=True)
            return str(path)
        pa, pb = write("sa.fits", a, "K"), write("sb.fits", b, "K")
        out = self.out("sdiff.fits")
        code, r = run("scalar_diff", "--input", pa, "--reference", pb, "--out", out)
        self.assertEqual(code, 0, r)
        d = fits.getdata(out).astype(float)
        want = a - b
        np.testing.assert_allclose(d[np.isfinite(want)], want[np.isfinite(want)], rtol=1e-6)
        self.assertTrue(np.isnan(d[0, 0]) and np.isnan(d[3, 3]))        # missing in either map stays missing
        self.assertEqual(d[1, 1], 0.0)                                   # a real zero is a value
        self.assertEqual(r["summary"]["pixels_without_data"], 2)
        self.assertEqual(r["summary"]["pixels_compared"], 14)
        self.assertAlmostEqual(r["summary"]["mean"], float(np.nanmean(want)), places=6)
        self.assertEqual(fits.getheader(out)["BUNIT"], "K")
        for pa2, pb2 in ((write("su.fits", a, None), pb), (pa, write("sl.fits", b, "Jy/beam")), (write("sw.fits", a, ""), pb)):
            code, r = run("scalar_diff", "--input", pa2, "--reference", pb2, "--out", self.out("never.fits"))
            self.assertEqual(code, 2, r)                                 # unknown or different units are refused, not guessed
            self.assertFalse((self.dir / "never.fits").exists())
        big = write("sbig.fits", np.zeros((5, 5)), "K")
        self.assertEqual(run("scalar_diff", "--input", pa, "--reference", big, "--out", self.out("never.fits"))[0], 2)

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


F = 2.0 * np.sqrt(2.0 * np.log(2.0))      # FWHM / sigma


def beam_image(path, cov_pix, amp=10.0, size=101, bunit="K", beam=None, nan_box=False):
    """A 2D image (1 arcsec pixels, north up, east left) holding a Gaussian with the given pixel covariance."""
    y, x = np.mgrid[:size, :size] - size // 2
    inv = np.linalg.inv(cov_pix)
    d = np.stack([x.ravel(), y.ravel()])
    img = amp * np.exp(-0.5 * np.einsum("ij,ij->j", d, inv @ d)).reshape(size, size)
    if nan_box:
        img[10:14, 20:24] = np.nan
    h = fits.Header()
    h["CTYPE1"], h["CTYPE2"] = "RA---TAN", "DEC--TAN"
    h["CRVAL1"], h["CRVAL2"], h["CRPIX1"], h["CRPIX2"] = 150.0, 2.0, size // 2 + 1, size // 2 + 1
    h["CDELT1"], h["CDELT2"] = -1.0 / 3600.0, 1.0 / 3600.0
    h["CUNIT1"], h["CUNIT2"] = "deg", "deg"
    h["BUNIT"] = bunit
    if beam:
        h["BMAJ"], h["BMIN"], h["BPA"] = beam[0] / 3600.0, beam[1] / 3600.0, beam[2]
    fits.PrimaryHDU(img.astype("<f4"), header=h).writeto(path, overwrite=True)
    return img


class Convolve(unittest.TestCase):
    """The result must be the Gaussian a beam of the target size would give: checked against the analytic answer,
    including which way the position angle turns on a grid with east to the left."""

    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        cls.dir = Path(cls.tmp.name)

    @classmethod
    def tearDownClass(cls):
        cls.tmp.cleanup()

    def convolve(self, name, cov0, beam0, target, **kw):
        src = self.dir / f"{name}.fits"
        beam_image(src, cov0, beam=beam0, **kw)
        code, r = run("convolve", "--input", src, "--out", self.dir / f"{name}_out.fits", "--target-bmaj", target[0], "--target-bmin", target[1], "--target-bpa", target[2])
        return code, r, self.dir / f"{name}_out.fits"

    def expected(self, cov_t, cov0, amp=10.0, size=101):
        y, x = np.mgrid[:size, :size] - size // 2
        d = np.stack([x.ravel(), y.ravel()])
        gauss = np.exp(-0.5 * np.einsum("ij,ij->j", d, np.linalg.inv(cov_t) @ d)).reshape(size, size)
        return amp * np.sqrt(np.linalg.det(cov0) / np.linalg.det(cov_t)) * gauss

    def check(self, out, want, tol=2e-3):
        got = fits.getdata(out).astype(float)
        c = slice(20, 81)
        self.assertLess(np.abs(got[c, c] - want[c, c]).max() / want.max(), tol)

    def test_circular_target_gives_the_analytic_gaussian(self):
        cov0, cov_t = np.eye(2) * 4.0, np.eye(2) * 9.0
        code, r, out = self.convolve("circ", cov0, (2 * F, 2 * F, 0.0), (3 * F, 3 * F, 0.0))
        self.assertEqual(code, 0, r)
        self.check(out, self.expected(cov_t, cov0))
        h = fits.getheader(out)
        self.assertAlmostEqual(h["BMAJ"] * 3600, 3 * F, places=6)
        self.assertEqual(h["VESQOP"], "convolve")

    def test_position_angle_zero_is_north_and_ninety_is_east_which_is_left_on_the_grid(self):
        cov0 = np.diag([4.0, 16.0])                    # source beam: minor 2 px along x, major 4 px along y (north), PA 0
        beam0 = (4 * F, 2 * F, 0.0)
        cov_t = np.diag([36.0, 25.0])                  # target: major 6 px along x (east-west), minor 5 px, PA 90
        code, r, out = self.convolve("pa90", cov0, beam0, (6 * F, 5 * F, 90.0))
        self.assertEqual(code, 0, r)
        self.check(out, self.expected(cov_t, cov0))

    def test_a_diagonal_position_angle_leans_towards_east_which_is_negative_x(self):
        cov0 = np.eye(2) * 4.0
        u, v = np.array([-1.0, 1.0]) / np.sqrt(2), np.array([1.0, 1.0]) / np.sqrt(2)     # PA 45: north-east = (-x, +y)
        cov_t = 36.0 * np.outer(u, u) + 9.0 * np.outer(v, v)
        code, r, out = self.convolve("pa45", cov0, (2 * F, 2 * F, 0.0), (6 * F, 3 * F, 45.0))
        self.assertEqual(code, 0, r)
        self.check(out, self.expected(cov_t, cov0))

    def test_missing_pixels_stay_missing_and_nothing_becomes_zero(self):
        code, r, out = self.convolve("nan", np.eye(2) * 4.0, (2 * F, 2 * F, 0.0), (3 * F, 3 * F, 0.0), nan_box=True)
        self.assertEqual(code, 0, r)
        got = fits.getdata(out)
        self.assertTrue(np.isnan(got[10:14, 20:24]).all())
        self.assertEqual(int(np.isnan(got).sum()), 16)
        self.assertEqual(r["summary"]["pixels_without_data"], 16)

    def test_jy_per_beam_is_rescaled_by_the_beam_area_and_kelvin_is_not(self):
        cov0 = np.eye(2) * 4.0
        _, rk, ok = self.convolve("k", cov0, (2 * F, 2 * F, 0.0), (3 * F, 3 * F, 0.0), bunit="K")
        _, rj, oj = self.convolve("jy", cov0, (2 * F, 2 * F, 0.0), (3 * F, 3 * F, 0.0), bunit="Jy/beam")
        ratio = np.nanmax(fits.getdata(oj)) / np.nanmax(fits.getdata(ok))
        self.assertAlmostEqual(ratio, (3 * 3) / (2 * 2), places=3)
        self.assertFalse(rk["summary"]["rescaled_by_beam_area"])
        self.assertTrue(rj["summary"]["rescaled_by_beam_area"])

    def test_it_refuses_what_convolution_cannot_do(self):
        cov0 = np.diag([4.0, 16.0])
        code, r, _ = self.convolve("smaller", cov0, (4 * F, 2 * F, 0.0), (3 * F, 3 * F, 0.0))     # 3 px < the 4 px major axis
        self.assertEqual(code, 2)
        self.assertIn("smaller", r["error"])
        code, r, _ = self.convolve("nobeam", cov0, None, (6 * F, 6 * F, 0.0))
        self.assertEqual(code, 2)
        self.assertIn("beam", r["error"])


class DeclaredUnit(unittest.TestCase):
    """A unit the author declared for a file without BUNIT reaches the results, labelled as theirs; the file's own BUNIT always wins."""

    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        cls.dir = Path(cls.tmp.name)
        cls.f = g.write_all(cls.dir / "in")
        with fits.open(cls.f["hi_cube.fits"]) as hd:                 # the same cube with its BUNIT taken out
            hd[0].header.remove("BUNIT")
            hd.writeto(cls.dir / "nounit.fits")
        cls.nounit = str(cls.dir / "nounit.fits")

    @classmethod
    def tearDownClass(cls):
        cls.tmp.cleanup()

    def out(self, name):
        return str(self.dir / name)

    def test_moment_zero_carries_the_declared_unit_as_the_authors_and_the_values_do_not_change(self):
        code, r = run("moment", "--input", self.nounit, "--order", 0, "--out", self.out("u0.fits"))
        self.assertEqual(code, 0, r)
        self.assertEqual(fits.getheader(self.out("u0.fits"))["BUNIT"], "(brightness unit unresolved) m/s")
        self.assertEqual(r["summary"]["unit_source"], "unresolved")
        code, r = run("moment", "--input", self.nounit, "--order", 0, "--declared-unit", "K", "--out", self.out("u1.fits"))
        self.assertEqual(code, 0, r)
        h = fits.getheader(self.out("u1.fits"))
        self.assertEqual((h["BUNIT"], h["VESQBUNS"]), ("K m/s", "user"))
        self.assertTrue(any("declared by the author" in str(x) for x in h["HISTORY"]))
        self.assertEqual(r["summary"]["unit_source"], "user")
        self.assertIn("declared by the author for the input", r["summary"]["policy"])
        self.assertIn("total. The brightness unit", r["summary"]["policy"])               # the added sentence starts a sentence of its own
        np.testing.assert_array_equal(fits.getdata(self.out("u0.fits")), fits.getdata(self.out("u1.fits")))   # a label, not a calibration
        # moment 1 is a velocity: no brightness unit is in it, and none is claimed
        code, r = run("moment", "--input", self.nounit, "--order", 1, "--threshold", 1.0, "--declared-unit", "K", "--out", self.out("u2.fits"))
        self.assertEqual(code, 0, r)
        self.assertNotIn("VESQBUNS", fits.getheader(self.out("u2.fits")))
        self.assertNotIn("unit_source", r["summary"])

    def test_the_files_own_bunit_wins_over_a_declared_unit(self):
        code, r = run("moment", "--input", self.f["hi_cube.fits"], "--order", 0, "--declared-unit", "Jy/beam", "--out", self.out("w.fits"))
        self.assertEqual(code, 0, r)
        h = fits.getheader(self.out("w.fits"))
        self.assertEqual(h["BUNIT"], "K m/s")
        self.assertNotIn("VESQBUNS", h)
        self.assertEqual(r["summary"]["unit_source"], "file")
        self.assertNotIn("declared by the author", r["summary"]["policy"])

    def test_cutout_slab_and_align_keep_the_unit_the_author_declared_and_say_so(self):
        runs = {"cutout": ("--x0", 0, "--x1", 3, "--y0", 0, "--y1", 3), "slab": ("--chan-start", 2, "--chan-end", 5),
                "align": ("--reference", self.f["hi_cube_coarser.fits"])}
        for op, extra in runs.items():
            code, bare = run(op, "--input", self.nounit, *extra, "--out", self.out(f"{op}0.fits"))
            self.assertEqual(code, 0, bare)
            self.assertNotIn("BUNIT", fits.getheader(self.out(f"{op}0.fits")), op)                    # nothing is invented without a declaration
            code, r = run(op, "--input", self.nounit, *extra, "--declared-unit", "K", "--out", self.out(f"{op}1.fits"))
            self.assertEqual(code, 0, r)
            h = fits.getheader(self.out(f"{op}1.fits"))
            self.assertEqual((h["BUNIT"], h["VESQBUNS"]), ("K", "user"), op)
            self.assertEqual(r["summary"]["unit_source"], "user", op)
            self.assertIn("declared by the author", r["summary"]["policy"], op)
            np.testing.assert_array_equal(fits.getdata(self.out(f"{op}0.fits")), fits.getdata(self.out(f"{op}1.fits")))
        code, r = run("cutout", "--input", self.f["hi_cube.fits"], "--x0", 0, "--x1", 3, "--y0", 0, "--y1", 3, "--declared-unit", "Jy", "--out", self.out("cf.fits"))
        self.assertEqual(code, 0, r)
        self.assertEqual(fits.getheader(self.out("cf.fits"))["BUNIT"], "K")                         # the file's unit, not the declared one
        self.assertNotIn("VESQBUNS", fits.getheader(self.out("cf.fits")))

    def scalar_map(self, name, unit):
        hd = fits.PrimaryHDU(np.arange(16, dtype=float).reshape(4, 4), header=g._map_header((4, 4)))
        if unit is not None:
            hd.header["BUNIT"] = unit
        path = self.out(name)
        fits.HDUList([hd]).writeto(path, overwrite=True)
        return path

    def test_scalar_difference_takes_declared_units_but_only_when_both_have_one_and_they_agree(self):
        bare_a, bare_b, k, jy = self.scalar_map("da.fits", None), self.scalar_map("db.fits", None), self.scalar_map("dk.fits", "K"), self.scalar_map("djy.fits", "Jy")
        code, r = run("scalar_diff", "--input", bare_a, "--reference", bare_b, "--declared-unit", "K", "--declared-unit-ref", "K", "--out", self.out("d1.fits"))
        self.assertEqual(code, 0, r)
        h = fits.getheader(self.out("d1.fits"))
        self.assertEqual((h["BUNIT"], h["VESQBUNS"]), ("K", "user"))
        self.assertIn("for the input and the reference", r["summary"]["policy"])
        code, r = run("scalar_diff", "--input", bare_a, "--reference", k, "--declared-unit", "K", "--out", self.out("d2.fits"))     # one from the file, one declared
        self.assertEqual(code, 0, r)
        self.assertIn("for the input;", r["summary"]["policy"])
        self.assertEqual(r["summary"]["unit_source"], "user")
        code, r = run("scalar_diff", "--input", k, "--reference", k, "--declared-unit", "Jy", "--declared-unit-ref", "Jy", "--out", self.out("d3.fits"))
        self.assertEqual(code, 0, r)                                                                 # both files state K: the declared Jy is ignored
        self.assertEqual((r["summary"]["unit"], r["summary"]["unit_source"]), ("K", "file"))
        self.assertNotIn("VESQBUNS", fits.getheader(self.out("d3.fits")))
        for args in (("--input", bare_a, "--reference", bare_b, "--declared-unit", "K"),            # the reference has no unit at all
                     ("--input", bare_a, "--reference", bare_b, "--declared-unit", "K", "--declared-unit-ref", "Jy"),   # declared units differ
                     ("--input", bare_a, "--reference", jy, "--declared-unit", "K")):                # a declared unit against a file's other unit
            code, r = run("scalar_diff", *args, "--out", self.out("never.fits"))
            self.assertEqual(code, 2, r)
            self.assertFalse(Path(self.out("never.fits")).exists())

    def test_a_declared_per_beam_unit_rescales_a_convolution_like_the_files_own_would(self):
        cov0 = np.eye(2) * 4.0
        bare = self.dir / "cbare.fits"
        beam_image(bare, cov0, beam=(2 * F, 2 * F, 0.0), bunit="Jy/beam")
        with fits.open(bare) as hd:
            hd[0].header.remove("BUNIT")
            hd.writeto(self.dir / "cbare_nounit.fits")
        own = self.dir / "cown.fits"
        beam_image(own, cov0, beam=(2 * F, 2 * F, 0.0), bunit="Jy/beam")
        target = ("--target-bmaj", 3 * F, "--target-bmin", 3 * F, "--target-bpa", 0.0)
        _, r_own = run("convolve", "--input", own, *target, "--out", self.out("c_own.fits"))
        code, r_decl = run("convolve", "--input", self.dir / "cbare_nounit.fits", *target, "--declared-unit", "Jy/beam", "--out", self.out("c_decl.fits"))
        self.assertEqual(code, 0, r_decl)
        np.testing.assert_array_equal(fits.getdata(self.out("c_own.fits")), fits.getdata(self.out("c_decl.fits")))
        self.assertTrue(r_decl["summary"]["rescaled_by_beam_area"])
        self.assertEqual(fits.getheader(self.out("c_decl.fits"))["VESQBUNS"], "user")
        # with no unit anywhere the values are smoothed as they are, and the result says that this is an assumption
        code, r = run("convolve", "--input", self.dir / "cbare_nounit.fits", *target, "--out", self.out("c_none.fits"))
        self.assertEqual(code, 0, r)
        self.assertFalse(r["summary"]["rescaled_by_beam_area"])
        self.assertEqual(r["summary"]["unit_source"], "unresolved")
        self.assertIn("no BUNIT and none was declared", r["summary"]["policy"])
        self.assertNotIn("BUNIT", fits.getheader(self.out("c_none.fits")))

    def test_a_malformed_declared_unit_is_refused_and_leaves_nothing(self):
        for bad in ("", " K", "K ", "x" * 41, "K\nm"):
            code, r = run("moment", "--input", self.nounit, "--order", 0, f"--declared-unit={bad}", "--out", self.out("bad.fits"))
            self.assertEqual(code, 2, (bad, r))
            self.assertFalse(Path(self.out("bad.fits")).exists())
        code, r = run("moment", "--input", self.nounit, "--order", 0, "--declared-unit=-K", "--out", self.out("dash.fits"))   # a leading dash is a value, not an option
        self.assertEqual(code, 0, r)
        self.assertEqual(fits.getheader(self.out("dash.fits"))["BUNIT"], "-K m/s")

    def test_the_unit_stays_the_authors_through_a_chain_of_analyses(self):
        code, r = run("cutout", "--input", self.nounit, "--x0", 0, "--x1", 9, "--y0", 0, "--y1", 9, "--declared-unit", "K", "--out", self.out("ch1.fits"))
        self.assertEqual(code, 0, r)
        for op, extra in (("moment", ("--order", 0)), ("slab", ("--chan-start", 1, "--chan-end", 4)), ("cutout", ("--x0", 0, "--x1", 4, "--y0", 0, "--y1", 4))):
            out = self.out(f"ch2_{op}.fits")
            code, r = run(op, "--input", self.out("ch1.fits"), *extra, "--out", out)          # no declaration is sent: the derived file has its own BUNIT
            self.assertEqual(code, 0, r)
            h = fits.getheader(out)
            self.assertEqual(h["VESQBUNS"], "user", op)
            self.assertEqual(r["summary"]["unit_source"], "user", op)
            self.assertIn("declared by the author", r["summary"]["policy"], op)
            self.assertEqual(sum("declared by the author" in str(x) for x in h["HISTORY"]), 1, op)      # said once, not once per step
        self.assertEqual(fits.getheader(self.out("ch2_moment.fits"))["BUNIT"], "K m/s")
        code, r = run("scalar_diff", "--input", self.out("ch1.fits"), "--reference", self.out("ch1.fits"), "--out", self.out("ch2_diff.fits"))
        self.assertNotEqual(code, 0)                                                    # cubes, not maps; the chain through a map is below
        m0 = self.out("ch2_moment.fits")
        code, r = run("scalar_diff", "--input", m0, "--reference", m0, "--out", self.out("ch3_diff.fits"))
        self.assertEqual(code, 0, r)
        self.assertEqual((r["summary"]["unit_source"], fits.getheader(self.out("ch3_diff.fits"))["VESQBUNS"]), ("user", "user"))

    def test_a_blank_bunit_is_unresolved_and_a_non_ascii_declared_unit_is_refused_by_name(self):
        with fits.open(self.nounit) as hd:
            hd[0].header["BUNIT"] = ""
            hd.writeto(self.dir / "blank.fits")
        code, r = run("moment", "--input", self.dir / "blank.fits", "--order", 0, "--declared-unit", "K", "--out", self.out("bl.fits"))
        self.assertEqual(code, 0, r)
        self.assertEqual(r["summary"]["unit_source"], "unresolved")                      # a blank card is no unit and the file's card still wins
        self.assertTrue(fits.getheader(self.out("bl.fits"))["BUNIT"].startswith("(brightness unit unresolved)"))
        code, r = run("moment", "--input", self.nounit, "--order", 0, "--declared-unit", "µJy/beam", "--out", self.out("nonascii.fits"))
        self.assertEqual(code, 2)
        self.assertIn("plain ASCII", r["error"])                                         # the declared text is named as the cause, not the file
        self.assertFalse(Path(self.out("nonascii.fits")).exists())

    def test_a_declared_per_beam_reading_is_stated_in_the_convolution_policy(self):
        bare = self.dir / "pb.fits"
        beam_image(bare, np.eye(2) * 4.0, beam=(2 * F, 2 * F, 0.0))
        with fits.open(bare) as hd:
            hd[0].header.remove("BUNIT")
            hd.writeto(self.dir / "pb_nounit.fits")
        target = ("--target-bmaj", 3 * F, "--target-bmin", 3 * F, "--target-bpa", 0.0)
        for unit, word in (("Jy/beam", "rescaled"), ("K", "not rescaled")):
            code, r = run("convolve", "--input", self.dir / "pb_nounit.fits", *target, "--declared-unit", unit, "--out", self.out("pbo.fits"))
            self.assertEqual(code, 0, r)
            self.assertIn(f'beam-area ratio): {word}.', r["summary"]["policy"])
