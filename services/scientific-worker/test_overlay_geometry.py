"""Overlay geometry (ADR 0042, section 3) against answers computed independently in the tests.

The expected positions come from astropy used directly here (SkyCoord and WCS on headers written by the tests), the
expected angles from walking a sky direction with the inverse WCS, and the expected noise from numpy's median. The worker
is driven through its line protocol, as the API drives it.
"""
import math
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
import overlay_geometry as og  # noqa: E402
from test_orientation import header, rot_cd, write, axial_dist  # noqa: E402
from test_plane_worker import Client  # noqa: E402

SCALE = 1.0 / 60.0   # 1 arcmin pixels


def read_paths(head, payload):
    """[(group, (n, 2) array)] from a paths payload."""
    a = np.frombuffer(payload, dtype="<f4")
    n = head["paths"]
    table = a[: 2 * n].reshape(n, 2)
    verts = a[2 * n:].reshape(-1, 2)
    assert len(verts) == head["vertices"]
    out, at = [], 0
    for g, c in table:
        out.append((int(g), verts[at:at + int(c)].astype(float)))
        at += int(c)
    return out


def blob(n, cx, cy, sigma=2.5, peak=10.0):
    yy, xx = np.mgrid[0:n, 0:n]
    return peak * np.exp(-(((xx - cx) ** 2 + (yy - cy) ** 2) / (2 * sigma ** 2)))


def centroid(paths):
    pts = np.concatenate([p for _, p in paths])
    return pts.mean(axis=0)


class Geometry(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        cls.d = Path(cls.tmp.name)
        cls.c = Client()

    @classmethod
    def tearDownClass(cls):
        cls.c.close()
        cls.tmp.cleanup()

    def ask(self, base, overlay, spec, **extra):
        head, payload = self.c.ask(op="overlay_geometry", path=str(base), overlay_path=str(overlay), overlay=spec, **extra)
        return head, payload

    def file(self, name, data, h):
        p = self.d / name
        write(p, data, h)
        return p

    # ---------- contours ----------

    def test_contour_of_a_blob_lands_at_the_same_sky_position_on_a_rotated_offset_grid(self):
        n = 41
        ha = header(n=n, extra={"RADESYS": "ICRS", "BUNIT": "K"})
        a = self.file("blob_a.fits", blob(n, 25.0, 14.0), ha)
        # grid B: rotated 35 degrees, offset by 4 arcmin, finer pixels
        hb = header(n=61, crval=(150.0 + 4 / 60.0, 2.0 - 3 / 60.0), cd=rot_cd(35.0, SCALE * 0.7), extra={"RADESYS": "ICRS", "BUNIT": "K"})
        b = self.file("grid_b.fits", np.zeros((61, 61)), hb)
        head, payload = self.ask(b, a, {"kind": "contours", "channel": 0, "levels": {"mode": "list", "values": [1.0, 5.0]}, "unit": "K"})
        self.assertTrue(head["ok"], head)
        paths = read_paths(head, payload)
        self.assertTrue(paths)
        # every vertex, mapped back with astropy directly, lies on the contour level on grid A
        wa, wb = WCS(ha), WCS(hb)
        for g, p in paths:
            sky = wb.pixel_to_world(p[:, 0], p[:, 1])
            xa, ya = wa.world_to_pixel(sky)
            r = np.hypot(np.asarray(xa) - 25.0, np.asarray(ya) - 14.0)
            want = 2.5 * math.sqrt(2 * math.log(10.0 / [1.0, 5.0][g]))     # radius of the level on the Gaussian, grid A pixels
            self.assertLess(np.max(np.abs(r - want)), 0.25, f"level {g}")
        # the blob's centre, computed by astropy, is where the contours are centred on grid B (within half a pixel)
        sky_peak = wa.pixel_to_world(25.0, 14.0)
        px, py = wb.world_to_pixel(sky_peak)
        inner = [(g, p) for g, p in paths if g == 1]
        cx, cy = centroid(inner)
        self.assertLess(math.hypot(cx - float(px), cy - float(py)), 0.5)
        self.assertIn("ICRS", head["placement"]["method"])
        self.assertEqual(head["coords"], og.COORDS)

    def test_galactic_overlay_on_an_icrs_base(self):
        n = 41
        centre = SkyCoord(150.0 * u.deg, 2.0 * u.deg, frame="icrs").galactic
        hg = header(n=n, ctype=("GLON-TAN", "GLAT-TAN"), crval=(centre.l.deg, centre.b.deg), extra={"BUNIT": "K"})
        g = self.file("blob_gal.fits", blob(n, 15.0, 26.0), hg)
        hb = header(n=41, extra={"RADESYS": "ICRS"})
        b = self.file("base_icrs.fits", np.zeros((41, 41)), hb)
        head, payload = self.ask(b, g, {"kind": "contours", "channel": 0, "levels": {"mode": "list", "values": [5.0]}, "unit": "K"})
        self.assertTrue(head["ok"], head)
        self.assertFalse(head["placement"]["same_frame"])
        self.assertEqual((head["placement"]["from_frame"], head["placement"]["to_frame"]), ("Galactic", "ICRS"))
        cx, cy = centroid(read_paths(head, payload))
        peak = WCS(hg).pixel_to_world(15.0, 26.0).transform_to("icrs")
        px, py = WCS(hb).world_to_pixel(peak)
        self.assertLess(math.hypot(cx - float(px), cy - float(py)), 0.5)

    def test_footprints_that_do_not_overlap_are_refused(self):
        hb = header(n=21, extra={"RADESYS": "ICRS"})
        far = header(n=21, crval=(10.0, -40.0), extra={"RADESYS": "ICRS", "BUNIT": "K"})
        b = self.file("near.fits", np.zeros((21, 21)), hb)
        f = self.file("far.fits", blob(21, 10, 10), far)
        head, _ = self.ask(b, f, {"kind": "contours", "channel": 0, "levels": {"mode": "list", "values": [1.0]}, "unit": "K"})
        self.assertFalse(head["ok"])
        self.assertIn("does not overlap", head["error"])

    def test_an_undeclared_celestial_frame_takes_the_fits_default_and_says_so(self):
        hb = header(n=21, extra={"RADESYS": "ICRS"})
        bare = header(n=21, extra={"BUNIT": "K"})               # RA/Dec, no RADESYS, no EQUINOX: ICRS by the FITS standard
        b = self.file("base2.fits", np.zeros((21, 21)), hb)
        f = self.file("bare.fits", blob(21, 10, 10), bare)
        spec = {"kind": "contours", "channel": 0, "levels": {"mode": "list", "values": [1.0]}, "unit": "K"}
        head, _ = self.ask(b, f, spec)
        self.assertTrue(head["ok"], head)
        self.assertIn("base frame ICRS (declared in the header)", head["placement"]["method"])
        self.assertIn("overlay frame ICRS (assumed: FITS default, header declares no RADESYS or EQUINOX)", head["placement"]["method"])
        self.assertEqual(head["placement"]["from_status"], "assumed")
        # and as the base
        head, _ = self.ask(f, f, {"kind": "grid", "frame": "icrs"})
        self.assertTrue(head["ok"], head)
        self.assertIn("base frame ICRS (assumed: FITS default", head["placement"]["method"])

    def test_equinox_without_radesys_is_inferred_by_the_standard(self):
        hb = header(n=21, extra={"RADESYS": "ICRS"})
        old = header(n=21, extra={"BUNIT": "K", "EQUINOX": 1950.0})       # FK4 by the standard
        new = header(n=21, extra={"BUNIT": "K", "EQUINOX": 2000.0})       # FK5
        b = self.file("base3.fits", np.zeros((21, 21)), hb)
        spec = {"kind": "contours", "channel": 0, "levels": {"mode": "list", "values": [1.0]}, "unit": "K"}
        for name, h, label in (("fk4.fits", old, "FK4 (B1950)"), ("fk5.fits", new, "FK5 (J2000)")):
            f = self.file(name, blob(21, 10, 10), h)
            head, _ = self.ask(b, f, spec)
            if head["ok"]:
                self.assertIn(f"overlay frame {label} (inferred from EQUINOX", head["placement"]["method"])
            else:
                self.assertIn("overlap", head["error"])      # B1950 precession can move the footprint off a small field

    def test_an_unbuildable_celestial_frame_is_still_refused(self):
        hb = header(n=21, extra={"RADESYS": "ICRS"})
        b = self.file("base4.fits", np.zeros((21, 21)), hb)
        f = self.file("bad.fits", blob(21, 10, 10), header(n=21, extra={"BUNIT": "K", "RADESYS": "NOSUCH"}))
        head, _ = self.ask(b, f, {"kind": "contours", "channel": 0, "levels": {"mode": "list", "values": [1.0]}, "unit": "K"})
        self.assertFalse(head["ok"])
        self.assertIn("celestial frame", head["error"])

    def test_nan_pixels_are_gaps_in_the_contour(self):
        n = 41
        data = blob(n, 20.0, 20.0, sigma=6.0)
        data[:, 20] = np.nan                                     # a column with no value cuts every ring in two
        h = header(n=n, extra={"RADESYS": "ICRS", "BUNIT": "K"})
        f = self.file("gap.fits", data, h)
        head, payload = self.ask(f, f, {"kind": "contours", "channel": 0, "levels": {"mode": "list", "values": [5.0]}, "unit": "K"})
        self.assertTrue(head["ok"], head)
        paths = read_paths(head, payload)
        self.assertEqual(len(paths), 2)
        for _, p in paths:
            self.assertFalse(np.any(np.abs(p[:, 0] - 20.0) < 0.5), "a contour crossed the column with no value")

    def test_level_generators_and_sigma_from_the_mad(self):
        n = 64
        rng = np.random.default_rng(7)
        data = rng.normal(0.0, 0.3, size=(n, n)) + blob(n, 30, 30, sigma=4, peak=5)
        h = header(n=n, extra={"RADESYS": "ICRS", "BUNIT": "Jy/beam"})
        f = self.file("noise.fits", data, h)
        plane = data.astype("<f4").astype(float)
        mad = np.median(np.abs(plane - np.median(plane)))
        sigma = 1.482602218505602 * mad                          # 1 / Phi^-1(3/4), the normal-consistent MAD
        head, _ = self.ask(f, f, {"kind": "contours", "channel": 0, "levels": {"mode": "sigma", "multiples": [3, 5, 10]}, "unit": "Jy/beam"})
        self.assertTrue(head["ok"], head)
        self.assertAlmostEqual(head["sigma"]["value"], sigma, places=9)
        self.assertIn("MAD", head["sigma"]["method"])
        np.testing.assert_allclose(head["levels"], [3 * sigma, 5 * sigma, 10 * sigma], rtol=1e-12)
        self.assertEqual(head["unit"], "Jy/beam")
        # a recorded sigma that is not what the data give is refused, the same one is accepted
        bad, _ = self.ask(f, f, {"kind": "contours", "channel": 0, "levels": {"mode": "sigma", "multiples": [3], "sigma": sigma * 1.01}, "unit": "Jy/beam"})
        self.assertFalse(bad["ok"])
        self.assertIn("data changed", bad["error"])
        good, _ = self.ask(f, f, {"kind": "contours", "channel": 0, "levels": {"mode": "sigma", "multiples": [3], "sigma": sigma}, "unit": "Jy/beam"})
        self.assertTrue(good["ok"], good)
        lin, _ = self.ask(f, f, {"kind": "contours", "channel": 0, "levels": {"mode": "linear", "start": 0.5, "step": 0.25, "count": 3}, "unit": "Jy/beam"})
        self.assertEqual(lin["levels"], [0.5, 0.75, 1.0])
        geo, _ = self.ask(f, f, {"kind": "contours", "channel": 0, "levels": {"mode": "geometric", "start": 0.1, "factor": 2, "count": 4}, "unit": "Jy/beam"})
        np.testing.assert_allclose(geo["levels"], [0.1, 0.2, 0.4, 0.8])

    def test_unit_of_the_levels_must_be_the_files(self):
        h = header(n=21, extra={"RADESYS": "ICRS", "BUNIT": "K"})
        f = self.file("unit.fits", blob(21, 10, 10), h)
        head, _ = self.ask(f, f, {"kind": "contours", "channel": 0, "levels": {"mode": "list", "values": [1.0]}, "unit": "Jy/beam"})
        self.assertFalse(head["ok"])
        self.assertIn("unit", head["error"])
        nounit = self.file("nounit.fits", blob(21, 10, 10), header(n=21, extra={"RADESYS": "ICRS"}))
        head, _ = self.ask(nounit, nounit, {"kind": "contours", "channel": 0, "levels": {"mode": "list", "values": [1.0]}})
        self.assertTrue(head["ok"], head)
        self.assertIsNone(head["unit"])
        head, _ = self.ask(nounit, nounit, {"kind": "contours", "channel": 0, "levels": {"mode": "list", "values": [1.0]}, "unit": "mK"}, overlay_declared_bunit="mK")
        self.assertTrue(head["ok"], head)
        self.assertEqual((head["unit"], head["unit_status"]), ("mK", "user"))

    def test_contours_of_an_angle_map_are_refused(self):
        h = header(n=21, extra={"RADESYS": "ICRS"})
        f = self.file("angles_c.fits", np.full((21, 21), 30.0), h)
        head, _ = self.ask(f, f, {"kind": "contours", "channel": 0, "levels": {"mode": "list", "values": [1.0]}}, overlay_axial=True)
        self.assertFalse(head["ok"])
        self.assertIn("orientation map", head["error"])

    def test_vertex_budget_is_kept_by_simplifying_and_reported(self):
        paths = [(0, np.column_stack([np.arange(50.0), np.sin(np.arange(50.0)) * 0.01]))] * 10
        out, simplified = og.within_budget(paths, budget=100)
        self.assertLessEqual(simplified["vertices_after"], 100)
        self.assertEqual(simplified["vertices_before"], 500)
        for _, p in out:                                         # within the reported tolerance of the original
            self.assertLessEqual(np.max(np.abs(p[:, 1])), 0.01 + 1e-12)
        # through the operation itself, with the budgets lowered in this process
        import plane_worker as pw
        n = 120
        data = np.random.default_rng(3).normal(0.0, 1.0, size=(n, n))     # noise: very many vertices at level 0
        f = self.file("busy.fits", data, header(n=n, scale=SCALE / 10, extra={"RADESYS": "ICRS"}))
        cube = pw.Cube(str(f))
        spec = {"kind": "contours", "channel": 0, "levels": {"mode": "list", "values": [-0.5, 0.0, 0.5]}}
        old = (og.MAX_VERTICES, og.MAX_TRACED_VERTICES)
        try:
            og.MAX_VERTICES = 15000
            head, _ = og.contours(cube, cube, spec, (None, "unresolved"), True)
            self.assertLessEqual(head["vertices"], 15000)
            self.assertGreater(head["simplified"]["vertices_before"], 15000)
            self.assertGreater(head["simplified"]["tolerance_px"], 0)
            og.MAX_TRACED_VERTICES = 1000                         # refused before any vertex is transformed
            with self.assertRaisesRegex(og.Refusal, "trace more than 1000 vertices"):
                og.contours(cube, cube, spec, (None, "unresolved"), True)
        finally:
            og.MAX_VERTICES, og.MAX_TRACED_VERTICES = old

    def test_a_file_drawn_on_itself_needs_no_declared_frame(self):
        h = header(n=41, extra={"BUNIT": "K", "BMAJ": 3 / 60.0, "BMIN": 2 / 60.0, "BPA": 10.0})    # RA/Dec, no RADESYS or EQUINOX
        f = self.file("own.fits", blob(41, 20, 20), h)
        head, _ = self.ask(f, f, {"kind": "contours", "channel": 0, "levels": {"mode": "list", "values": [5.0]}, "unit": "K"})
        self.assertTrue(head["ok"], head)
        head, _ = self.ask(f, f, {"kind": "beam"})
        self.assertTrue(head["ok"], head)
        head, _ = self.ask(f, f, {"kind": "grid", "frame": "icrs"})       # a grid relates it to a named frame: the default, said
        self.assertTrue(head["ok"], head)
        self.assertIn("assumed: FITS default", head["placement"]["method"])

    def test_rdp_keeps_shape_within_tolerance(self):
        t = np.linspace(0, 2 * np.pi, 500)
        pts = np.column_stack([10 * np.cos(t), 10 * np.sin(t)])
        out = og.rdp(pts, 0.1)
        self.assertLess(len(out), 120)
        # every original point is within the tolerance of the simplified polyline
        for p in pts[::7]:
            d = min(_seg_dist(p, out[i], out[i + 1]) for i in range(len(out) - 1))
            self.assertLessEqual(d, 0.1 + 1e-9)

    # ---------- orientation ----------

    def orientation_files(self, name, base_cd, frame="pixel", angle=None, quantity="b_field"):
        """A uniform north-pointing field on a north-up grid, drawn on a base whose grid is rotated."""
        n = 21
        ho = header(n=n, extra={"RADESYS": "ICRS"})
        o = self.file(f"{name}_o.fits", np.full((n, n), 90.0 if angle is None else angle), ho)   # pixel angle 90 = +y = north here
        hb = header(n=31, cd=base_cd, extra={"RADESYS": "ICRS"})
        b = self.file(f"{name}_b.fits", np.zeros((31, 31)), hb)
        return o, b, ho, hb

    def test_north_pointing_field_points_to_base_pixel_north_on_a_rotated_grid(self):
        o, b, ho, hb = self.orientation_files("north", rot_cd(40.0, SCALE * 0.8))
        head, payload = self.ask(b, o, {"kind": "orientation", "sampling": {"mode": "step", "step": 3}, "length": "fixed", "opacity": "uniform"},
                                 overlay_interp={"unit": "deg", "frame": "pixel", "quantity": "b_field"})
        self.assertTrue(head["ok"], head)
        seg = np.frombuffer(payload, dtype="<f4").reshape(-1, 4).astype(float)
        self.assertGreater(len(seg), 10)
        wb = WCS(hb)
        for x, y, ang, w in seg:
            # independent north: walk a little north on the sky from the segment's centre and see where it lands on the base
            sky = wb.pixel_to_world(x, y)
            tip = SkyCoord(sky.ra, sky.dec + 1e-3 * u.deg, frame="icrs")
            tx, ty = wb.world_to_pixel(tip)
            north = math.degrees(math.atan2(float(ty) - y, float(tx) - x)) % 180.0
            self.assertLess(axial_dist(ang, north), 0.05)
            self.assertEqual(w, 1.0)
        # positions: each segment centre is the sky position of an overlay pixel
        wo = WCS(ho)
        ox, oy = wo.world_to_pixel(wb.pixel_to_world(seg[:, 0], seg[:, 1]))
        self.assertLess(np.max(np.abs(np.asarray(ox) - np.round(ox))), 1e-3)
        self.assertLess(np.max(np.abs(np.asarray(oy) - np.round(oy))), 1e-3)
        self.assertEqual(head["step"], 3)

    def test_gradient_is_rotated_to_the_b_field(self):
        o, b, _, hb = self.orientation_files("grad", rot_cd(0.0), angle=0.0)   # gradient along +x (east-west): B is north-south
        head, payload = self.ask(b, o, {"kind": "orientation", "sampling": {"mode": "step", "step": 5}, "length": "fixed", "opacity": "uniform"},
                                 overlay_interp={"unit": "deg", "frame": "pixel", "quantity": "gradient"})
        self.assertTrue(head["ok"], head)
        seg = np.frombuffer(payload, dtype="<f4").reshape(-1, 4)
        for ang in seg[:, 2]:
            self.assertLess(axial_dist(float(ang), 90.0), 0.05)
        self.assertIn("90", head["conversion"])

    def test_undeclared_frame_or_quantity_is_refused_by_name(self):
        o, b, _, _ = self.orientation_files("undecl", rot_cd(10.0))
        spec = {"kind": "orientation", "sampling": {"mode": "step", "step": 3}, "length": "fixed", "opacity": "uniform"}
        head, _ = self.ask(b, o, spec, overlay_interp={"unit": "deg", "frame": None, "quantity": "b_field"})
        self.assertFalse(head["ok"])
        self.assertIn("frame", head["error"])
        head, _ = self.ask(b, o, spec, overlay_interp={"unit": "deg", "frame": "pixel", "quantity": "unspecified"})
        self.assertFalse(head["ok"])
        self.assertIn("unspecified", head["error"])
        head, _ = self.ask(b, o, spec)
        self.assertFalse(head["ok"])
        self.assertIn("not declared as an orientation map", head["error"])

    def test_galactic_orientation_map_on_an_icrs_base_keeps_the_physical_direction(self):
        n = 21
        centre = SkyCoord(150.0 * u.deg, 2.0 * u.deg, frame="icrs").galactic
        ho = header(n=n, ctype=("GLON-TAN", "GLAT-TAN"), crval=(centre.l.deg, centre.b.deg))
        o = self.file("gal_o.fits", np.full((n, n), 0.0), ho)    # sky frame: 0 = Galactic north
        hb = header(n=31, cd=rot_cd(25.0, SCALE * 0.9), extra={"RADESYS": "ICRS"})
        b = self.file("gal_b.fits", np.zeros((31, 31)), hb)
        head, payload = self.ask(b, o, {"kind": "orientation", "sampling": {"mode": "step", "step": 4}, "length": "fixed", "opacity": "uniform"},
                                 overlay_interp={"unit": "deg", "frame": "sky", "quantity": "starlight_polarization"})
        self.assertTrue(head["ok"], head)
        seg = np.frombuffer(payload, dtype="<f4").reshape(-1, 4).astype(float)
        wb = WCS(hb)
        for x, y, ang, _ in seg:
            # independent: step towards Galactic north from the segment's sky position and see where that lands on the base
            sky = wb.pixel_to_world(x, y).galactic
            tip = SkyCoord(sky.l, sky.b + 1e-3 * u.deg, frame="galactic").icrs
            tx, ty = wb.world_to_pixel(tip)
            want = math.degrees(math.atan2(float(ty) - y, float(tx) - x)) % 180.0
            self.assertLess(axial_dist(ang, want), 0.05)

    def test_reliability_threshold_and_weight(self):
        n = 21
        ho = header(n=n, extra={"RADESYS": "ICRS"})
        o = self.file("rel_o.fits", np.full((n, n), 45.0), ho)
        rel = np.tile(np.linspace(0.0, 2.0, n), (n, 1))          # 0 at the left edge to 2 at the right
        r = self.file("rel_r.fits", rel, ho)
        spec = {"kind": "orientation", "sampling": {"mode": "step", "step": 2}, "length": "reliability", "opacity": "reliability", "threshold": 1.0}
        head, payload = self.ask(o, o, spec, overlay_interp={"unit": "deg", "frame": "pixel", "quantity": "b_field"}, reliability_path=str(r))
        self.assertTrue(head["ok"], head)
        seg = np.frombuffer(payload, dtype="<f4").reshape(-1, 4).astype(float)
        relf = rel.astype("<f4")
        p99 = float(np.percentile(relf, 99))
        for x, y, _, w in seg:
            rv = float(relf[int(round(y)), int(round(x))])
            self.assertGreaterEqual(rv, 1.0)
            self.assertAlmostEqual(w, min(1.0, rv / p99), places=5)
        self.assertGreater(head["reliability"]["dropped_below_threshold"], 0)
        head, _ = self.ask(o, o, spec, overlay_interp={"unit": "deg", "frame": "pixel", "quantity": "b_field"})
        self.assertFalse(head["ok"])
        self.assertIn("reliability map", head["error"])

    def test_auto_sampling_spaces_samples_in_base_pixels(self):
        n = 41
        ho = header(n=n, scale=SCALE / 2, extra={"RADESYS": "ICRS"})          # overlay pixels are half the base's
        o = self.file("auto_o.fits", np.full((n, n), 10.0), ho)
        hb = header(n=21, extra={"RADESYS": "ICRS"})
        b = self.file("auto_b.fits", np.zeros((21, 21)), hb)
        head, _ = self.ask(b, o, {"kind": "orientation", "sampling": {"mode": "auto", "gapPx": 16}, "length": "fixed", "opacity": "uniform"},
                           overlay_interp={"unit": "deg", "frame": "pixel", "quantity": "b_field"}, spacing=3)
        self.assertTrue(head["ok"], head)
        self.assertEqual(head["step"], 6)
        self.assertAlmostEqual(head["spacing_px"], 3.0, places=6)

    # ---------- beam ----------

    def test_beam_absent_is_refused(self):
        h = header(n=41, extra={"RADESYS": "ICRS"})
        f = self.file("nobeam.fits", np.zeros((41, 41)), h)
        head, _ = self.ask(f, f, {"kind": "beam"})
        self.assertFalse(head["ok"])
        self.assertIn("BMAJ", head["error"])
        h2 = header(n=41, extra={"RADESYS": "ICRS", "BMAJ": 4 / 60.0, "BMIN": 2 / 60.0})
        f2 = self.file("nobpa.fits", np.zeros((41, 41)), h2)
        head, _ = self.ask(f2, f2, {"kind": "beam"})
        self.assertFalse(head["ok"])
        self.assertIn("BPA", head["error"])

    def test_beam_is_drawn_with_its_axes_and_position_angle(self):
        bmaj, bmin, bpa = 6 / 60.0, 3 / 60.0, 30.0                # 6 x 3 arcmin, major axis 30 degrees east of north
        hs = header(n=41, extra={"RADESYS": "ICRS", "BMAJ": bmaj, "BMIN": bmin, "BPA": bpa})
        s = self.file("beam_src.fits", np.zeros((41, 41)), hs)
        hb = header(n=81, cd=rot_cd(20.0, SCALE / 2), extra={"RADESYS": "ICRS"})   # base: half-arcmin pixels, rotated
        b = self.file("beam_base.fits", np.zeros((81, 81)), hb)
        head, payload = self.ask(b, s, {"kind": "beam", "corner": "top-right"})
        self.assertTrue(head["ok"], head)
        (_, poly), = read_paths(head, payload)
        wb = WCS(hb)
        cx, cy = head["centre"]
        centre = wb.pixel_to_world(cx, cy)
        sky = wb.pixel_to_world(poly[:, 0], poly[:, 1])
        sep = centre.separation(sky).deg
        self.assertAlmostEqual(sep.max(), bmaj / 2, delta=bmaj * 1e-3)
        self.assertAlmostEqual(sep.min(), bmin / 2, delta=bmin * 1e-2)
        far = sky[int(np.argmax(sep))]
        self.assertLess(axial_dist(float(centre.position_angle(far).deg), bpa), 0.5)
        self.assertGreater(cx, 40)
        self.assertGreater(cy, 40)                                 # top-right of an 81-pixel image
        self.assertAlmostEqual(head["beam"]["bmaj_arcsec"], 360.0)
        self.assertAlmostEqual(head["beam"]["bmin_arcsec"], 180.0)
        self.assertEqual(head["beam"]["bpa_deg"], 30.0)

    def test_circular_beam_without_bpa_is_drawn_and_says_so(self):
        h = header(n=41, extra={"RADESYS": "ICRS", "BMAJ": 3 / 60.0, "BMIN": 3 / 60.0})
        f = self.file("round.fits", np.zeros((41, 41)), h)
        head, _ = self.ask(f, f, {"kind": "beam"})
        self.assertTrue(head["ok"], head)
        self.assertIsNone(head["beam"]["bpa_deg"])
        self.assertIn("circular", head["beam"]["note"])

    # ---------- grid ----------

    def test_grid_lines_at_known_ra_dec_pass_through_known_pixels(self):
        hb = header(n=61, cd=rot_cd(15.0, SCALE), extra={"RADESYS": "ICRS"})
        b = self.file("grid_base.fits", np.zeros((61, 61)), hb)
        head, payload = self.ask(b, b, {"kind": "grid", "frame": "icrs"})
        self.assertTrue(head["ok"], head)
        paths = read_paths(head, payload)
        wb = WCS(hb)
        s_lon, s_lat = head["lon_step_deg"], head["lat_step_deg"]
        lons = [p for g, p in paths if g == 0]
        lats = [p for g, p in paths if g == 1]
        self.assertGreaterEqual(len(lons), 2)
        self.assertGreaterEqual(len(lats), 2)
        for p in lons:
            ra = wb.pixel_to_world(p[:, 0], p[:, 1]).ra.deg
            k = np.round(ra / s_lon)
            np.testing.assert_allclose(ra, k * s_lon, atol=1e-6)    # a line of constant RA on a multiple of the step
            # a known position on that line maps (astropy, directly) onto the polyline
            mid = p[len(p) // 2]
            dec = float(wb.pixel_to_world(mid[0], mid[1]).dec.deg) + 0.1
            kx, ky = wb.world_to_pixel(SkyCoord(float(k[0] * s_lon) * u.deg, dec * u.deg, frame="icrs"))
            if og._inside(np.asarray(kx), np.asarray(ky), (61, 61)):
                d = min(_seg_dist(np.array([float(kx), float(ky)]), p[i], p[i + 1]) for i in range(len(p) - 1))
                self.assertLess(d, 0.05)
        for p in lats:
            dec = wb.pixel_to_world(p[:, 0], p[:, 1]).dec.deg
            np.testing.assert_allclose(dec, np.round(dec / s_lat) * s_lat, atol=1e-6)
        self.assertTrue(any(lab["axis"] == "lon" and "h" in lab["text"] for lab in head["labels"]))
        self.assertTrue(any(lab["axis"] == "lat" for lab in head["labels"]))

    def test_galactic_grid_on_an_icrs_base(self):
        hb = header(n=61, extra={"RADESYS": "ICRS"})
        b = self.file("ggrid.fits", np.zeros((61, 61)), hb)
        head, payload = self.ask(b, b, {"kind": "grid", "frame": "galactic"})
        self.assertTrue(head["ok"], head)
        self.assertFalse(head["placement"]["same_frame"])
        wb = WCS(hb)
        for g, p in read_paths(head, payload):
            gal = wb.pixel_to_world(p[:, 0], p[:, 1]).galactic
            v = gal.l.deg if g == 0 else gal.b.deg
            step = head["lon_step_deg"] if g == 0 else head["lat_step_deg"]
            np.testing.assert_allclose(v, np.round(v / step) * step, atol=1e-6)
        self.assertTrue(all(lab["text"].endswith("°") for lab in head["labels"]))

    def test_grid_from_another_dataset_is_refused(self):
        hb = header(n=21, extra={"RADESYS": "ICRS"})
        b = self.file("g1.fits", np.zeros((21, 21)), hb)
        o = self.file("g2.fits", np.zeros((21, 21)), hb)
        head, _ = self.ask(b, o, {"kind": "grid", "frame": "icrs"})
        self.assertFalse(head["ok"])


def _seg_dist(p, a, b):
    ab = b - a
    t = 0.0 if not np.any(ab) else float(np.clip(np.dot(p - a, ab) / np.dot(ab, ab), 0, 1))
    return float(np.hypot(*(a + t * ab - p)))


if __name__ == "__main__":
    unittest.main()
