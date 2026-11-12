"""What VGT.jl writes into its product headers (ADR 0014 and 0018 amendments): the keywords are reported verbatim, a sky-frame angle
stated in another celestial frame than its grid's is carried through the sky, and the resolution is computed only from what the file states.

Independence: the frame rotation is checked against the position angle of the North Galactic Pole at each pixel
(SkyCoord.position_angle), not against the helper under test.
"""
import json
import math
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

import numpy as np
from astropy import units as u
from astropy.coordinates import SkyCoord
from astropy.io import fits
from astropy.wcs import WCS

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
sys.path.insert(0, str(HERE.parent.parent / "fixtures"))
import generate as g  # noqa: E402
import orientation as ori  # noqa: E402
import relorient as ro  # noqa: E402
from test_relorient import args, header, write_map  # noqa: E402

SCRIPT = str(HERE / "fits_header.py")


def describe(path):
    p = subprocess.run([sys.executable, "-P", SCRIPT, str(path)], capture_output=True, text=True, timeout=20)
    return json.loads(p.stdout)


class Keywords(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        cls.f = g.write_all(Path(cls.tmp.name))

    @classmethod
    def tearDownClass(cls):
        cls.tmp.cleanup()

    def vgt(self, name):
        return describe(self.f[name])["vgt"]

    def test_each_psiframe_case_is_reported_as_written(self):
        for name, frame in (("vgt_psi_pixel_frame.fits", "PIXEL"), ("vgt_psi_galactic_frame.fits", "GALACTIC"),
                            ("vgt_psi_icrs_frame.fits", "ICRS"), ("vgt_psi_unknown_frame.fits", "ECLIPTIC")):
            v = self.vgt(name)
            self.assertEqual((v["psiframe"], v["psiangle"], v["vgtprod"]), (frame, "BFIELD", "psi"))
        self.assertEqual(self.vgt("vgt_psi_galactic_frame.fits")["params"]["VGTDN"], 10)

    def test_a_file_without_psiframe_reports_none_and_nothing_is_inferred_from_the_wcs(self):
        v = self.vgt("vgt_psi_no_frame.fits")
        self.assertIsNone(v["psiframe"])
        self.assertIsNone(v["psiangle"])
        self.assertIsNone(self.vgt("vgt_psi_rad.fits"))                    # a file with no VGT card at all has no block

    def test_resolution_is_the_input_beam_and_the_kernel_in_quadrature_and_the_window_is_stated_apart(self):
        r = self.vgt("vgt_psi_galactic_frame.fits")["resolution"]
        k = 2 * math.sqrt(2 * math.log(2)) * g.VGT_KER * g.CDELT_DEG * 3600.0           # VGTKFWHM (input pixels) x the pixel scale
        bmaj, bmin, bpa = g.VGT_INPUT_BEAM_ARCSEC
        self.assertEqual(r["status"], "approximate")
        self.assertAlmostEqual(r["kernel_fwhm_arcsec"], k, places=6)
        self.assertAlmostEqual(r["gaussian_arcsec"]["bmaj"], math.hypot(bmaj, k), places=6)
        self.assertAlmostEqual(r["gaussian_arcsec"]["bmin"], math.hypot(bmin, k), places=6)
        self.assertEqual(r["gaussian_arcsec"]["bpa"], bpa)
        self.assertAlmostEqual(r["window"]["side_arcsec"], g.VGT_DN * g.CDELT_DEG * 3600.0, places=6)
        self.assertIn("not included", r["window"]["note"])

    def test_an_input_beam_whose_unit_the_card_does_not_state_is_not_established(self):
        r = self.vgt("vgt_psi_beam_unit_unstated.fits")["resolution"]
        self.assertEqual(r["status"], "not_established")
        self.assertNotIn("gaussian_arcsec", r)
        self.assertTrue(any("INBMAJ" in x and "unit" in x for x in r["reasons"]))

    def test_missing_inputs_and_non_square_pixels_are_not_established(self):
        h = fits.getheader(self.f["vgt_psi_galactic_frame.fits"])
        no_beam = h.copy()
        for k in ("INBMAJ", "INBMIN", "INBPA"):
            del no_beam[k]
        r = Path(self.tmp.name) / "nobeam.fits"
        fits.PrimaryHDU(np.zeros((4, 4), dtype="f4"), header=no_beam).writeto(r)
        res = describe(r)["vgt"]["resolution"]
        self.assertEqual(res["status"], "not_established")
        self.assertTrue(any("INBMAJ" in x for x in res["reasons"]))
        rect = h.copy()
        rect["CDELT2"] = 2 * abs(rect["CDELT2"])
        r2 = Path(self.tmp.name) / "rect.fits"
        fits.PrimaryHDU(np.zeros((4, 4), dtype="f4"), header=rect).writeto(r2)
        res = describe(r2)["vgt"]["resolution"]
        self.assertEqual(res["status"], "not_established")
        self.assertTrue(any("not square" in x for x in res["reasons"]))


class GalacticAngleOnEquatorialGrid(unittest.TestCase):
    """A psi stated east of Galactic north, on an ICRS grid (the LMC: Galactic north is far from celestial north)."""

    def setUp(self):
        self.n = 48
        self.h = header(self.n, self.n, 1.0 / 60.0)
        self.w = WCS(self.h)
        self.ys, self.xs = np.mgrid[0:self.n, 0:self.n].astype(float)
        self.ngp = SkyCoord(l=0 * u.deg, b=90 * u.deg, frame="galactic").icrs

    def expected_pixel_angle(self, galactic_pa):
        """Independent: the ICRS position angle of a direction at Galactic PA theta is theta plus the ICRS PA of the North Galactic Pole
        at that pixel; a probe along that ICRS PA is turned into a pixel direction by the WCS."""
        c = self.w.pixel_to_world(self.xs, self.ys)
        p = c.position_angle(self.ngp).deg
        tip = c.directional_offset_by((p + galactic_pa) * u.deg, 1e-4 * u.deg)
        tx, ty = self.w.world_to_pixel(tip)
        return np.degrees(np.arctan2(ty - self.ys, tx - self.xs)) % 180.0

    def test_the_local_rotation_is_large_and_the_carried_angle_matches_the_pole_construction(self):
        theta = np.full((self.n, self.n), 30.0)
        got = ro.pixel_b_field(self.w, theta, "sky", "b_field", "galactic")
        want = self.expected_pixel_angle(30.0)
        self.assertLess(float(np.abs((got - want + 90.0) % 180.0 - 90.0).max()), 0.02)
        # what treating it as east of north in the grid's own frame (the old reading) would give: wrong by the local angle between the two norths
        naive = ro.pixel_b_field(self.w, theta, "sky", "b_field", None)
        off = float(np.abs((naive - want + 90.0) % 180.0 - 90.0).mean())
        p = float(self.w.pixel_to_world(self.n / 2, self.n / 2).position_angle(self.ngp).deg)
        self.assertGreater(off, 45.0)
        self.assertAlmostEqual(off, abs(((p + 90.0) % 180.0) - 90.0), delta=2.0)

    def test_relative_orientation_against_a_reference_gives_the_known_angle(self):
        """The reference is the same field written independently in the pixel frame, rotated by 20 degrees: the relative angle is 20."""
        theta = np.full((self.n, self.n), 30.0)
        ref = (self.expected_pixel_angle(30.0 + 20.0)) % 180.0
        d = Path(tempfile.mkdtemp())
        a = write_map(d / "a.fits", theta, self.h)
        b = write_map(d / "b.fits", ref, self.h)
        s = ro.op_maps(args(input=a, reference=b, axial_frame="sky", axial_sky_frame="galactic", out=str(d / "o.fits"), table_out=str(d / "t.csv")), {"n_shifts": 0})["summary"]
        # the maps differ by 20 degrees everywhere: the alignment measure is <cos 2 dphi> = cos 40
        self.assertAlmostEqual(s["am"], math.cos(math.radians(40.0)), delta=2e-3)
        naive = ro.op_maps(args(input=a, reference=b, axial_frame="sky", out=str(d / "o2.fits"), table_out=str(d / "t2.csv")), {"n_shifts": 0})["summary"]
        self.assertGreater(abs(naive["am"] - s["am"]), 0.3)               # reading it as the grid's own frame gives a different answer

    def test_a_galactic_psi_on_a_galactic_grid_is_not_rotated_and_icrs_on_icrs_is_not_either(self):
        gh = g.vgt_psi_galactic_grid()
        gw = WCS(gh)
        t = np.full((4, 4), 30.0)
        got = ro.pixel_b_field(gw, t, "sky", "b_field", "galactic")
        self.assertTrue(np.allclose(got, ro.pixel_b_field(gw, t, "sky", "b_field", None)))
        ih = header(8, 8, 1.0 / 60.0)
        iw = WCS(ih)
        t = np.full((8, 8), 30.0)
        self.assertTrue(np.allclose(ro.pixel_b_field(iw, t, "sky", "b_field", "icrs"), ro.pixel_b_field(iw, t, "sky", "b_field", None)))

    def test_an_unknown_frame_name_is_refused_with_a_sentence(self):
        with self.assertRaises(ori.Undeclared) as e:
            ori.sky_pa_in_grid_frame(self.w, 1.0, 1.0, 10.0, "ecliptic")
        self.assertIn("ecliptic", str(e.exception))


def cli(*a):
    p = subprocess.run([sys.executable, "-P", str(HERE / "analysis.py"), *map(str, a)], capture_output=True, text=True, timeout=120)
    return p.returncode, json.loads(p.stdout)


class DerivedFromAVgtMap(unittest.TestCase):
    """Convolve from a stated resolution, and the producer's cards never travel into a derived file."""

    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        cls.dir = Path(cls.tmp.name)
        cls.f = g.write_all(cls.dir / "fx")

    @classmethod
    def tearDownClass(cls):
        cls.tmp.cleanup()

    def convolve(self, src, *extra):
        return cli("convolve", "--input", src, "--out", self.dir / "c.fits", "--target-bmaj", 1000, "--target-bmin", 1000, "--target-bpa", 0, *extra)

    def test_a_file_with_no_beam_is_refused_until_a_resolution_is_given(self):
        code, r = self.convolve(self.f["vgt_psi_galactic_frame.fits"], "--axial-unit", "rad", "--axial-frame", "sky", "--axial-sky-frame", "galactic")
        self.assertEqual(code, 2)
        self.assertIn("does not declare its beam", r["error"])

    def test_the_accepted_approximation_is_used_recorded_and_stripped_from_the_result(self):
        res = describe(self.f["vgt_psi_galactic_frame.fits"])["vgt"]["resolution"]["gaussian_arcsec"]
        code, r = self.convolve(self.f["vgt_psi_galactic_frame.fits"], "--axial-unit", "rad", "--axial-frame", "sky", "--axial-sky-frame", "galactic",
                                "--source-bmaj", res["bmaj"], "--source-bmin", res["bmin"], "--source-bpa", res["bpa"], "--source-beam-basis", "vgt_approximate")
        self.assertEqual(code, 0, r)
        s = r["summary"]
        self.assertEqual(s["source_beam"], {"basis": "vgt_approximate", "approximate": True})
        self.assertAlmostEqual(s["from_beam_arcsec"][0], res["bmaj"])
        self.assertIn("approximation", s["policy"])
        self.assertIn("first carried from the Galactic frame", s["policy"])
        h = fits.getheader(self.dir / "c.fits")
        self.assertEqual([k for k in h if k.startswith(("PSI", "VGT", "INB"))], [])     # the result must not re-declare itself from the original's cards
        self.assertAlmostEqual(h["BMAJ"], 1000 / 3600.0)

    def test_a_file_that_declares_a_beam_is_never_overridden(self):
        h = fits.getheader(self.f["vgt_psi_galactic_frame.fits"])
        h["BMAJ"], h["BMIN"], h["BPA"] = 0.01, 0.01, 0.0
        path = self.dir / "withbeam.fits"
        fits.PrimaryHDU(np.zeros((4, 4), dtype="f4"), header=h).writeto(path)
        code, r = self.convolve(path, "--source-bmaj", 100, "--source-bmin", 100, "--source-beam-basis", "user")
        self.assertEqual(code, 2)
        self.assertIn("declares its own beam", r["error"])

    def test_a_strided_window_drops_the_widths_counted_in_input_pixels(self):
        src = str(self.f["vgt_psi_galactic_frame.fits"])
        for step, gone in ((1, False), (2, True)):
            out = self.dir / f"sub{step}.fits"
            code, r = cli("subset", "--input", src, "--out", out, "--window", json.dumps({"x0": 0, "x1": 3, "y0": 0, "y1": 3, "step": step}))
            self.assertEqual(code, 0, r)
            h = fits.getheader(out)
            self.assertEqual("VGTKFWHM" not in h, gone)
            self.assertEqual("PSIFRAME" in h, True)             # the angle's frame still describes a strided copy of the same grid


if __name__ == "__main__":
    unittest.main()
