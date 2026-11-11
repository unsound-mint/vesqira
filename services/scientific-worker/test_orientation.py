"""Angle frames, quantities and axial resampling (ADR 0042), checked against answers computed in the tests themselves:
tangent-plane algebra for a CD matrix, a walk along the meridian with the inverse WCS, a published Galactic/ICRS angle, and
hand-made doubled-angle averages. astropy is the independent provider where the module under test uses it differently."""
import json
import math
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

import numpy as np
from astropy.io import fits
from astropy.wcs import WCS

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import orientation as ori  # noqa: E402
from test_plane_worker import Client  # noqa: E402

SCRIPT = str(HERE / "analysis.py")


def run(*args):
    p = subprocess.run([sys.executable, "-P", SCRIPT, *map(str, args)], capture_output=True, text=True, timeout=120)
    return p.returncode, json.loads(p.stdout)


def header(n=21, ctype=("RA---TAN", "DEC--TAN"), crval=(150.0, 2.0), cd=None, scale=1.0 / 60.0, extra=None):
    """A 2D header; default north up, east left, `scale` degrees per pixel, reference pixel at the middle."""
    h = fits.Header()
    h["CTYPE1"], h["CTYPE2"] = ctype
    h["CRVAL1"], h["CRVAL2"] = crval
    h["CRPIX1"] = h["CRPIX2"] = n // 2 + 1
    h["CUNIT1"] = h["CUNIT2"] = "deg"
    if cd is None:
        h["CDELT1"], h["CDELT2"] = -scale, scale
    else:
        for i in range(2):
            for j in range(2):
                h[f"CD{i + 1}_{j + 1}"] = float(cd[i][j])
    for k, v in (extra or {}).items():
        h[k] = v
    return h


def rot_cd(deg, scale=1.0 / 60.0):
    """CD matrix of a north-up, east-left grid with the pixel-to-sky map rotated by `deg` (north then lies at pixel angle 90 - deg)."""
    a = math.radians(deg)
    base = np.array([[-scale, 0.0], [0.0, scale]])
    r = np.array([[math.cos(a), -math.sin(a)], [math.sin(a), math.cos(a)]])
    return base @ r


def write(path, data, h):
    fits.PrimaryHDU(np.asarray(data, dtype="<f4"), header=h).writeto(path, overwrite=True)


def axial_dist(a, b):
    """Distance between two orientations in degrees, 0 to 90."""
    d = abs((a - b) % 180.0)
    return min(d, 180.0 - d)


class SkyAngle(unittest.TestCase):
    def wcs(self, **kw):
        return WCS(header(**kw)).celestial

    def test_north_up_grid_has_no_offset_and_east_is_left(self):
        w = self.wcs()
        # +y is north: a pixel-frame angle of 90 degrees is a position angle of 0
        self.assertLess(axial_dist(float(ori.pixel_to_sky_pa(w, 10, 10, 90.0)), 0.0), 1e-4)
        # +x is west on this grid (east is left): position angle 270, the same orientation as 90
        self.assertLess(abs(float(ori.pixel_to_sky_pa(w, 10, 10, 0.0)) - 270.0), 1e-4)
        # as an orientation: a B-field along +y is north-south (0), along +x is east-west (90)
        self.assertLess(axial_dist(float(ori.to_sky_b_field(w, 10, 10, 90.0, "pixel", "b_field")), 0.0), 1e-4)
        self.assertLess(axial_dist(float(ori.to_sky_b_field(w, 10, 10, 0.0, "pixel", "b_field")), 90.0), 1e-4)

    def test_a_rotated_cd_matrix_turns_north_accordingly(self):
        cd = rot_cd(30.0)
        w = self.wcs(cd=cd)
        for theta in (0.0, 37.0, 90.0, 123.0, 250.0):
            # tangent-plane algebra at the reference pixel: (east, north) = CD @ (dx, dy), position angle = atan2(east, north)
            e, n = cd @ np.array([math.cos(math.radians(theta)), math.sin(math.radians(theta))])
            want = math.degrees(math.atan2(e, n)) % 360.0
            got = float(ori.pixel_to_sky_pa(w, 10, 10, theta))
            self.assertLess(min(abs(got - want), 360.0 - abs(got - want)), 2e-3, (theta, got, want))
        # this CD turns the sky clockwise on the pixel grid by 30 degrees, so north (pixel angle 90 on the unturned grid) is at 60
        self.assertLess(abs(float(ori.sky_pa_to_pixel(w, 10, 10, 0.0)) - 60.0), 2e-3)

    def test_the_local_north_direction_follows_the_meridian_away_from_the_reference_pixel(self):
        # at 60 degrees declination the meridians converge: north is not +y away from the reference column
        h = header(n=81, crval=(150.0, 60.0), scale=0.25)
        w = WCS(h).celestial
        for (x, y) in ((5, 40), (70, 10), (40, 75)):
            lon, lat = w.pixel_to_world_values(x, y)
            # independent: walk 1e-3 degree north along the meridian and look where that lands in the pixel grid
            px, py = w.world_to_pixel_values(lon, lat + 1e-3)
            north_pixel_angle = math.degrees(math.atan2(py - y, px - x))
            self.assertLess(axial_dist(float(ori.to_sky_b_field(w, x, y, north_pixel_angle, "pixel", "b_field")), 0.0), 5e-3, (x, y))
        # the angle of north differs by several degrees between the left and right edges of the field
        a = float(ori.sky_pa_to_pixel(w, 5, 40, 0.0))
        b = float(ori.sky_pa_to_pixel(w, 75, 40, 0.0))
        self.assertGreater(abs(a - b), 5.0)

    def test_sky_means_east_of_north_in_the_files_own_celestial_frame(self):
        # a Galactic-frame file: its "sky" frame is Galactic, so north is the direction of increasing b
        h = header(n=61, ctype=("GLON-TAN", "GLAT-TAN"), crval=(120.0, 20.0), scale=0.2)
        w = WCS(h).celestial
        self.assertEqual(ori.sky_frame(w).name, "galactic")
        x, y = 12, 47
        l, b = w.pixel_to_world_values(x, y)
        px, py = w.world_to_pixel_values(l, b + 1e-3)
        north = math.degrees(math.atan2(py - y, px - x))
        self.assertLess(axial_dist(float(ori.to_sky_b_field(w, x, y, north, "pixel", "b_field")), 0.0), 5e-3)
        # a file already in the sky frame is used as it is, with only the quantity rotation
        self.assertAlmostEqual(float(ori.to_sky_b_field(w, x, y, 25.0, "sky", "b_field")), 25.0, places=9)

    def test_galactic_north_seen_in_icrs_is_58_6_degrees_at_the_galactic_centre(self):
        from astropy.coordinates import SkyCoord
        gc = SkyCoord(l=0.0, b=0.0, unit="deg", frame="galactic")
        pa = float(ori.rotate_pa_frame(gc, 0.0, SkyCoord(0, 0, unit="deg", frame="icrs").frame))
        self.assertLess(abs(pa - (360.0 - 58.6)), 0.1)       # Galactic north is 58.6 degrees west of celestial north at Sgr A* (a known value)
        back = float(ori.rotate_pa_frame(gc.icrs, pa, SkyCoord(0, 0, unit="deg", frame="galactic").frame))
        self.assertLess(axial_dist(back, 0.0), 1e-3)

    def test_the_quantity_decides_the_rotation_to_the_b_field(self):
        w = self.wcs()
        f = lambda a, q: float(ori.to_sky_b_field(w, 10, 10, a, "sky", q))
        self.assertAlmostEqual(f(30.0, "b_field"), 30.0)
        self.assertAlmostEqual(f(30.0, "starlight_polarization"), 30.0)       # parallel to B
        self.assertAlmostEqual(f(30.0, "gradient"), 120.0)                    # perpendicular to B
        self.assertAlmostEqual(f(100.0, "dust_polarization"), 10.0)           # perpendicular, wrapped into [0, 180)
        self.assertAlmostEqual(f(90.0, "gradient"), 0.0)                      # exactly 180 is written as 0
        self.assertTrue(0.0 <= f(179.999999, "gradient") < 180.0)

    def test_an_undeclared_frame_or_quantity_is_refused_by_name(self):
        w = self.wcs()
        for frame in (None, "unknown", ""):
            with self.assertRaisesRegex(ori.Undeclared, "frame"):
                ori.to_sky_b_field(w, 1, 1, 10.0, frame, "b_field")
        with self.assertRaisesRegex(ori.Undeclared, "unspecified"):
            ori.to_sky_b_field(w, 1, 1, 10.0, "sky", "unspecified")
        with self.assertRaisesRegex(ori.Undeclared, "quantity|what the angle is"):
            ori.to_sky_b_field(w, 1, 1, 10.0, "sky", None)


class DoubledAngle(unittest.TestCase):
    def test_one_and_179_degrees_average_to_zero_not_ninety(self):
        q, u = ori.doubled(np.array([1.0, 179.0]), np.array([1.0, 1.0]))
        th, r = ori.combine(np.array([q.mean()]), np.array([u.mean()]), np.array([1.0]))
        self.assertLess(axial_dist(float(th[0]), 0.0), 1e-9)
        self.assertAlmostEqual(float(r[0]), math.cos(math.radians(2.0)), places=9)
        self.assertGreater(abs((1.0 + 179.0) / 2 - float(th[0])), 80.0)        # the scalar mean is 90

    def test_orthogonal_orientations_cancel_and_are_flagged_undefined(self):
        q, u = ori.doubled(np.array([0.0, 90.0]), np.array([1.0, 1.0]))
        th, r = ori.combine(np.array([q.mean()]), np.array([u.mean()]), np.array([1.0]))
        self.assertLess(float(r[0]), 1e-6)
        self.assertTrue(np.isnan(th[0]))

    def test_no_value_stays_no_value_and_zero_weight_is_not_missing(self):
        q, u = ori.doubled(np.array([10.0, np.nan, 10.0]), np.array([1.0, 1.0, 0.0]))
        self.assertTrue(np.isnan(q[1]) and np.isfinite(q[2]) and q[2] == 0.0)
        th, r = ori.combine(q, u, np.array([1.0, np.nan, 0.0]))
        self.assertAlmostEqual(float(th[0]), 10.0, places=9)
        self.assertTrue(np.isnan(th[1]) and np.isnan(th[2]) and np.isnan(r[2]))  # nothing contributed at zero total weight


class WorkerOp(unittest.TestCase):
    """The orientation_sky request, end to end through the line protocol."""

    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        cls.dir = Path(cls.tmp.name)
        cls.c = Client()
        n = 9
        data = np.full((n, n), 30.0)
        data[4, 4] = np.nan
        cls.path = cls.dir / "psi.fits"
        write(cls.path, data, header(n=n, cd=rot_cd(30.0)))
        cls.rad = cls.dir / "psi_rad.fits"
        write(cls.rad, np.full((n, n), math.radians(30.0)), header(n=n))

    @classmethod
    def tearDownClass(cls):
        cls.c.close()
        cls.tmp.cleanup()

    def ask(self, path=None, **kw):
        req = dict(op="orientation_sky", path=str(path or self.path), x=2, y=3, unit="deg", frame="pixel", quantity="b_field")
        req.update(kw)
        return self.c.ask(**req)[0]

    def test_a_pixel_frame_angle_becomes_sky_b_field_through_the_wcs(self):
        r = self.ask()
        self.assertTrue(r["ok"], r)
        cd = rot_cd(30.0)
        e, n = cd @ np.array([math.cos(math.radians(30.0)), math.sin(math.radians(30.0))])
        want = math.degrees(math.atan2(e, n)) % 180.0
        self.assertLess(axial_dist(r["sky_b_field_deg"], want), 0.5)         # tangent-plane value; the pixel is a few pixels from the reference
        self.assertEqual(r["stored"], 30.0)
        self.assertIn("east of north", r["method"])

    def test_the_quantity_is_applied_and_radians_are_converted(self):
        a = self.ask(frame="sky")["sky_b_field_deg"]
        b = self.ask(frame="sky", quantity="gradient")["sky_b_field_deg"]
        self.assertAlmostEqual(a, 30.0, places=4)
        self.assertAlmostEqual(b, 120.0, places=4)
        r = self.ask(self.rad, unit="rad", frame="sky")
        self.assertAlmostEqual(r["sky_b_field_deg"], 30.0, places=4)

    def test_a_missing_declaration_is_refused_and_named(self):
        r = self.ask(frame=None)
        self.assertFalse(r["ok"])
        self.assertIn("frame", r["error"])
        r = self.ask(quantity="unspecified")
        self.assertFalse(r["ok"])
        self.assertIn("unspecified", r["error"])
        r = self.ask(quantity=None)
        self.assertFalse(r["ok"])
        self.assertIn("what the angle is", r["error"])

    def test_a_pixel_without_a_value_is_null_not_zero(self):
        r = self.ask(x=4, y=4)
        self.assertTrue(r["ok"])
        self.assertIsNone(r["sky_b_field_deg"])
        self.assertIsNone(r["stored"])


class AxialResampling(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        cls.dir = Path(cls.tmp.name)

    @classmethod
    def tearDownClass(cls):
        cls.tmp.cleanup()

    def p(self, name):
        return self.dir / name

    def axial_align(self, name, src, ref, unit="deg", frame="sky", method="interp", weights=None):
        args = ["align", "--input", src, "--reference", ref, "--out", self.p(name + ".fits"), "--rel-out", self.p(name + "_rel.fits"),
                "--axial-unit", unit, "--method", method]
        if frame:
            args += ["--axial-frame", frame]
        if weights:
            args += ["--weights", weights]
        code, r = run(*args)
        return code, r

    def test_neighbours_of_one_and_179_degrees_average_to_zero_and_not_ninety(self):
        n = 12
        data = np.zeros((n, n))
        data[:, ::2], data[:, 1::2] = 1.0, 179.0                              # alternate columns
        write(self.p("alt.fits"), data, header(n=n))
        h = header(n=n)
        h["CRPIX1"] = float(h["CRPIX1"]) + 0.5                                # the reference grid sits half a pixel from the input's
        write(self.p("alt_ref.fits"), np.zeros((n, n)), h)
        code, r = self.axial_align("alt", self.p("alt.fits"), self.p("alt_ref.fits"))
        self.assertEqual(code, 0, r)
        out = fits.getdata(self.p("alt.fits")).astype(float)
        inner = out[2:-2, 2:-2]
        self.assertTrue(np.isfinite(inner).all())
        self.assertLess(max(axial_dist(v, 0.0) for v in inner.ravel()), 1e-3)
        rel = fits.getdata(self.p("alt_rel.fits")).astype(float)[2:-2, 2:-2]
        self.assertLess(abs(rel.mean() - math.cos(math.radians(2.0))), 1e-3)  # |mean of the two unit vectors|
        self.assertTrue(r["summary"]["reliability_file"])
        self.assertIn("Doubled-angle", r["summary"]["policy"])
        self.assertIn("doubled angle", fits.getheader(self.p("alt.fits")).tostring())

    def test_an_orthogonal_pair_has_no_mean_orientation_and_is_counted(self):
        n = 12
        data = np.zeros((n, n))
        data[:, ::2], data[:, 1::2] = 0.0, 90.0
        write(self.p("orth.fits"), data, header(n=n))
        h = header(n=n)
        h["CRPIX1"] = float(h["CRPIX1"]) + 0.5
        write(self.p("orth_ref.fits"), np.zeros((n, n)), h)
        code, r = self.axial_align("orth", self.p("orth.fits"), self.p("orth_ref.fits"))
        self.assertEqual(code, 0, r)
        out = fits.getdata(self.p("orth.fits")).astype(float)[2:-2, 2:-2]
        rel = fits.getdata(self.p("orth_rel.fits")).astype(float)[2:-2, 2:-2]
        self.assertLess(rel.max(), 1e-5)
        self.assertTrue(np.isnan(out).all())                                  # undefined, never an invented angle
        self.assertGreaterEqual(r["summary"]["pixels_orientation_undefined"], out.size)

    def test_a_uniform_north_pointing_field_stays_north_on_a_rotated_reference_grid(self):
        n = 31
        write(self.p("north.fits"), np.full((n, n), 90.0), header(n=n))      # pixel +y is north: 90 degrees in the pixel frame
        cd = rot_cd(30.0)
        write(self.p("rot_ref.fits"), np.zeros((n, n)), header(n=n, cd=cd))
        for method in ("interp", "exact", "nearest"):
            code, r = self.axial_align("rot_" + method, self.p("north.fits"), self.p("rot_ref.fits"), frame="pixel", method=method)
            self.assertEqual(code, 0, r)
            out = fits.getdata(self.p(f"rot_{method}.fits")).astype(float)
            inner = out[8:-8, 8:-8]
            self.assertTrue(np.isfinite(inner).all(), method)
            # north on the reference grid points along CD^-1 @ (0, 1) in pixels: that is the pixel-frame angle to expect
            dx, dy = np.linalg.solve(cd, np.array([0.0, 1.0]))
            want = math.degrees(math.atan2(dy, dx)) % 180.0
            self.assertLess(max(axial_dist(v, want) for v in inner.ravel()), 0.05, method)
            self.assertGreater(axial_dist(want, 90.0), 25.0)                  # it did change, as it must: the grid is turned
            rel = fits.getdata(self.p(f"rot_{method}_rel.fits")) if r["summary"]["reliability_file"] else None
            if method != "nearest":
                self.assertGreater(float(np.nanmin(rel[8:-8, 8:-8])), 0.999)
            else:
                self.assertIsNone(rel)                                         # nothing was averaged and none was declared

    def test_a_field_in_another_celestial_frame_is_rotated_into_the_references(self):
        from astropy.coordinates import SkyCoord
        n = 21
        gal = header(n=n, ctype=("GLON-TAN", "GLAT-TAN"), crval=(0.0, 0.0), scale=0.1)
        write(self.p("gal_north.fits"), np.zeros((n, n)), gal)                # sky frame, 0 = Galactic north everywhere
        wg = WCS(gal).celestial
        c = SkyCoord(l=0.0, b=0.0, unit="deg", frame="galactic").icrs
        ref = header(n=n, crval=(float(c.ra.deg), float(c.dec.deg)), scale=0.1)
        write(self.p("icrs_ref.fits"), np.zeros((n, n)), ref)
        code, r = self.axial_align("gal2icrs", self.p("gal_north.fits"), self.p("icrs_ref.fits"), frame="sky")
        self.assertEqual(code, 0, r)
        out = fits.getdata(self.p("gal2icrs.fits")).astype(float)
        wr = WCS(ref).celestial
        for (x, y) in ((10, 10), (12, 8), (8, 13)):
            here = wr.pixel_to_world(x, y)                                     # the point in the reference, in ICRS
            g = here.galactic
            north_g = SkyCoord(l=g.l.deg, b=g.b.deg + 1e-3, unit="deg", frame="galactic")
            want = float(here.position_angle(north_g.icrs).deg) % 180.0       # independent: where Galactic north points in ICRS
            self.assertLess(axial_dist(out[y, x], want), 0.1, (x, y))
        self.assertGreater(axial_dist(out[10, 10], 0.0), 50.0)                 # it is not the unrotated 0
        self.assertEqual(r["summary"]["source_frame"], "Galactic")

    def test_a_declared_reliability_weights_the_average_and_zero_weight_counts_for_nothing(self):
        n = 12
        data = np.zeros((n, n))
        data[:, ::2], data[:, 1::2] = 10.0, 100.0                              # these two disagree by 90 degrees
        w = np.ones((n, n))
        w[:, 1::2] = 0.0                                                       # but the second has no weight
        write(self.p("wt.fits"), data, header(n=n))
        write(self.p("wt_w.fits"), w, header(n=n))
        h = header(n=n)
        h["CRPIX1"] = float(h["CRPIX1"]) + 0.5
        write(self.p("wt_ref.fits"), np.zeros((n, n)), h)
        code, r = self.axial_align("wt", self.p("wt.fits"), self.p("wt_ref.fits"), weights=self.p("wt_w.fits"))
        self.assertEqual(code, 0, r)
        out = fits.getdata(self.p("wt.fits")).astype(float)[2:-2, 2:-2]
        self.assertLess(max(axial_dist(v, 10.0) for v in out.ravel()), 1e-3)
        rel = fits.getdata(self.p("wt_rel.fits")).astype(float)[2:-2, 2:-2]
        self.assertGreater(rel.min(), 0.999)                                   # the only contributing orientation: R is 1
        self.assertTrue(r["summary"]["declared_reliability_used"])

    def test_radians_in_radians_out_and_a_missing_frame_is_refused_by_name(self):
        n = 12
        write(self.p("rad.fits"), np.full((n, n), math.radians(10.0)), header(n=n))
        write(self.p("rad_ref.fits"), np.zeros((n, n)), header(n=n))
        code, r = self.axial_align("rad", self.p("rad.fits"), self.p("rad_ref.fits"), unit="rad")
        self.assertEqual(code, 0, r)
        out = fits.getdata(self.p("rad.fits")).astype(float)[2:-2, 2:-2]
        self.assertLess(np.abs(out - math.radians(10.0)).max(), 1e-5)
        self.assertLess(out.max(), math.pi)
        code, r = self.axial_align("nofr", self.p("rad.fits"), self.p("rad_ref.fits"), unit="rad", frame=None)
        self.assertEqual(code, 2)
        self.assertIn("frame", r["error"])
        self.assertFalse(self.p("nofr.fits").exists())

    def test_moment_and_slab_of_an_axial_map_are_refused_with_a_reason(self):
        n = 8
        cube = np.zeros((4, n, n))
        h = header(n=n)
        h["CTYPE3"], h["CRVAL3"], h["CRPIX3"], h["CDELT3"], h["CUNIT3"] = "VRAD", 0.0, 1.0, 1.0, "m/s"
        fits.PrimaryHDU(cube.astype("<f4"), header=h).writeto(self.p("c.fits"), overwrite=True)
        for op in ("moment", "slab"):
            code, r = run(op, "--input", self.p("c.fits"), "--out", self.p(op + ".fits"), "--axial-unit", "deg")
            self.assertEqual(code, 2, op)
            self.assertIn("axial", r["error"])
            self.assertFalse(self.p(op + ".fits").exists())

    def test_a_pixel_frame_map_with_a_rotated_north_matches_the_sky_route_on_an_identity_align(self):
        # aligning onto its own grid must give the input back, in either frame
        n = 15
        cd = rot_cd(20.0)
        rng = np.random.default_rng(3)
        data = rng.uniform(0.0, 180.0, (n, n))
        write(self.p("self.fits"), data, header(n=n, cd=cd))
        code, r = self.axial_align("self_out", self.p("self.fits"), self.p("self.fits"), frame="pixel", method="interp")
        self.assertEqual(code, 0, r)
        out = fits.getdata(self.p("self_out.fits")).astype(float)
        diff = [axial_dist(a, b) for a, b in zip(out[3:-3, 3:-3].ravel(), data[3:-3, 3:-3].ravel())]
        self.assertLess(max(diff), 0.05)


def beam_header(n, extra=None):
    h = header(n=n, scale=1.0 / 3600.0, extra={"BMAJ": 2.0 / 3600.0, "BMIN": 2.0 / 3600.0, "BPA": 0.0})
    for k, v in (extra or {}).items():
        h[k] = v
    return h


class AxialConvolution(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        cls.dir = Path(cls.tmp.name)

    @classmethod
    def tearDownClass(cls):
        cls.tmp.cleanup()

    def convolve(self, name, data, extra=None, weights=None, unit="deg"):
        n = data.shape[0]
        write(self.dir / (name + ".fits"), data, beam_header(n))
        args = ["convolve", "--input", self.dir / (name + ".fits"), "--out", self.dir / (name + "_out.fits"), "--rel-out", self.dir / (name + "_rel.fits"),
                "--target-bmaj", 4.0, "--target-bmin", 4.0, "--target-bpa", 0.0, "--axial-unit", unit, "--axial-frame", "pixel"]
        if weights is not None:
            write(self.dir / (name + "_w.fits"), weights, beam_header(n))
            args += ["--weights", self.dir / (name + "_w.fits")]
        code, r = run(*args)
        return code, r, fits.getdata(self.dir / (name + "_out.fits")).astype(float)

    def test_a_uniform_field_keeps_its_angle_everywhere_including_the_edges(self):
        n = 41
        code, r, out = self.convolve("uni", np.full((n, n), 10.0))
        self.assertEqual(code, 0, r)
        self.assertTrue(np.isfinite(out).all())
        self.assertLess(np.abs(out - 10.0).max(), 1e-3)
        rel = fits.getdata(self.dir / "uni_rel.fits").astype(float)
        self.assertGreater(rel.min(), 0.9999)
        self.assertLessEqual(rel.max(), 1.0)
        self.assertIn("Doubled-angle", r["summary"]["policy"])

    def test_a_field_that_wraps_through_zero_is_not_averaged_through_ninety(self):
        n = 41
        data = np.full((n, n), 1.0)
        data[::2, :] = 179.0                                                   # alternating rows of 1 and 179 degrees
        code, r, out = self.convolve("wrap", data)
        self.assertEqual(code, 0, r)
        inner = out[10:-10, 10:-10]
        self.assertLess(max(axial_dist(v, 0.0) for v in inner.ravel()), 0.2)   # the scalar mean would be 90
        self.assertGreater(np.nanmin(fits.getdata(self.dir / "wrap_rel.fits")[10:-10, 10:-10]), 0.99)

    def test_missing_pixels_stay_missing_and_are_not_zero(self):
        n = 41
        data = np.full((n, n), 25.0)
        data[18:22, 18:22] = np.nan
        code, r, out = self.convolve("gap", data)
        self.assertEqual(code, 0, r)
        self.assertTrue(np.isnan(out[19, 19]))
        self.assertLess(np.nanmax(np.abs(out - 25.0)), 1e-3)                   # neighbours are averaged over the finite pixels only

    def test_an_axial_map_is_not_rescaled_by_the_beam_area_ratio(self):
        n = 21
        h = beam_header(n, {"BUNIT": "Jy/beam"})
        write(self.dir / "jy.fits", np.full((n, n), 10.0), h)
        code, r = run("convolve", "--input", self.dir / "jy.fits", "--out", self.dir / "jy_out.fits", "--target-bmaj", 4.0, "--target-bmin", 4.0,
                      "--target-bpa", 0.0, "--axial-unit", "deg")
        self.assertEqual(code, 0, r)
        self.assertLess(np.abs(fits.getdata(self.dir / "jy_out.fits") - 10.0).max(), 1e-3)


if __name__ == "__main__":
    unittest.main()
