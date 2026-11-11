"""Linked views (ADR 0042, section 4) against answers computed independently in the tests.

Expected pixels come from astropy used directly here (WCS and SkyCoord on headers the tests write); expected channels from
astropy's Doppler equivalencies on the channel values the tests choose. The worker is driven through its line protocol, as
the API drives it.
"""
import sys
import tempfile
import unittest
from pathlib import Path

import numpy as np
import astropy.units as u
from astropy.coordinates import SkyCoord
from astropy.io import fits
from astropy.wcs import WCS

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
from test_orientation import header, rot_cd  # noqa: E402
from test_plane_worker import Client  # noqa: E402

HI = 1.420405751768e9
CO = 115.271202e9


def galactic_of(ra, dec):
    g = SkyCoord(ra * u.deg, dec * u.deg, frame="icrs").galactic
    return float(g.l.deg), float(g.b.deg)


def cube(directory, name, *, n=41, nc=12, sky=None, spec=("VRAD", 0.0, 1000.0, "m/s"), specsys="LSRK", rest=HI, crpix3=1.0, extra=None):
    """A small 3D cube: celestial axes from `sky` (header() keywords), a spectral axis, one value per voxel."""
    h = header(n=n, **(sky or {}))
    h["NAXIS"] = 3
    ctype, crval, cdelt, cunit = spec
    h["CTYPE3"], h["CRVAL3"], h["CDELT3"], h["CRPIX3"], h["CUNIT3"] = ctype, crval, cdelt, crpix3, cunit
    if specsys:
        h["SPECSYS"] = specsys
    if rest:
        h["RESTFRQ"] = rest
    for k, v in (extra or {}).items():
        h[k] = v
    path = directory / name
    fits.PrimaryHDU(np.zeros((nc, n, n), dtype="<f4"), header=h).writeto(path, overwrite=True)
    return path


class Link(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        cls.d = Path(cls.tmp.name)
        cls.c = Client()

    @classmethod
    def tearDownClass(cls):
        cls.c.close()
        cls.tmp.cleanup()

    def link(self, a, b, **kw):
        head, _ = self.c.ask(op="link", path=str(a), other_path=str(b), **kw)
        self.assertTrue(head["ok"], head)
        return head

    @staticmethod
    def expected_pixels(ha, hb, pts):
        """A pixel -> B pixel by astropy alone: pixel_to_world, SkyCoord.transform_to B's frame, world_to_pixel."""
        wa, wb = WCS(ha).celestial, WCS(hb).celestial
        sc = wa.pixel_to_world(np.array([p[0] for p in pts], float), np.array([p[1] for p in pts], float))
        fb = SkyCoord(0 * u.deg, 0 * u.deg, frame="galactic" if hb["CTYPE1"].startswith("GLON") else "icrs").frame
        x, y = wb.world_to_pixel(sc.transform_to(fb))
        return np.column_stack([x, y])

    def pair(self, name, hb_kw, n_b=31):
        a = cube(self.d, f"{name}_a.fits")
        b = cube(self.d, f"{name}_b.fits", n=n_b, sky=hb_kw)
        return a, b, fits.getheader(a), fits.getheader(b)

    PTS = [[0, 0], [40, 0], [0, 40], [40, 40], [20, 20], [7.25, 31.5], [33, 11]]

    def test_icrs_to_galactic_cursor_matches_astropy(self):
        l, b = galactic_of(150.0, 2.0)
        a, bb, ha, hb = self.pair("gal", dict(ctype=("GLON-TAN", "GLAT-TAN"), crval=(l, b), scale=1.2 / 60.0))
        head = self.link(a, bb, region={"x0": -0.5, "x1": 40.5, "y0": -0.5, "y1": 40.5}, points=self.PTS)
        sky = head["sky"]
        self.assertIsNone(sky["refused"])
        self.assertEqual((sky["frames"]["from"], sky["frames"]["to"]), ("ICRS", "Galactic"))
        want = self.expected_pixels(ha, hb, self.PTS)
        got = np.array([[e["bx"], e["by"]] for e in sky["exact"]])
        self.assertLess(np.abs(got - want).max(), 0.5)
        self.assertLess(np.abs(got - want).max(), 1e-3)
        # the affine the browser applies lands within its stated error of the exact answer
        m = sky["affine"]
        app = np.array([[m[0] * x + m[1] * y + m[2], m[3] * x + m[4] * y + m[5]] for x, y in self.PTS])
        self.assertLessEqual(np.abs(app - want).max(), sky["max_error_px"] + 1e-6)
        self.assertIn("ICRS to Galactic", sky["basis"])

    def test_rotated_grid(self):
        a, b, ha, hb = self.pair("rot", dict(cd=rot_cd(30.0, 1.0 / 60.0), crval=(150.01, 2.02)))
        sky = self.link(a, b, region={"x0": -0.5, "x1": 40.5, "y0": -0.5, "y1": 40.5}, points=self.PTS)["sky"]
        want = self.expected_pixels(ha, hb, self.PTS)
        got = np.array([[e["bx"], e["by"]] for e in sky["exact"]])
        self.assertLess(np.abs(got - want).max(), 0.5)
        m = sky["affine"]
        # a 30 degree rotation is in the linear part: the affine is not the identity map
        self.assertGreater(abs(m[1]), 0.3)
        self.assertLess(sky["max_error_px"], 0.5)
        self.assertTrue(sky["frames"]["same_frame"])

    def test_outside_the_other_footprint(self):
        a, b, ha, hb = self.pair("far", dict(crval=(150.0, 2.0)), n_b=11)         # 11 px, 1 arcmin: a small field at the centre of A
        sky = self.link(a, b, region={"x0": -0.5, "x1": 40.5, "y0": -0.5, "y1": 40.5}, points=[[20, 20], [0, 0], [40, 40]])["sky"]
        inside = [e["inside"] for e in sky["exact"]]
        self.assertEqual(inside, [True, False, False])
        want = self.expected_pixels(ha, hb, [[0, 0]])
        self.assertAlmostEqual(sky["exact"][1]["bx"], want[0][0], places=4)       # still reported, so the browser can say where

    def test_affine_error_bound_is_respected_over_the_region(self):
        l, b = galactic_of(175.0, 12.0)                                             # a different tangent point: the map between them is not linear
        a = cube(self.d, "wide_a.fits", n=101, sky=dict(scale=1.0))                 # a 100 degree field: not linear in Galactic
        bb = cube(self.d, "wide_b.fits", n=101, sky=dict(ctype=("GLON-TAN", "GLAT-TAN"), crval=(l, b), scale=1.0))
        ha, hb = fits.getheader(a), fits.getheader(bb)
        gx, gy = np.meshgrid(np.linspace(-0.5, 100.5, 57), np.linspace(-0.5, 100.5, 57))
        dense = np.column_stack([gx.ravel(), gy.ravel()]).tolist()
        for width in (100.0, 20.0, 4.0):
            r = {"x0": 50 - width / 2, "x1": 50 + width / 2, "y0": 50 - width / 2, "y1": 50 + width / 2}
            sky = self.link(a, bb, region=r)["sky"]
            self.assertIsNone(sky["refused"])
            inside = [p for p in dense if r["x0"] <= p[0] <= r["x1"] and r["y0"] <= p[1] <= r["y1"]]
            want = self.expected_pixels(ha, hb, inside)
            m = sky["affine"]
            app = np.array([[m[0] * x + m[1] * y + m[2], m[3] * x + m[4] * y + m[5]] for x, y in inside])
            worst = np.hypot(*(app - want).T).max()
            self.assertLessEqual(worst, sky["max_error_px"] * 1.02 + 1e-6, (width, worst, sky["max_error_px"]))
            if width == 100.0:
                big = sky["max_error_px"]
                self.assertGreater(big, 0.5)                                         # the stated error is not flattering
            if width == 4.0:
                self.assertLess(sky["max_error_px"], big / 100)                     # re-requested over a smaller view, it shrinks

    def test_refusals(self):
        a = cube(self.d, "r_a.fits")
        nowcs = self.d / "r_flat.fits"
        fits.PrimaryHDU(np.zeros((4, 4), dtype="<f4")).writeto(nowcs, overwrite=True)
        sky = self.link(a, nowcs, region={"x0": 0, "x1": 5, "y0": 0, "y1": 5})["sky"]
        self.assertIn("celestial", sky["refused"])
        head, _ = self.c.ask(op="link", path=str(a), other_path=str(a), region={"x0": 0, "x1": "x", "y0": 0, "y1": 5})
        self.assertFalse(head["ok"])
        head, _ = self.c.ask(op="link", path=str(a), other_path=str(a), points=[[0, 0]] * 65)
        self.assertFalse(head["ok"])

    # ---- channels ----

    def chan(self, a, b):
        head = self.link(a, b, channels=True)
        self.assertIsNone(head["sky"])
        return head["spectral"]

    def test_velocity_to_frequency_with_residual(self):
        # A: radio velocity from -5 km/s in 1 km/s steps; B: frequency channels 25 kHz apart (about 5.3 km/s), not aligned to A
        a = cube(self.d, "v_a.fits", nc=12, spec=("VRAD", -5000.0, 1000.0, "m/s"))
        f0 = HI * (1 - (-6000.0) / 299792458.0)
        b = cube(self.d, "f_b.fits", nc=2, spec=("FREQ", f0, -25000.0, "Hz"))
        s = self.chan(a, b)
        self.assertIsNone(s["refused"])
        self.assertEqual(s["unit"], "m/s")
        rest = HI * u.Hz
        va = (-5000.0 + 1000.0 * np.arange(12)) * u.m / u.s
        fb = (f0 - 25000.0 * np.arange(2)) * u.Hz
        vb = fb.to(u.m / u.s, u.doppler_radio(rest))                               # B's channels as radio velocities, by astropy
        half = 0.5 * abs(float((vb[1] - vb[0]).value))
        for i in range(12):
            j = int(np.argmin(np.abs(vb - va[i])))
            self.assertEqual(s["index"][i], j, i)
            res = float((vb[j] - va[i]).value)
            self.assertAlmostEqual(s["residual"][i], res, places=6)
            self.assertEqual(s["outside"][i], abs(res) > half, i)                  # beyond B's range: reported, not clamped silently
        self.assertTrue(any(s["outside"]) and not all(s["outside"]))
        self.assertIn("doppler_radio", s["basis"])

    def test_frequency_axis_runs_backwards_and_nearest_is_by_value(self):
        a = cube(self.d, "fa.fits", nc=6, spec=("FREQ", 1.4204e9, 5000.0, "Hz"))
        b = cube(self.d, "fb.fits", nc=6, spec=("FREQ", 1.4204e9 + 5 * 5000.0, -5000.0, "Hz"))      # same channels, reversed
        s = self.chan(a, b)
        self.assertEqual(s["index"], [5, 4, 3, 2, 1, 0])
        self.assertTrue(all(abs(r) < 1e-6 for r in s["residual"]))
        self.assertFalse(any(s["outside"]))

    def test_velocity_axes_link_as_velocities_whatever_the_line(self):
        # H I against CO: different rest frequencies, but each file's own line: the velocity is what links
        a = cube(self.d, "hi_v.fits", nc=8, spec=("VRAD", 0.0, 2000.0, "m/s"), rest=HI)
        b = cube(self.d, "co_v.fits", nc=8, spec=("VRAD", 4000.0, 1000.0, "m/s"), rest=CO)
        s = self.chan(a, b)
        self.assertIsNone(s["refused"])
        self.assertEqual(s["index"][:4], [0, 0, 0, 2])      # v = 0, 2, 4, 6 km/s -> B channel at 4 km/s is index 0
        self.assertEqual(s["index"][3], 2)
        self.assertEqual([round(r) for r in s["residual"][:4]], [4000, 2000, 0, 0])
        self.assertTrue(s["outside"][0] and not s["outside"][3])
        self.assertIn("no rest frequency is used", s["basis"])

    def test_specsys_differs_is_refused(self):
        a = cube(self.d, "s_a.fits", specsys="LSRK")
        b = cube(self.d, "s_b.fits", specsys="TOPOCENT")
        s = self.chan(a, b)
        self.assertEqual(s["refused"], "spectral frames differ (LSRK vs TOPOCENT); changing frame is not offered")
        self.assertIsNone(s["index"])

    def test_a_missing_specsys_is_not_the_same_frame(self):
        a = cube(self.d, "n_a.fits", specsys=None)
        b = cube(self.d, "n_b.fits", specsys="LSRK")
        self.assertIn("not stated by the first map", self.chan(a, b)["refused"])
        self.assertIn("not stated by either map", self.chan(a, cube(self.d, "n_c.fits", specsys=None))["refused"])

    def test_conventions_rest_frequencies_and_legacy_axes(self):
        vrad = cube(self.d, "c_vrad.fits", spec=("VRAD", 0.0, 1000.0, "m/s"))
        vopt = cube(self.d, "c_vopt.fits", spec=("VOPT", 0.0, 1000.0, "m/s"))
        self.assertIn("conventions differ (radio vs optical)", self.chan(vrad, vopt)["refused"])
        freq_other = cube(self.d, "c_f.fits", spec=("FREQ", 1.42e9, 1e4, "Hz"), rest=CO)
        self.assertIn("different rest frequencies", self.chan(vrad, freq_other)["refused"])
        freq_norest = cube(self.d, "c_fn.fits", spec=("FREQ", 1.42e9, 1e4, "Hz"), rest=None)
        ok = self.chan(vrad, freq_norest)
        self.assertIsNone(ok["refused"])
        self.assertIn("states none, so this one is used", ok["basis"])
        vel_norest = cube(self.d, "c_vn.fits", spec=("VRAD", 0.0, 1000.0, "m/s"), rest=None)
        self.assertIn("no rest frequency", self.chan(vel_norest, freq_norest)["refused"])
        legacy = cube(self.d, "c_leg.fits", spec=("VELO-LSR", 0.0, 1000.0, "m/s"), specsys="LSRK")
        legacy2 = cube(self.d, "c_leg2.fits", spec=("VELO-LSR", 500.0, 1000.0, "m/s"), specsys="LSRK")
        self.assertIn("legacy", self.chan(legacy, vrad)["refused"])                # no declared convention: only links to another legacy axis
        self.assertIn("legacy", self.chan(legacy, freq_norest)["refused"])
        both = self.chan(legacy, legacy2)
        self.assertIsNone(both["refused"])                                         # velocity to velocity, a unit change only
        self.assertEqual(both["index"][:3], [0, 0, 1])

    def test_no_spectral_axis_has_no_channel_link(self):
        flat = self.d / "flat2d.fits"
        fits.PrimaryHDU(np.zeros((21, 21), dtype="<f4"), header=header(n=21)).writeto(flat, overwrite=True)
        s = self.chan(cube(self.d, "p_a.fits"), flat)
        self.assertIn("no spectral axis", s["refused"])


if __name__ == "__main__":
    unittest.main()
