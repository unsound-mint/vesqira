"""Checks the fixtures against independent implementations and closed-form answers."""
import math
import tempfile
import unittest
from pathlib import Path

import numpy as np
from astropy.io import fits
from astropy.wcs import WCS

import generate as g


def tan_world(x0, y0, ra0_deg, dec0_deg, cdelt_deg_x, cdelt_deg_y, crpix):
    """Independent gnomonic (TAN) projection written from the standard formulas, not from astropy."""
    xi = math.radians((x0 + 1 - crpix[0]) * cdelt_deg_x)    # +1: FITS pixels are 1-based
    eta = math.radians((y0 + 1 - crpix[1]) * cdelt_deg_y)
    a0, d0 = math.radians(ra0_deg), math.radians(dec0_deg)
    den = math.cos(d0) - eta * math.sin(d0)
    ra = a0 + math.atan2(xi, den)
    dec = math.atan2(math.sin(d0) + eta * math.cos(d0), math.hypot(xi, den))
    return math.degrees(ra) % 360.0, math.degrees(dec)


class HiCube(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        cls.files = g.write_all(Path(cls.tmp.name))

    @classmethod
    def tearDownClass(cls):
        cls.tmp.cleanup()

    def test_generation_is_deterministic(self):
        a = g.hi_cube()[0].data
        b = g.hi_cube()[0].data
        np.testing.assert_array_equal(a, b)

    def test_shape_and_axis_order_are_native_fits(self):
        with fits.open(self.files["hi_cube.fits"]) as h:
            self.assertEqual(h[0].data.shape, (g.NC, g.NY, g.NX))   # numpy order is reversed FITS order
            self.assertEqual(h[0].header["NAXIS1"], g.NX)
            self.assertEqual(h[0].header["CTYPE3"], "VRAD")

    def test_reference_pixel_maps_to_crval_exactly(self):
        w = WCS(g.hi_cube()[0].header).celestial
        ra, dec = w.wcs_pix2world(g.CRPIX[0] - 1, g.CRPIX[1] - 1, 0)
        self.assertAlmostEqual(float(ra), g.CRVAL_RA, places=9)
        self.assertAlmostEqual(float(dec), g.CRVAL_DEC, places=9)

    def test_off_axis_positions_match_independent_tan(self):
        w = WCS(g.hi_cube()[0].header).celestial
        for x, y in [(0, 0), (31, 23), g.SOURCE_PIX, (5, 20)]:
            ra, dec = w.wcs_pix2world(x, y, 0)
            ra_i, dec_i = tan_world(x, y, g.CRVAL_RA, g.CRVAL_DEC, -g.CDELT_DEG, g.CDELT_DEG, g.CRPIX)
            self.assertAlmostEqual(float(ra), ra_i, places=8)
            self.assertAlmostEqual(float(dec), dec_i, places=8)

    def test_spectral_axis_is_linear_in_ms(self):
        w = WCS(g.hi_cube()[0].header)
        for ch in (0, g.SOURCE_CHANNEL, g.NC - 1):
            v = float(w.spectral.pixel_to_world_values(ch))
            self.assertAlmostEqual(v, g.V0_MS + ch * g.DV_MS, places=6)   # 1-based CRPIX=1 -> channel 0 is CRVAL

    def test_source_peak_is_where_declared(self):
        d = g.hi_cube()[0].data
        c, y, x = np.unravel_index(np.nanargmax(d), d.shape)
        self.assertEqual((x, y, c), (*g.SOURCE_PIX, g.SOURCE_CHANNEL))
        self.assertAlmostEqual(float(d[c, y, x]), g.PEAK_K, delta=1.0)

    def test_nan_block_is_missing_never_zero(self):
        d = g.hi_cube()[0].data
        self.assertTrue(np.isnan(d[:, g.NAN_BOX[0], g.NAN_BOX[1]]).all())
        self.assertFalse((d[~np.isnan(d)] == 0.0).any())      # no zero was substituted for missing

    def test_flagged_channels_keep_finite_data_and_are_flagged_separately(self):
        hdul = g.hi_cube()
        d, flags = hdul[0].data, hdul["FLAGS"].data
        for ch in range(g.NC):
            if ch in g.FLAGGED_CHANNELS:
                self.assertEqual(flags[ch], 1)
                self.assertTrue(np.isfinite(d[ch][~np.isnan(d[ch])]).all())
                self.assertGreater(np.isfinite(d[ch]).sum(), 0)   # flagged is not the same as missing
            else:
                self.assertEqual(flags[ch], 0)

    def test_incompatible_variants_differ_where_they_should(self):
        base = g.hi_cube()[0].header
        gal = g.cube_other_frame()[0].header
        coarse = g.cube_coarser_pixels()[0].header
        self.assertNotEqual(base["CTYPE1"][:4], gal["CTYPE1"][:4])
        self.assertEqual(base["CTYPE1"], coarse["CTYPE1"])
        self.assertNotEqual(base["CDELT2"], coarse["CDELT2"])

    def test_vgt_file_round_trips_with_units_and_axial_label(self):
        with fits.open(self.files["vgt_result.fits"]) as h:
            self.assertEqual(h["ANGLE"].header["BUNIT"], "deg")
            self.assertEqual(h["ANGLE"].header["ORIENT"], "axial")
            np.testing.assert_array_equal(h["ANGLE"].data, g.vgt_angle_map()["angle_deg"])

    def test_missing_spectral_reference_is_detectable(self):
        h = g.cube_missing_spectral_reference()[0].header
        self.assertNotIn("CRVAL3", h)
        self.assertNotIn("CDELT3", h)

    def test_unlabelled_cube_has_no_celestial_keys(self):
        h = g.cube_unlabelled_pixels()[0].header
        for k in ("CTYPE1", "CRVAL1", "CDELT1", "CTYPE2"):
            self.assertNotIn(k, h)


class AxialStatistics(unittest.TestCase):
    def test_one_and_179_degrees_average_to_zero_not_ninety(self):
        mean, r = g.axial_mean_deg([1.0, 179.0])
        self.assertTrue(min(mean, 180.0 - mean) < 1e-9)       # 0 and 180 are the same orientation
        self.assertGreater(r, 0.99)
        self.assertAlmostEqual(float(np.mean([1.0, 179.0])), 90.0)   # the naive mean is wrong

    def test_perpendicular_pair_is_degenerate(self):
        mean, r = g.axial_mean_deg([0.0, 90.0])
        self.assertIsNone(mean)
        self.assertLess(r, 1e-6)

    def test_nan_is_skipped_not_counted_as_zero(self):
        mean, _ = g.axial_mean_deg([45.0, float("nan"), 45.0])
        self.assertAlmostEqual(mean, 45.0, places=9)

    def test_map_keeps_zero_reliability_distinct_from_missing(self):
        m = g.vgt_angle_map()
        self.assertEqual(m["reliability"][1, 0], 0.0)
        self.assertTrue(np.isnan(m["reliability"][3, 2]))
        self.assertTrue(np.isnan(m["angle_deg"][3, 2]))


if __name__ == "__main__":
    unittest.main()
