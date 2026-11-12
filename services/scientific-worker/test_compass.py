"""The north/east compass at the centre of a map: pixel-grid directions of increasing latitude and longitude.

The answer is checked against one computed here without orientation.py: central differences of astropy's pixel-to-world
transform (plain arrays, no SkyCoord position angles), turned into the pixel vectors that move one unit north and one unit east
on the tangent plane. A rotated (CROTA2), PC-matrix, mirrored and Galactic WCS must each give its own arrows: north is never
assumed to be up."""
import math
import sys
import tempfile
import unittest
from pathlib import Path

import numpy as np
from astropy.wcs import WCS

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import orientation as ori  # noqa: E402
from test_orientation import header, write  # noqa: E402
from test_plane_worker import Client  # noqa: E402

N = 21
CENTRE = (N - 1) / 2.0


def independent_compass(w, x, y, h=1e-3):
    """(north_deg, east_deg), counter-clockwise from +x with y up, from the Jacobian of pixel to (east, north) offsets."""
    def world(px, py):
        lon, lat = w.all_pix2world([[px, py]], 0)[0]
        return lon, lat

    lon0, lat0 = world(x, y)
    cols = []
    for dx, dy in ((h, 0.0), (0.0, h)):
        lp, bp = world(x + dx, y + dy)
        lm, bm = world(x - dx, y - dy)
        dlon = ((lp - lm + 180.0) % 360.0) - 180.0
        cols.append(((dlon * math.cos(math.radians(lat0))) / (2 * h), (bp - bm) / (2 * h)))    # d(east, north) per pixel along x, then y
    jac = np.array(cols).T                                                                      # rows: east, north
    north_px = np.linalg.solve(jac, [0.0, 1.0])
    east_px = np.linalg.solve(jac, [1.0, 0.0])
    ang = lambda v: math.degrees(math.atan2(v[1], v[0])) % 360.0  # noqa: E731
    return ang(north_px), ang(east_px)


def circ(a, b):
    return abs(((a - b + 180.0) % 360.0) - 180.0)


def pc_header(rot_deg, flip=False, **kw):
    a = math.radians(rot_deg)
    scale = 1.0 / 60.0
    return header(n=N, scale=scale, extra={"CDELT1": scale if flip else -scale, "PC1_1": math.cos(a), "PC1_2": -math.sin(a),
                                           "PC2_1": math.sin(a), "PC2_2": math.cos(a)}, **kw)


CASES = {
    "north up, east left": header(n=N),
    "CROTA2 = 30": header(n=N, extra={"CROTA2": 30.0}),
    "CROTA2 = -137": header(n=N, extra={"CROTA2": -137.0}),
    "PC matrix, 30 degrees": pc_header(30.0),
    "PC matrix, 30 degrees, mirrored (east right)": pc_header(30.0, flip=True),
    "Galactic, CROTA2 = -20": header(n=N, ctype=("GLON-CAR", "GLAT-CAR"), crval=(120.0, 30.0), extra={"CROTA2": -20.0}),
}


class Compass(unittest.TestCase):
    def test_matches_an_independent_jacobian_for_each_wcs(self):
        for name, h in CASES.items():
            with self.subTest(name):
                w = WCS(h)
                got = ori.compass(w, CENTRE, CENTRE)
                self.assertTrue(got["available"], got)
                want_n, want_e = independent_compass(w, CENTRE, CENTRE)
                self.assertLess(circ(got["north_deg"], want_n), 0.01, (got, want_n))
                self.assertLess(circ(got["east_deg"], want_e), 0.01, (got, want_e))

    def test_a_rotated_grid_does_not_point_north_up(self):
        got = ori.compass(WCS(CASES["CROTA2 = 30"]), CENTRE, CENTRE)
        self.assertGreater(circ(got["north_deg"], 90.0), 20.0)                      # not up
        up = ori.compass(WCS(CASES["north up, east left"]), CENTRE, CENTRE)
        self.assertLess(circ(up["north_deg"], 90.0), 0.01)
        self.assertLess(circ(up["east_deg"], 180.0), 0.01)                          # east is to the left

    def test_a_mirrored_grid_flips_east_not_north(self):
        a = ori.compass(WCS(pc_header(0.0)), CENTRE, CENTRE)
        b = ori.compass(WCS(pc_header(0.0, flip=True)), CENTRE, CENTRE)
        self.assertLess(circ(a["north_deg"], b["north_deg"]), 0.01)
        self.assertLess(circ(a["east_deg"], 180.0), 0.01)
        self.assertLess(circ(b["east_deg"], 0.0), 0.01)

    def test_a_position_the_projection_cannot_place_is_unavailable_not_guessed(self):
        h = header(n=N, ctype=("RA---AIT", "DEC--AIT"), crval=(0.0, 0.0), scale=10.0)
        got = ori.compass(WCS(h), 1e6, 1e6)
        self.assertFalse(got["available"])
        self.assertIn("WCS", got["reason"])


class ViewReply(unittest.TestCase):
    """The view op carries the compass; a file without a celestial WCS says why it has none."""

    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        cls.dir = Path(cls.tmp.name)
        cls.c = Client()

    @classmethod
    def tearDownClass(cls):
        cls.c.close()
        cls.tmp.cleanup()

    def test_view_compass_for_a_rotated_wcs_matches_the_independent_answer(self):
        path = self.dir / "rot.fits"
        write(path, np.zeros((N, N)), CASES["CROTA2 = 30"])
        head, _ = self.c.ask(op="view", path=str(path))
        self.assertTrue(head["ok"], head)
        comp = head["compass"]
        self.assertTrue(comp["available"], comp)
        want_n, want_e = independent_compass(WCS(CASES["CROTA2 = 30"]), CENTRE, CENTRE)
        self.assertLess(circ(comp["north_deg"], want_n), 0.01)
        self.assertLess(circ(comp["east_deg"], want_e), 0.01)

    def test_view_without_celestial_wcs_has_no_compass_and_says_why(self):
        path = self.dir / "pix.fits"
        write(path, np.zeros((N, N)), header(n=N, ctype=("LINEAR", "LINEAR")))
        head, _ = self.c.ask(op="view", path=str(path))
        self.assertTrue(head["ok"], head)
        self.assertEqual(head["sky"]["kind"], "pixel")
        self.assertFalse(head["compass"]["available"])
        self.assertIn("celestial", head["compass"]["reason"])
        self.assertNotIn("north_deg", head["compass"])


if __name__ == "__main__":
    unittest.main()
