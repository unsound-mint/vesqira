"""Sky context (ADR 0072): footprint polygons and cursor positions against astropy used directly in the tests.

The expected positions are computed here from astropy (WCS and SkyCoord on headers the tests write), never from the module.
The worker is driven through its line protocol, as the API drives it.
"""
import math
import os
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
from test_orientation import header, write  # noqa: E402
from test_plane_worker import Client  # noqa: E402

N = 21
SCALE = 1.0 / 60.0   # 1 arcmin pixels


def polygon_of(head):
    return np.array(head["footprint"]["polygon"], dtype=float)


def sep_arcsec(lon1, lat1, lon2, lat2, frame="icrs"):
    return SkyCoord(lon1, lat1, unit="deg", frame=frame).separation(SkyCoord(lon2, lat2, unit="deg", frame=frame)).arcsec


class Footprint(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        cls.d = Path(cls.tmp.name)
        cls.c = Client()

    @classmethod
    def tearDownClass(cls):
        cls.c.close()
        cls.tmp.cleanup()

    def file(self, name, h, shape=(N, N)):
        p = self.d / name
        write(p, np.ones(shape), h)
        return p

    def ask(self, op, p, frame, **kw):
        head, _ = self.c.ask(op=op, path=str(p), frame=frame, **kw)
        self.assertTrue(head["ok"], head)
        return head

    def test_icrs_cube_in_icrs_corners_are_the_wcs_corners(self):
        p = self.file("icrs.fits", header(N))
        head = self.ask("sky_footprint", p, "icrs")
        self.assertTrue(head["same_frame"])
        poly = polygon_of(head)
        w = WCS(header(N))
        want = w.pixel_to_world([-0.5, N - 0.5, N - 0.5, -0.5], [-0.5, -0.5, N - 0.5, N - 0.5])
        for corner in want:
            d = sep_arcsec(poly[:, 0], poly[:, 1], corner.ra.deg, corner.dec.deg)
            self.assertLess(d.min(), 1e-3)                 # every pixel corner is a vertex (to a thousandth of an arcsecond)
        # the polygon's centre of mass is the image centre; its half-width is half the field (10.5 arcmin)
        c = head["footprint"]["center"]
        mid = w.pixel_to_world([(N - 1) / 2.0], [(N - 1) / 2.0])[0]
        self.assertLess(sep_arcsec(c["lon"], c["lat"], mid.ra.deg, mid.dec.deg), 1e-3)
        self.assertAlmostEqual(head["footprint"]["extent_deg"], math.hypot(N, N) / 2 * SCALE, delta=0.01)

    def test_cdelt1_negative_and_positive_give_the_same_sky_outline_mirrored_in_pixels(self):
        # CDELT1 < 0 is the usual (east left); a positive CDELT1 flips the image on the sky, so the corners differ from the
        # ones a naive "ra increases to the right" reading gives. Both are checked against astropy directly.
        for name, c1 in (("neg.fits", -SCALE), ("pos.fits", SCALE)):
            h = header(N)
            h["CDELT1"] = c1
            p = self.file(name, h)
            head = self.ask("sky_footprint", p, "icrs")
            poly = polygon_of(head)
            w = WCS(h)
            first = w.pixel_to_world(-0.5, -0.5)          # the first vertex is the pixel corner (-0.5, -0.5)
            self.assertLess(sep_arcsec(poly[0, 0], poly[0, 1], first.ra.deg, first.dec.deg), 1e-3, name)
            right = w.pixel_to_world(N - 0.5, -0.5)       # a vertex of the bottom edge's far end follows (the next side starts there)
            self.assertLess(sep_arcsec(poly[:, 0], poly[:, 1], right.ra.deg, right.dec.deg).min(), 1e-3, name)
        neg = polygon_of(self.ask("sky_footprint", self.d / "neg.fits", "icrs"))
        pos = polygon_of(self.ask("sky_footprint", self.d / "pos.fits", "icrs"))
        # the same field, so the same bounding box, but the starting corner is on opposite sides in RA
        self.assertAlmostEqual(neg[:, 0].min(), pos[:, 0].min(), places=6)
        self.assertGreater(neg[0, 0], 150.0)               # RA increases to the left: the lower-left pixel is the larger RA
        self.assertLess(pos[0, 0], 150.0)

    def test_galactic_cube_shown_in_icrs_goes_through_the_frame_transformation(self):
        h = header(N, ctype=("GLON-TAN", "GLAT-TAN"), crval=(120.0, 5.0))
        p = self.file("gal.fits", h)
        head = self.ask("sky_footprint", p, "icrs")
        self.assertFalse(head["same_frame"])
        self.assertEqual(head["from_frame"], "Galactic")
        poly = polygon_of(head)
        w = WCS(h)
        gal = w.pixel_to_world([-0.5, N - 0.5, N - 0.5, -0.5], [-0.5, -0.5, N - 0.5, N - 0.5])
        for corner in gal:
            want = corner.transform_to("icrs")             # astropy's own transformation, computed here
            d = sep_arcsec(poly[:, 0] % 360, poly[:, 1], want.ra.deg, want.dec.deg)
            self.assertLess(d.min(), 1e-3)
        # not the galactic numbers relabelled: the outline is nowhere near (120, 5)
        self.assertGreater(sep_arcsec(poly[0, 0], poly[0, 1], 120.0, 5.0), 3600.0)
        # and shown in Galactic it is the file's own numbers
        same = polygon_of(self.ask("sky_footprint", p, "galactic"))
        want0 = w.pixel_to_world(-0.5, -0.5)
        self.assertAlmostEqual(same[0, 0] % 360, want0.l.deg, places=6)
        self.assertAlmostEqual(same[0, 1], want0.b.deg, places=6)

    def test_icrs_cube_shown_in_galactic(self):
        p = self.d / "icrs.fits"
        head = self.ask("sky_footprint", p if p.exists() else self.file("icrs.fits", header(N)), "galactic")
        poly = polygon_of(head)
        w = WCS(header(N))
        first = w.pixel_to_world(-0.5, -0.5).transform_to("galactic")
        self.assertAlmostEqual(poly[0, 0] % 360, first.l.deg, places=6)
        self.assertAlmostEqual(poly[0, 1], first.b.deg, places=6)

    def test_outline_across_longitude_zero_is_continuous(self):
        h = header(N, crval=(0.05, 1.0))
        p = self.file("wrap.fits", h)
        poly = polygon_of(self.ask("sky_footprint", p, "icrs"))
        steps = np.abs(np.diff(poly[:, 0]))
        self.assertLess(steps.max(), 1.0)                  # no jump of about 360 degrees between neighbours
        self.assertLess(poly[:, 0].min(), 0.0)             # unwrapped: some longitudes are negative, the browser wraps them

    def test_missing_celestial_wcs_has_no_footprint_and_no_guess(self):
        h = fits.Header()
        h["CTYPE1"], h["CTYPE2"] = "X", "Y"
        h["CRVAL1"] = h["CRVAL2"] = 0.0
        h["CRPIX1"] = h["CRPIX2"] = 1.0
        p = self.file("pix.fits", h)
        head = self.ask("sky_footprint", p, "icrs")
        self.assertIsNone(head["footprint"])
        self.assertIn("no complete celestial WCS", head["reason"])
        pt = self.ask("sky_point", p, "icrs", x=3, y=4)
        self.assertIsNone(pt["position"])

    def test_header_without_any_wcs_has_no_footprint(self):
        p = self.file("bare.fits", fits.Header())
        head = self.ask("sky_footprint", p, "galactic")
        self.assertIsNone(head["footprint"])
        self.assertTrue(head["reason"])

    def test_a_survey_sized_map_has_no_outline(self):
        h = header(N, ctype=("RA---CAR", "DEC--CAR"), crval=(0.0, 0.0), scale=10.0)
        p = self.file("survey.fits", h)
        head = self.ask("sky_footprint", p, "icrs")
        self.assertIsNone(head["footprint"])
        self.assertIn("survey", head["reason"])

    def test_the_basis_says_how_the_frame_is_known(self):
        p = self.file("assumed.fits", header(N))
        head = self.ask("sky_footprint", p, "icrs")
        self.assertEqual(head["from_status"], "assumed")   # no RADESYS, no EQUINOX: the FITS default, said so
        p2 = self.file("declared.fits", header(N, extra={"RADESYS": "ICRS"}))
        self.assertEqual(self.ask("sky_footprint", p2, "icrs")["from_status"], "declared")

    def test_a_correction_moves_the_footprint(self):
        # the researcher's WCS correction (ADR 0061) is in force for the request: the outline follows the corrected WCS
        p = self.file("corr.fits", header(N))
        base = polygon_of(self.ask("sky_footprint", p, "icrs"))
        head, _ = self.c.ask(op="sky_footprint", path=str(p), frame="icrs",
                             corrections={os.path.realpath(p): {"hash": "b" * 64, "wcs": {"CRVAL1": 151.0}}})
        self.assertTrue(head["ok"], head)
        h = header(N)
        h["CRVAL1"] = 151.0
        first = WCS(h).pixel_to_world(-0.5, -0.5)
        moved = polygon_of(head)
        self.assertLess(sep_arcsec(moved[0, 0], moved[0, 1], first.ra.deg, first.dec.deg), 1e-3)
        self.assertGreater(sep_arcsec(moved[0, 0], moved[0, 1], base[0, 0], base[0, 1]), 3000.0)   # about a degree from the file's own

    def test_point_matches_astropy_and_the_viewer_readout(self):
        h = header(N, ctype=("GLON-TAN", "GLAT-TAN"), crval=(120.0, 5.0))
        p = self.file("gal2.fits", h)
        w = WCS(h)
        for x, y in ((0, 0), (10, 10), (20, 3)):
            pt = self.ask("sky_point", p, "icrs", x=x, y=y)["position"]
            want = w.pixel_to_world(x, y).transform_to("icrs")
            self.assertLess(sep_arcsec(pt["lon"], pt["lat"], want.ra.deg, want.dec.deg), 1e-3)
        # in the file's own frame it is the readout's number
        native = self.ask("sky_point", p, "galactic", x=10, y=10)["position"]
        coord, _ = self.c.ask(op="coord", path=str(p), x=10, y=10, channel=0)
        self.assertAlmostEqual(native["lon"], coord["sky"]["lon"] % 360, places=9)
        self.assertAlmostEqual(native["lat"], coord["sky"]["lat"], places=9)

    def test_bad_requests_are_refused(self):
        p = self.file("ok.fits", header(N))
        for req in (dict(op="sky_footprint", frame="fk5"), dict(op="sky_footprint", frame=None), dict(op="sky_point", frame="icrs", x="a", y=1),
                    dict(op="sky_point", frame="icrs", x=True, y=1), dict(op="sky_point", frame="icrs", x=float("nan"), y=1)):
            head, _ = self.c.ask(path=str(p), **req)
            self.assertFalse(head["ok"], req)


if __name__ == "__main__":
    unittest.main()
