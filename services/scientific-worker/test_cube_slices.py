"""Channel maps and position-velocity diagrams (ADR 0049) against answers computed independently in the tests: a synthetic cube
with a Gaussian line whose velocity changes linearly with x (the gradient is known), astropy separations for the offsets,
scipy's map_coordinates for the interpolation, numpy percentiles for the shared scale and the existing `convert` operation
(ADR 0040) for the channel labels. The worker is driven through its line protocol, as the API drives it."""
import math
import warnings
import sys
import tempfile
import unittest
from pathlib import Path

import numpy as np
from astropy.coordinates import SkyCoord
from astropy.io import fits
from astropy.wcs import WCS

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import cube_slices as cs  # noqa: E402
from test_plane_worker import Client  # noqa: E402

warnings.filterwarnings("ignore", category=RuntimeWarning)   # nanmean of an all-NaN column is the point of some tests

NX, NY, NV = 41, 21, 30
SCALE = 1.0 / 60.0           # 1 arcmin pixels
V0, DV = 0.0, 1000.0         # m/s of channel 0 and channel width
GRAD = 2.0                   # channels of line-centre shift per pixel along x
SIGMA = 1.5                  # channels


def header(spec="VRAD", nv=NV, extra=None):
    h = fits.Header()
    h["CTYPE1"], h["CTYPE2"], h["CTYPE3"] = "RA---TAN", "DEC--TAN", spec
    h["CRVAL1"], h["CRVAL2"] = 150.0, 2.0
    h["CRPIX1"], h["CRPIX2"], h["CRPIX3"] = NX // 2 + 1, NY // 2 + 1, 1
    h["CDELT1"], h["CDELT2"] = -SCALE, SCALE
    h["CUNIT1"] = h["CUNIT2"] = "deg"
    if spec == "FREQ":
        h["CRVAL3"], h["CDELT3"], h["CUNIT3"] = 1.4204e9, 5.0e3, "Hz"
        h["RESTFRQ"] = 1.420405751768e9
    else:
        h["CRVAL3"], h["CDELT3"], h["CUNIT3"] = V0, DV, "m/s"
    h["BUNIT"] = "K"
    h["RADESYS"] = "ICRS"
    for k, v in (extra or {}).items():
        h[k] = v
    return h


def moving_gaussian() -> np.ndarray:
    """(channels, y, x): a line at channel 15 + GRAD * (x - 20), the same at every y, amplitude 10 K."""
    v = np.arange(NV)[:, None, None]
    x = np.arange(NX)[None, None, :]
    centre = 15.0 + GRAD * (x - 20)
    return (10.0 * np.exp(-((v - centre) ** 2) / (2 * SIGMA ** 2)) * np.ones((1, NY, 1))).astype("<f4")


def write(path, data, h):
    fits.PrimaryHDU(np.asarray(data, dtype="<f4"), header=h).writeto(path, overwrite=True)


def centroid_channel(col: np.ndarray) -> float:
    w = np.where(np.isfinite(col), col, 0.0)
    return float((w * np.arange(len(col))).sum() / w.sum())


def pv_arrays(head, payload):
    n, nc = head["offsets"], head["channels_returned"]
    off = np.frombuffer(payload, dtype="<f8", count=n)
    axis = np.frombuffer(payload, dtype="<f8", count=nc, offset=8 * n)
    data = np.frombuffer(payload, dtype="<f4", count=nc * n, offset=8 * (n + nc)).reshape(nc, n)
    counts = np.frombuffer(payload, dtype="u1", count=nc * n, offset=8 * (n + nc) + 4 * nc * n).reshape(nc, n)
    return off, axis, data, counts


class CubeSlices(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        cls.d = Path(cls.tmp.name)
        cls.c = Client()
        cls.data = moving_gaussian()
        cls.cube = cls.d / "cube.fits"
        write(cls.cube, cls.data, header())

    @classmethod
    def tearDownClass(cls):
        cls.c.close()
        cls.tmp.cleanup()

    def pv(self, path=None, **kw):
        req = dict(op="pv", path=str(path or self.cube), points=[[2, 10], [38, 10]], width=1, width_unit="pixel")
        req.update(kw)
        return self.c.ask(**req)

    def no(self, head, msg=None):
        self.assertFalse(head["ok"], msg)
        self.assertTrue(head["error"])
        return head

    # ---------- position-velocity ----------

    def test_pv_of_a_moving_gaussian_recovers_the_velocity_gradient(self):
        head, payload = self.pv()
        off, axis, data, counts = pv_arrays(head, payload)
        self.assertEqual(head["offset"]["unit"], "arcsec")
        self.assertEqual(data.shape, (NV, len(off)))
        # the line centre (in channels) at each offset, from the centroid of its column; its slope against the offset in arcsec
        keep = [i for i in range(len(off)) if 5 < centroid_channel(data[:, i]) < NV - 6]
        cen = np.array([centroid_channel(data[:, i]) for i in keep])
        slope_ch_per_arcsec = np.polyfit(off[keep], cen, 1)[0]
        # x runs along the path, so a step of 1 pixel is 60 arcsec (TAN, 1 arcmin pixels, small field) and GRAD channels of DV m/s
        want = GRAD / 60.0
        self.assertAlmostEqual(slope_ch_per_arcsec, want, delta=want * 0.01)
        km_s_per_arcsec = np.polyfit(off[keep], np.interp(cen, np.arange(NV), axis) / 1000.0, 1)[0]
        self.assertAlmostEqual(km_s_per_arcsec, GRAD * DV / 1000.0 / 60.0, delta=0.001)
        self.assertEqual(head["interpolation"]["mode"], "linear")
        self.assertIn("bilinear", head["interpolation"]["method"])

    def test_offsets_are_astropy_separations_not_pixel_counts(self):
        # a diagonal path over a wide field (half-degree pixels at 60 degrees declination), where the tangent-plane distortion makes
        # the sky distance differ clearly from the pixel count times the reference scale
        h = header(extra={"CRVAL2": 60.0, "CDELT1": -0.5, "CDELT2": 0.5})
        p = self.d / "dec60.fits"
        write(p, self.data, h)
        head, payload = self.pv(path=p, points=[[3, 2], [37, 18]])
        off, _, _, _ = pv_arrays(head, payload)
        w = WCS(h).celestial
        n = head["offsets"]
        t = np.linspace(0, 1, n)
        x, y = 3 + t * 34, 2 + t * 16
        sky = SkyCoord(*w.pixel_to_world_values(x, y), unit="deg", frame="icrs")
        want = np.concatenate([[0.0], np.cumsum(sky[:-1].separation(sky[1:]).arcsec)])
        np.testing.assert_allclose(off, want, rtol=0, atol=1e-6)
        self.assertEqual(off[0], 0.0)
        # the end-to-end separation is the great-circle distance, and it is not 60 arcsec per pixel of path length
        self.assertAlmostEqual(off[-1], sky[0].separation(sky[-1]).arcsec, delta=0.05)
        naive = math.hypot(34, 16) * 0.5 * 3600.0
        self.assertGreater(abs(off[-1] - naive), 100.0)
        self.assertAlmostEqual(head["path"]["length_pixels"], math.hypot(34, 16), places=9)
        self.assertLessEqual(head["path"]["spacing_pixels"], 1.0)

    def test_width_averaging_excludes_nan_and_counts(self):
        data = self.data.copy()
        data[:, 11, 10:15] = np.nan                  # one of three rows across the path blank at x = 10..14
        data[:, 9:12, 20] = np.nan                   # every row blank at x = 20
        p = self.d / "holes.fits"
        write(p, data, header())
        head, payload = self.pv(path=p, points=[[0, 10], [40, 10]], width=3, interpolation="nearest")
        off, _, out, counts = pv_arrays(head, payload)
        self.assertEqual(head["width"]["cross_samples"], 3)
        i = lambda x: int(np.argmin(np.abs(np.arange(head["offsets"]) * 1.0 - x)))   # path from x=0 to 40 at one sample per pixel
        self.assertTrue(np.all(counts[:, i(3)] == 3))
        self.assertTrue(np.all(counts[:, i(12)] == 2))            # NaN row excluded, not counted as zero
        self.assertTrue(np.all(counts[:, i(20)] == 0))
        self.assertTrue(np.all(np.isnan(out[:, i(20)])))          # nothing finite: unknown, never 0
        # the mean of two finite rows equals the source's (the rows are identical here), not two thirds of it
        np.testing.assert_allclose(out[:, i(12)], self.data[:, 10, 12], rtol=1e-5)
        self.assertEqual(head["interpolation"]["mode"], "nearest")

    def test_linear_interpolation_drops_a_sample_whose_neighbour_has_no_value_and_matches_scipy_elsewhere(self):
        from scipy.ndimage import map_coordinates
        rng = np.random.default_rng(3)
        block = rng.normal(size=(4, 12, 15)).astype("<f4")
        xs, ys = rng.uniform(0, 14, 50), rng.uniform(0, 11, 50)
        got = cs.interpolate(block, 0, 0, xs, ys, 15, 12, "linear")
        for k in range(4):
            want = map_coordinates(block[k].astype(float), [ys, xs], order=1, mode="nearest")
            np.testing.assert_allclose(got[k], want, atol=1e-6)
        # a sub-block read at an offset gives the same values
        sub = block[:, 3:10, 4:12]
        inside = (xs >= 4) & (xs <= 10) & (ys >= 3) & (ys <= 8)
        np.testing.assert_allclose(cs.interpolate(sub, 4, 3, xs[inside], ys[inside], 15, 12, "linear"), got[:, inside], atol=1e-6)
        # a NaN pixel contaminates only samples that weigh it; a sample exactly on another pixel does not
        b = block.copy()
        b[:, 5, 5] = np.nan
        out = cs.interpolate(b, 0, 0, np.array([5.0, 5.4, 6.0, 8.0]), np.array([5.0, 5.0, 5.0, 5.0]), 15, 12, "linear")
        self.assertTrue(np.all(np.isnan(out[:, 0])) and np.all(np.isnan(out[:, 1])))
        self.assertTrue(np.all(np.isfinite(out[:, 2])) and np.all(np.isfinite(out[:, 3])))
        # nearest picks a pixel and never mixes
        near = cs.interpolate(block, 0, 0, np.array([2.4, 2.6]), np.array([3.0, 3.0]), 15, 12, "nearest")
        np.testing.assert_array_equal(near[:, 0], block[:, 3, 2])
        np.testing.assert_array_equal(near[:, 1], block[:, 3, 3])

    def test_the_spectral_axis_is_labelled_and_converted_with_the_conventions_of_adr_0040(self):
        p = self.d / "freq.fits"
        write(p, self.data, header("FREQ"))
        head, payload = self.pv(path=p, unit="km/s", convention="radio")
        _, axis, _, _ = pv_arrays(head, payload)
        self.assertEqual(head["spectral"]["unit"], "km/s")
        self.assertEqual(head["spectral"]["convention_source"], "user")
        for ch in (0, 7, 29):
            want = self.c.ask(op="convert", path=str(p), x=0, y=0, channel=ch, spectral="km/s", convention="radio")[0]["spectral"]["converted"]["value"]
            self.assertAlmostEqual(axis[ch], want, places=9)
        # a conversion that is refused leaves the native axis and says why
        head2, payload2 = self.pv(path=p, unit="km/s")
        self.assertIsNotNone(head2["spectral"]["refused"])
        self.assertEqual(head2["spectral"]["unit"], "Hz")

    def test_pv_without_a_celestial_wcs_is_in_pixels_and_refuses_an_angular_width(self):
        h = header()
        for k in ("CTYPE1", "CTYPE2"):
            h[k] = "X" if k == "CTYPE1" else "Y"
        p = self.d / "plain.fits"
        write(p, self.data, h)
        head, payload = self.pv(path=p)
        off, _, _, _ = pv_arrays(head, payload)
        self.assertEqual(head["offset"]["unit"], "pixel")
        self.assertAlmostEqual(off[-1], 36.0, places=9)
        self.assertIn("celestial", self.no(self.pv(path=p, width=60, width_unit="arcsec")[0])["error"])

    def test_an_angular_width_becomes_whole_pixels_and_the_note_says_how(self):
        head, _ = self.pv(width=180, width_unit="arcsec")
        self.assertEqual(head["width"]["cross_samples"], 3)
        self.assertIn("arcsec per pixel", head["width"]["note"])

    def test_a_polyline_follows_its_vertices(self):
        head, payload = self.pv(points=[[2, 4], [20, 4], [20, 16]])
        off, _, data, _ = pv_arrays(head, payload)
        self.assertAlmostEqual(head["path"]["length_pixels"], 18 + 12, places=9)
        # the first leg runs along x (the line moves), the second runs along y (the line does not)
        a, b = data[:, 5], data[:, -3]
        self.assertGreater(abs(centroid_channel(np.nan_to_num(data[:, 20])) - centroid_channel(np.nan_to_num(data[:, 2]))), 5)
        self.assertLess(abs(centroid_channel(np.nan_to_num(data[:, -2])) - centroid_channel(np.nan_to_num(data[:, -10]))), 0.05)
        self.assertTrue(np.all(np.diff(off) > 0))

    def test_caps_refuse_before_any_read(self):
        for kw in (dict(points=[[0, 0], [40, 20]], width=100, width_unit="pixel"),
                   dict(points=[[0, 0]]), dict(points=[[0, 0], [5000, 5000]]),
                   dict(width=0), dict(interpolation="cubic"), dict(first=5, last=2), dict(points=[[3, 3], [3, 3]])):
            self.no(self.pv(**kw)[0], str(kw))

    def test_a_path_leaving_the_image_gives_no_value_there(self):
        head, payload = self.pv(points=[[-10, 10], [30, 10]])
        off, _, data, counts = pv_arrays(head, payload)
        self.assertGreater(head["outside_samples"], 0)
        self.assertTrue(np.all(np.isnan(data[:, 0])) and np.all(counts[:, 0] == 0))
        self.assertTrue(np.any(np.isfinite(data[:, -1])))

    # ---------- slab ----------

    def test_a_slab_mean_excludes_nan_and_says_how_many(self):
        data = self.data.copy()
        data[10:13, 5, 7] = np.nan                   # three channels blank at one pixel
        data[:, 6, 7] = np.nan                       # all channels blank at another
        p = self.d / "slab.fits"
        write(p, data, header())
        head, payload = self.c.ask(op="slab", path=str(p), first=9, last=14)
        a = np.frombuffer(payload, dtype="<f4").reshape(head["height"], head["width"])
        want = np.nanmean(data[9:15], axis=0)
        np.testing.assert_allclose(a[np.isfinite(want)], want[np.isfinite(want)], rtol=1e-5)
        self.assertAlmostEqual(float(a[5, 7]), float(np.nanmean(data[[9, 13, 14], 5, 7])), places=5)
        self.assertTrue(np.isnan(a[5, 7]) == np.isnan(want[5, 7]))
        self.assertEqual(head["slab"]["channels"], 6)
        self.assertEqual(head["slab"]["excluded_samples"], int(np.isnan(data[9:15]).sum()))
        self.assertEqual(head["slab"]["pixels_without_value"], 0 if not np.isnan(data[9:15, 6, 7]).all() else 1)
        self.assertIn("mean of the finite values of channels 9-14", head["slab"]["method"])
        self.assertTrue(np.isnan(a[6, 7]))           # a pixel with no finite value is NaN, not 0

    def test_a_slab_preview_is_block_mean_of_the_slab_mean(self):
        head, payload = self.c.ask(op="slab", path=str(self.cube), first=10, last=13, step=4)
        a = np.frombuffer(payload, dtype="<f4").reshape(head["height"], head["width"])
        full, _ = self.c.ask(op="slab", path=str(self.cube), first=10, last=13)[0], None
        self.assertEqual(head["preview"]["step"], 4)
        self.assertIn("block of the slab mean", head["preview"]["method"])
        self.assertEqual((head["width"], head["height"]), (math.ceil(NX / 4), math.ceil(NY / 4)))
        want = np.nanmean(self.data[10:14], axis=0)
        np.testing.assert_allclose(a[0, 0], want[0:4, 0:4].mean(), rtol=1e-5)

    # ---------- channel grid ----------

    def grid(self, path=None, **kw):
        req = dict(op="channel_grid", path=str(path or self.cube), first=0, last=NV - 1, stride=3, average=1)
        req.update(kw)
        return self.c.ask(**req)[0]

    def test_panels_follow_the_range_stride_and_average(self):
        h = self.grid(first=2, last=20, stride=5, average=3)
        self.assertEqual([(p["first"], p["last"]) for p in h["panels"]], [(2, 4), (7, 9), (12, 14), (17, 19)])
        self.assertTrue(all(p["mean"] for p in h["panels"]))
        self.assertEqual(h["averaging"]["channels"], 3)
        h = self.grid(first=0, last=7, stride=3, average=1)
        self.assertEqual([(p["first"], p["last"]) for p in h["panels"]], [(0, 0), (3, 3), (6, 6)])
        self.assertIsNone(h["averaging"])
        # the last panel is cut at the end of the range, never beyond it
        h = self.grid(first=0, last=10, stride=4, average=4)
        self.assertEqual([(p["first"], p["last"]) for p in h["panels"]], [(0, 3), (4, 7), (8, 10)])

    def test_labels_match_the_readout_conversions(self):
        p = self.d / "freq2.fits"
        write(p, self.data, header("FREQ"))
        h = self.grid(path=p, first=0, last=NV - 1, stride=7, average=2, unit="km/s", convention="radio")
        sp = h["spectral"]
        self.assertEqual((sp["unit"], sp["convention"], sp["convention_source"], sp["specsys"]), ("km/s", "radio", "user", None))
        self.assertEqual(sp["rest"]["source"], "RESTFRQ")
        for pn in h["panels"]:
            for which, ch in (("first", pn["first"]), ("last", pn["last"])):
                want = self.c.ask(op="convert", path=str(p), x=0, y=0, channel=ch, spectral="km/s", convention="radio")[0]["spectral"]["converted"]["value"]
                self.assertAlmostEqual(pn["spectral"][which], want, places=9)
        # native labels when no unit is asked for; the file's own axis unit and status
        n = self.grid(path=p)
        self.assertEqual((n["spectral"]["unit"], n["spectral"]["unit_status"]), ("Hz", "declared"))
        want0 = self.c.ask(op="coord", path=str(p), x=0, y=0, channel=3)[0]["spectral"]["value"]
        self.assertAlmostEqual(n["panels"][1]["spectral"]["first"], want0, places=3)
        # a refusal (no convention for a frequency axis) keeps native values and says why
        r = self.grid(path=p, unit="km/s")
        self.assertIsNotNone(r["spectral"]["refused"])
        self.assertEqual(r["spectral"]["unit"], "Hz")

    def test_one_shared_scale_from_the_pooled_panels_or_the_custom_limits(self):
        h = self.grid(first=0, last=NV - 1, stride=5, average=1)
        lo, hi = h["scale"]["lo"], h["scale"]["hi"]
        idx = h["scale"]["sampled_panels"]
        pooled = np.concatenate([self.data[h["panels"][i]["first"]].ravel() for i in idx])
        self.assertAlmostEqual(lo, float(np.percentile(pooled, 1)), places=4)
        self.assertAlmostEqual(hi, float(np.percentile(pooled, 99)), places=4)
        self.assertIn("one scale for every panel", h["scale"]["source"])
        # the same limits whatever the unit asked for or the panels shown
        self.assertEqual(self.grid(first=0, last=NV - 1, stride=5, unit="km/s")["scale"]["lo"], lo)
        c = self.grid(limits=[0.5, 7.0])
        self.assertEqual((c["scale"]["lo"], c["scale"]["hi"], c["scale"]["sampled_panels"]), (0.5, 7.0, []))
        self.no(self.grid(limits=[3.0, 3.0]))

    def test_caps(self):
        self.assertTrue(self.grid(first=0, last=NV - 1, stride=1, average=1)["ok"])      # 30 panels: fine
        big = self.d / "many.fits"
        write(big, np.zeros((70, 4, 4), dtype="<f4"), header(nv=70))
        self.assertIn("panels", self.no(self.grid(path=big, first=0, last=69, stride=1))["error"])
        self.assertIn("share channels", self.no(self.grid(stride=2, average=3))["error"])
        self.no(self.grid(first=9, last=3))
        self.no(self.grid(average=300))

    def test_a_large_plane_is_previewed_by_block_mean_within_the_pixel_caps(self):
        w = 2400
        big = self.d / "wide.fits"
        h = header()
        h["CRPIX1"], h["CRPIX2"] = w // 2, w // 2
        write(big, np.ones((3, w, w), dtype="<f4"), h)
        g = self.grid(path=big, first=0, last=2, stride=1)
        self.assertGreater(g["step"], 1)
        self.assertLessEqual(g["pixels_per_panel"], cs.DEFAULT_PANEL_PIXELS)
        self.assertLessEqual(g["pixels_total"], cs.MAX_GRID_PIXELS)
        self.assertIn("preview", g["preview"]["method"])
        self.assertEqual(g["panel_width"], math.ceil(w / g["step"]))

    def test_a_two_dimensional_file_has_no_channel_grid(self):
        p = self.d / "two.fits"
        h = fits.Header()
        h["CTYPE1"], h["CTYPE2"] = "RA---TAN", "DEC--TAN"
        h["CRVAL1"], h["CRVAL2"], h["CRPIX1"], h["CRPIX2"], h["CDELT1"], h["CDELT2"] = 150.0, 2.0, 5, 5, -SCALE, SCALE
        write(p, np.ones((9, 9)), h)
        self.assertIn("channel axis", self.no(self.grid(path=p))["error"])


if __name__ == "__main__":
    unittest.main()
