import json
import math
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

import numpy as np
from astropy.io import fits

HERE = Path(__file__).resolve().parent
FIX = HERE.parent.parent / "fixtures"
sys.path.insert(0, str(FIX))
import generate as g  # noqa: E402
from test_fixtures import tan_world  # noqa: E402  (independent TAN oracle)

SCRIPT = str(HERE / "plane_worker.py")


class Client:
    """Talks the line protocol to one long-lived worker process."""

    def __init__(self, roots=None, env_extra=None):
        env = dict(os.environ)
        env.pop("VESQIRA_ROOTS", None)
        if roots is not None:
            env["VESQIRA_ROOTS"] = json.dumps([str(r) for r in roots])
        env.pop("VESQIRA_CACHE", None)
        env.update(env_extra or {})
        self.p = subprocess.Popen([sys.executable, "-P", SCRIPT], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                  stderr=subprocess.DEVNULL, env=env)

    def ask(self, **req):
        self.p.stdin.write(json.dumps(req).encode() + b"\n")
        self.p.stdin.flush()
        head = json.loads(self.p.stdout.readline())
        payload = self.p.stdout.read(head["nbytes"]) if head["nbytes"] else b""
        assert len(payload) == head["nbytes"]
        return head, payload

    def plane(self, path, channel):
        head, payload = self.ask(op="plane", path=str(path), channel=channel)
        if not head["ok"]:
            return head, None
        return head, np.frombuffer(payload, dtype="<f4").reshape(head["height"], head["width"])

    def close(self):
        self.p.stdin.close()
        self.p.wait(timeout=10)


class PlaneWorker(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        cls.f = g.write_all(Path(cls.tmp.name))
        cls.c = Client()

    @classmethod
    def tearDownClass(cls):
        cls.c.close()
        cls.tmp.cleanup()

    def test_preview_is_the_block_mean_of_finite_pixels_and_says_it_is_a_preview(self):
        path = self.f["hi_cube.fits"]
        _, full = self.c.plane(path, 3)
        step = 4
        head, payload = self.c.ask(op="plane", path=str(path), channel=3, step=step)
        self.assertTrue(head["ok"])
        w, h = head["width"], head["height"]
        small = np.frombuffer(payload, "<f4").reshape(h, w)
        self.assertEqual((w, h), (-(-full.shape[1] // step), -(-full.shape[0] // step)))
        self.assertEqual(head["preview"]["step"], step)
        self.assertEqual((head["preview"]["source_width"], head["preview"]["source_height"]), (full.shape[1], full.shape[0]))
        self.assertIn("not for measurement", head["preview"]["method"])
        for j in range(h):                                   # independent per-block oracle, including partial edge blocks
            for i in range(w):
                blk = full[j * step:(j + 1) * step, i * step:(i + 1) * step]
                fin = blk[np.isfinite(blk)]
                if fin.size:
                    self.assertAlmostEqual(float(small[j, i]), float(fin.astype(np.float64).mean()), places=5)
                else:
                    self.assertTrue(np.isnan(small[j, i]))   # a block with no value is unknown, never 0
        full_head, _ = self.c.ask(op="plane", path=str(path), channel=3)
        self.assertIsNone(full_head["preview"])
        for bad in (0, -1, 65, 2.5, True, "2"):
            self.assertFalse(self.c.ask(op="plane", path=str(path), channel=3, step=bad)[0]["ok"])

    def test_a_request_over_the_memory_cap_fails_alone_and_the_worker_keeps_serving(self):
        big = Path(self.tmp.name) / "big_plane.fits"
        fits.PrimaryHDU(np.random.default_rng(0).random((3000, 3000), dtype=np.float32)).writeto(big)
        env = dict(os.environ, VESQIRA_WORKER_MEM_MB="40")          # far below what a 9-megapixel plane needs to be processed
        env.pop("VESQIRA_ROOTS", None)
        p = subprocess.Popen([sys.executable, "-P", SCRIPT], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, env=env)

        def ask(**req):
            p.stdin.write(json.dumps(req).encode() + b"\n")
            p.stdin.flush()
            head = json.loads(p.stdout.readline())
            p.stdout.read(head["nbytes"])
            return head
        try:
            over = ask(op="plane", path=str(big), channel=0)
            self.assertFalse(over["ok"])
            self.assertEqual(over["error"], "over the memory cap (40 MB)")
            small = self.f["vgt_psi_deg.fits"]
            self.assertTrue(ask(op="plane", path=str(small), channel=0)["ok"])    # same process, next request
        finally:
            p.stdin.close()
            p.wait(timeout=10)

    def test_sky_positions_land_on_the_pixels_the_independent_projection_says(self):
        path = self.f["hi_cube.fits"]
        pts = [(10.0, 7.0), (3.25, 15.5), (0.0, 0.0)]
        sky = [list(tan_world(x, y, g.CRVAL_RA, g.CRVAL_DEC, -g.CDELT_DEG, g.CDELT_DEG, g.CRPIX)) for x, y in pts]
        head, _ = self.c.ask(op="sky2pix", path=str(path), points=sky)
        self.assertTrue(head["ok"], head)
        for (x, y), got in zip(pts, head["pixels"]):                       # the inverse of the independent forward formula
            self.assertAlmostEqual(got[0], x, places=5)
            self.assertAlmostEqual(got[1], y, places=5)
        sx, sy = head["pixel_scale_arcsec"]
        self.assertAlmostEqual(sx, g.CDELT_DEG * 3600, places=4)
        self.assertAlmostEqual(sy, g.CDELT_DEG * 3600, places=4)
        self.assertAlmostEqual(head["north_angle_deg"], 0.0, delta=0.05)    # north is up in this grid (meridians converge slightly away from the reference)
        self.assertEqual(head["sky"]["frame"], "ICRS")
        for bad in ([], [[1.0]], [[1.0, 95.0]], [["a", 2]], [[float("nan"), 2]], [[1, True]]):
            self.assertFalse(self.c.ask(op="sky2pix", path=str(path), points=bad)[0]["ok"])
        self.assertFalse(self.c.ask(op="sky2pix", path=str(path), points=[[1.0, 2.0]] * 65)[0]["ok"])

    def test_a_cube_cut_short_still_serves_the_planes_that_are_there(self):
        full = self.f["hi_cube.fits"]
        cut = Path(self.tmp.name) / "cut_short.fits"
        data = Path(full).read_bytes()
        with fits.open(full) as h:
            plane_bytes = h[0].header["NAXIS1"] * h[0].header["NAXIS2"] * 4
            offset = h[0]._data_offset
        cut.write_bytes(data[: offset + 5 * plane_bytes])                       # 5 of the 64 channels survive
        head, plane = self.c.plane(cut, 1)
        self.assertTrue(head["ok"], head)
        with fits.open(full) as h:
            np.testing.assert_array_equal(np.nan_to_num(plane, nan=-1), np.nan_to_num(h[0].data[1].astype(np.float32), nan=-1))

    def test_north_points_the_same_way_near_the_pole_as_elsewhere(self):
        path = self.f["hi_cube.fits"]
        for dec in (2.0, 89.5):                                                   # the fixture grid is rotation free
            head, _ = self.c.ask(op="sky2pix", path=str(path), points=[[150.0, dec]])
            self.assertTrue(head["ok"], head)
        near, _ = self.c.ask(op="sky2pix", path=str(path), points=[[150.0, 2.0]])
        self.assertAlmostEqual(near["north_angle_deg"], 0.0, delta=0.05)

    def test_view_locates_axes_from_the_wcs(self):
        v, _ = self.c.ask(op="view", path=str(self.f["hi_cube.fits"]))
        self.assertTrue(v["ok"])
        self.assertEqual((v["width"], v["height"], v["channels"]), (g.NX, g.NY, g.NC))
        self.assertEqual(v["fits_axes"], {"x": 1, "y": 2, "channel": 3})
        self.assertEqual(v["spectral"]["ctype"], "VRAD")
        self.assertEqual(v["spectral"]["unit"], "m/s")
        self.assertTrue(v["spectral"]["resolved"])
        self.assertEqual(v["sky"]["frame"], "ICRS")
        self.assertEqual(v["bunit"], "K")

    def _without_bunit(self, name):
        src = self.f[name]
        dst = Path(self.tmp.name) / ("nobunit_" + name)
        with fits.open(src) as hdul:
            del hdul[0].header["BUNIT"]
            hdul.writeto(dst, overwrite=True)
        return dst

    def test_a_declared_unit_fills_a_missing_bunit_and_is_labelled_as_the_person_s(self):
        p = self._without_bunit("hi_cube.fits")
        v, _ = self.c.ask(op="view", path=str(p))
        self.assertEqual((v["bunit"], v["bunit_status"]), (None, "unresolved"))
        for op, extra in (("view", {}), ("plane", {"channel": 3}), ("coord", {"channel": 3, "x": 2, "y": 2}),
                          ("spectrum", {"shape": "rect", "x0": 1, "y0": 1, "x1": 4, "y1": 4})):
            h, _ = self.c.ask(op=op, path=str(p), declared_bunit="Jy/beam", **extra)
            self.assertTrue(h["ok"], (op, h))
            self.assertEqual((h["bunit"], h["bunit_status"]), ("Jy/beam", "user"), op)

    def test_the_file_s_own_bunit_always_wins_over_a_declared_one(self):
        for op, extra in (("view", {}), ("plane", {"channel": 3}), ("spectrum", {"shape": "rect", "x0": 1, "y0": 1, "x1": 4, "y1": 4})):
            h, _ = self.c.ask(op=op, path=str(self.f["hi_cube.fits"]), declared_bunit="Jy/beam", **extra)
            self.assertTrue(h["ok"], (op, h))
            self.assertEqual((h["bunit"], h["bunit_status"]), ("K", "declared"), op)

    def test_a_declared_unit_that_is_not_plain_short_text_is_refused_not_stored_or_echoed(self):
        p = self._without_bunit("hi_cube.fits")
        for bad in ("", " K", "K ", "K\nkm", "K\x00", "x" * 41, 7, ["K"], {"a": 1}, True):
            h, _ = self.c.ask(op="view", path=str(p), declared_bunit=bad)
            self.assertFalse(h["ok"], repr(bad))
            self.assertEqual(h["code"], "bad_request")
            self.assertNotIn("km", h.get("error", ""))
        # and the worker keeps serving
        h, _ = self.c.ask(op="view", path=str(p), declared_bunit="K")
        self.assertTrue(h["ok"])

    def test_plane_matches_a_full_astropy_load_including_nan(self):
        with fits.open(self.f["hi_cube.fits"]) as h:
            full = h[0].data
        head, plane = self.c.plane(self.f["hi_cube.fits"], g.SOURCE_CHANNEL)
        np.testing.assert_array_equal(plane, full[g.SOURCE_CHANNEL])   # NaN positions compare equal
        self.assertEqual(head["dtype"], "<f4")
        self.assertEqual(head["nbytes"], g.NX * g.NY * 4)
        self.assertEqual(head["stats"]["nonfinite_count"], 9)           # the 3x3 NaN box
        self.assertAlmostEqual(head["stats"]["max"], float(np.nanmax(full[g.SOURCE_CHANNEL])), places=5)

    def test_4d_cube_with_degenerate_stokes_uses_freq_not_axis_3(self):
        path = self.f["cube_stokes_freq.fits"]
        v, _ = self.c.ask(op="view", path=str(path))
        self.assertEqual(v["fits_axes"], {"x": 1, "y": 2, "channel": 4})
        self.assertEqual(v["channels"], g.NC)
        self.assertEqual(v["spectral"]["ctype"], "FREQ")
        self.assertEqual(v["spectral"]["unit"], "Hz")
        self.assertEqual(v["spectral"]["velocity"], "unresolved")       # no declared convention
        head, plane = self.c.plane(path, 5)
        _, ref = self.c.plane(self.f["hi_cube.fits"], 5)
        np.testing.assert_array_equal(plane, ref)
        self.assertAlmostEqual(head["spectral_value"], g.FREQ0_HZ + 5 * g.DFREQ_HZ, places=3)

    def test_int16_scaling_blank_is_nan_and_real_zero_stays_zero(self):
        path = self.f["cube_int16_scaled.fits"]
        with fits.open(path) as h:
            full = h[0].data                                           # astropy applies BSCALE/BZERO/BLANK
        bx, by, bc = g.SCALED_BLANK_PIX
        zx, zy, zc = g.SCALED_ZERO_PIX
        self.assertTrue(np.isnan(full[bc, by, bx]))
        head, plane = self.c.plane(path, bc)
        self.assertTrue(np.isnan(plane[by, bx]))
        raw = g.scaled_raw()[bc].astype(np.float64)
        expect = (raw * g.SCALED_BSCALE + g.SCALED_BZERO).astype(np.float32)   # independent float64 oracle
        expect[raw == g.SCALED_BLANK] = np.nan
        np.testing.assert_array_equal(plane, expect)
        np.testing.assert_allclose(plane, full[bc], rtol=1e-6, equal_nan=True)  # astropy scales in float32
        _, plane_z = self.c.plane(path, zc)
        self.assertEqual(float(plane_z[zy, zx]), 0.0)
        self.assertFalse(np.isnan(plane_z[zy, zx]))
        self.assertTrue(head["scaled"])
        self.assertEqual(head["source_bitpix"], 16)

    def test_coordinate_readout_matches_independent_tan_and_spectral_formula(self):
        x, y, c = g.SOURCE_PIX[0], g.SOURCE_PIX[1], g.SOURCE_CHANNEL
        r, _ = self.c.ask(op="coord", path=str(self.f["hi_cube.fits"]), x=x, y=y, channel=c)
        ra, dec = tan_world(x, y, g.CRVAL_RA, g.CRVAL_DEC, -g.CDELT_DEG, g.CDELT_DEG, g.CRPIX)
        self.assertAlmostEqual(r["sky"]["lon"], ra, places=8)
        self.assertAlmostEqual(r["sky"]["lat"], dec, places=8)
        self.assertAlmostEqual(r["spectral"]["value"], g.V0_MS + c * g.DV_MS, places=6)   # 0-based channel c is FITS pixel c+1
        # the reference pixel (0-based = CRPIX-1) must map to CRVAL exactly
        r0, _ = self.c.ask(op="coord", path=str(self.f["hi_cube.fits"]), x=int(g.CRPIX[0]) - 1, y=int(g.CRPIX[1]) - 1, channel=0)
        self.assertAlmostEqual(r0["sky"]["lon"], g.CRVAL_RA, places=9)
        self.assertAlmostEqual(r0["sky"]["lat"], g.CRVAL_DEC, places=9)

    def test_missing_spectral_reference_is_unresolved_never_default_values(self):
        path = self.f["hi_cube_no_spectral_ref.fits"]
        v, _ = self.c.ask(op="view", path=str(path))
        self.assertFalse(v["spectral"]["resolved"])
        self.assertEqual(v["spectral"]["velocity"], "unresolved")
        self.assertIsNone(v["spectral"]["unit"])
        head, plane = self.c.plane(path, 3)
        self.assertIsNone(head["spectral_value"])
        self.assertIsNotNone(plane)
        r, _ = self.c.ask(op="coord", path=str(path), x=1, y=1, channel=3)
        self.assertIsNone(r["spectral"])

    def test_legacy_velo_lsr_is_not_given_a_convention_or_frame(self):
        hdul = g.hi_cube()
        hdul[0].header["CTYPE3"] = "VELO-LSR"
        path = Path(self.tmp.name) / "legacy.fits"
        del hdul[0].header["SPECSYS"]
        hdul.writeto(path)
        v, _ = self.c.ask(op="view", path=str(path))
        self.assertEqual(v["spectral"]["velocity"], "unresolved")
        self.assertEqual(v["spectral"]["name"], "VELO-LSR")
        self.assertIsNone(v["spectral"]["specsys"])
        self.assertTrue(v["spectral"]["resolved"])
        self.assertIn("legacy", v["spectral"]["note"])

    def test_celestial_frame_is_inferred_from_equinox_only_when_radesys_is_absent(self):
        hdul = g.hi_cube()
        del hdul[0].header["RADESYS"]
        path = Path(self.tmp.name) / "noframe.fits"
        hdul.writeto(path)
        self.assertEqual(self.c.ask(op="view", path=str(path))[0]["sky"]["frame_status"], "unresolved")
        hdul[0].header["EQUINOX"] = 2000.0
        path2 = Path(self.tmp.name) / "equinox.fits"
        hdul.writeto(path2)
        sky = self.c.ask(op="view", path=str(path2))[0]["sky"]
        self.assertEqual((sky["frame"], sky["frame_status"]), ("FK5", "inferred"))

    def test_a_spectral_axis_wcslib_rejects_keeps_the_sky_axes(self):
        hdul = g.hi_cube()
        h = hdul[0].header
        h["CTYPE3"] = "FELO-HEL"                    # needs a rest frequency wcslib does not have
        del h["RESTFRQ"]
        path = Path(self.tmp.name) / "felo.fits"
        hdul.writeto(path)
        v, _ = self.c.ask(op="view", path=str(path))
        self.assertTrue(v["ok"], v)
        self.assertEqual(v["sky"]["kind"], "celestial")
        self.assertFalse(v["spectral"]["resolved"])

    def test_galactic_axes_never_take_radesys(self):
        hdul = g.cube_other_frame()
        hdul[0].header["RADESYS"] = "FK5"
        path = Path(self.tmp.name) / "gal.fits"
        hdul.writeto(path)
        sky = self.c.ask(op="view", path=str(path))[0]["sky"]
        self.assertEqual((sky["frame"], sky["frame_status"]), ("Galactic", "declared"))

    def test_float64_beyond_float32_range_is_counted_not_called_missing(self):
        data = np.zeros((2, 3, 4), dtype=">f8")
        data[1, 1, 1] = 1e300
        data[1, 0, 0] = np.nan
        h = fits.PrimaryHDU(data)
        h.header["CTYPE3"], h.header["CRVAL3"], h.header["CRPIX3"], h.header["CDELT3"] = "VRAD", 0.0, 1.0, 1.0
        path = Path(self.tmp.name) / "big_values.fits"
        h.writeto(path)
        head, plane = self.c.plane(path, 1)
        self.assertEqual(head["stats"]["overflow_count"], 1)
        self.assertEqual(head["stats"]["nonfinite_count"], 2)          # the NaN and the overflowed value
        self.assertEqual(head["source_bitpix"], -64)

    def test_symlinked_path_is_refused_and_errors_do_not_echo_paths(self):
        link = Path(self.tmp.name) / "link.fits"
        link.symlink_to(self.f["hi_cube.fits"])
        self.assertEqual(self.c.ask(op="view", path=str(link))[0]["code"], "bad_request")
        junk = Path(self.tmp.name) / "junk2.fits"
        junk.write_bytes(b"x" * 5000)
        self.assertNotIn(self.tmp.name, self.c.ask(op="view", path=str(junk))[0]["error"])

    def spectrum(self, path, **req):
        head, payload = self.c.ask(op="spectrum", path=str(path), **req)
        if not head["ok"]:
            return head, None
        nc = head["channels"]
        axis = np.frombuffer(payload, "<f8", nc, 0)
        mean = np.frombuffer(payload, "<f8", nc, 8 * nc)
        count = np.frombuffer(payload, "<u4", nc, 16 * nc)
        return head, (axis, mean, count)

    def test_rect_spectrum_matches_an_independent_numpy_mean_and_counts_nan(self):
        with fits.open(self.f["hi_cube.fits"]) as h:
            full = h[0].data.astype(np.float64)
        # a 6x6 box that contains the 3x3 NaN block (x 2-4, y 2-4)
        head, (axis, mean, count) = self.spectrum(self.f["hi_cube.fits"], shape="rect", x0=0, y0=0, x1=5, y1=5)
        sub = full[:, 0:6, 0:6]
        np.testing.assert_allclose(mean, np.nanmean(sub.reshape(g.NC, -1), axis=1), rtol=1e-12)
        self.assertTrue((count == 36 - 9).all())                        # NaN pixels excluded, and counted as excluded
        self.assertEqual(head["region"]["pixels"], 36)
        self.assertFalse(head["region"]["clipped"])
        np.testing.assert_allclose(axis, g.V0_MS + np.arange(g.NC) * g.DV_MS)
        self.assertEqual(head["axis"]["unit"], "m/s")
        self.assertIn("NaN", head["mask_policy"])
        sx = head["size"]["pixel_scale_arcsec"][0]
        self.assertAlmostEqual(sx, g.CDELT_DEG * 3600, places=6)
        self.assertAlmostEqual(head["size"]["area_arcmin2"], 36 * (g.CDELT_DEG * 60) ** 2, places=6)

    def test_spectrum_is_the_same_whatever_the_block_size_and_the_reader(self):
        # blocks of 1 channel, 3 channels and all channels must agree exactly, and so must the section reader that
        # is used when a file cannot be memory-mapped
        sys.path.insert(0, str(HERE))
        import plane_worker as pw
        want = self.spectrum(self.f["hi_cube.fits"], shape="rect", x0=0, y0=0, x1=5, y1=5)[1]
        old_block, old_array = pw.SPECTRUM_BLOCK_ELEMENTS, pw.fits_array
        try:
            for elements in (1, 36 * 3, 10 ** 9):
                for reader in (old_array, lambda hdul: hdul[0].section):
                    pw.SPECTRUM_BLOCK_ELEMENTS, pw.fits_array = elements, reader
                    head, payload = pw.Cube(os.path.realpath(self.f["hi_cube.fits"])).spectrum(
                        {"shape": "rect", "x0": 0, "y0": 0, "x1": 5, "y1": 5})
                    nc = head["channels"]
                    np.testing.assert_array_equal(np.frombuffer(payload, "<f8", nc, 8 * nc), want[1])
                    np.testing.assert_array_equal(np.frombuffer(payload, "<u4", nc, 16 * nc), want[2])
        finally:
            pw.SPECTRUM_BLOCK_ELEMENTS, pw.fits_array = old_block, old_array

    def test_a_map_without_a_channel_axis_has_a_one_channel_spectrum(self):
        with fits.open(self.f["vgt_psi_deg.fits"]) as h:
            full = h[0].data.astype(np.float64)
        head, (axis, mean, count) = self.spectrum(self.f["vgt_psi_deg.fits"], shape="rect", x0=1, y0=1, x1=6, y1=5)
        sub = full[1:6, 1:7]
        self.assertEqual(head["channels"], 1)
        np.testing.assert_allclose(mean, [np.nanmean(sub)], rtol=1e-12)
        self.assertEqual(int(count[0]), int(np.isfinite(sub).sum()))

    def test_all_nan_region_is_unknown_not_zero(self):
        head, (axis, mean, count) = self.spectrum(self.f["hi_cube.fits"], shape="rect", x0=2, y0=2, x1=4, y1=4)
        self.assertTrue(np.isnan(mean).all())
        self.assertTrue((count == 0).all())

    def test_circle_uses_pixel_centres_and_clips_at_the_edge(self):
        head, (axis, mean, count) = self.spectrum(self.f["hi_cube.fits"], shape="circle", cx=20, cy=9, r=3)
        yy, xx = np.mgrid[0:g.NY, 0:g.NX]
        inside = (xx - 20) ** 2 + (yy - 9) ** 2 <= 9
        self.assertEqual(head["region"]["pixels"], int(inside.sum()))
        with fits.open(self.f["hi_cube.fits"]) as h:
            np.testing.assert_allclose(mean, h[0].data[:, inside].astype(np.float64).mean(axis=1), rtol=1e-12)
        self.assertEqual(int(mean.argmax()), g.SOURCE_CHANNEL)         # the synthetic source peaks in its own channel
        edge, _ = self.spectrum(self.f["hi_cube.fits"], shape="circle", cx=0, cy=0, r=3)
        self.assertTrue(edge["region"]["clipped"])
        self.assertLess(edge["region"]["pixels"], edge["region"]["requested_pixels"])

    def test_spectrum_without_spectral_reference_uses_channel_numbers(self):
        head, (axis, mean, count) = self.spectrum(self.f["hi_cube_no_spectral_ref.fits"], shape="rect", x0=10, y0=10, x1=11, y1=11)
        self.assertEqual(head["axis"]["kind"], "channel")
        self.assertIsNone(head["axis"]["unit"])
        np.testing.assert_array_equal(axis, np.arange(g.NC))

    def test_blank_pixels_do_not_enter_the_mean(self):
        bx, by, bc = g.SCALED_BLANK_PIX
        head, (axis, mean, count) = self.spectrum(self.f["cube_int16_scaled.fits"], shape="rect", x0=0, y0=0, x1=g.SCALED_SHAPE[2] - 1, y1=g.SCALED_SHAPE[1] - 1)
        self.assertEqual(int(count[bc]), g.SCALED_SHAPE[1] * g.SCALED_SHAPE[2] - 1)
        raw = g.scaled_raw()[bc].astype(np.float64)
        keep = raw != g.SCALED_BLANK
        self.assertAlmostEqual(float(mean[bc]), float((raw[keep] * g.SCALED_BSCALE + g.SCALED_BZERO).mean()), places=9)

    def test_bad_regions_fail_in_a_controlled_way(self):
        p = self.f["hi_cube.fits"]
        for req in ({"shape": "rect", "x0": 100, "y0": 0, "x1": 120, "y1": 5}, {"shape": "blob"}, {"shape": "circle", "cx": 1, "cy": 1, "r": -1},
                    {"shape": "circle", "cx": 1, "cy": 1, "r": 10**9}, {"shape": "rect", "x0": "a", "y0": 0, "x1": 1, "y1": 1}):
            head, _ = self.spectrum(p, **req)
            self.assertEqual(head["code"], "bad_request", req)
        self.assertTrue(self.c.plane(p, 0)[0]["ok"])                    # worker still alive

    def axial(self, name="vgt_psi_deg.fits", unit="deg", weights=None, **region):
        req = {"op": "axial", "path": str(self.f[name]), "unit": unit, **region}
        if weights:
            req["weights_path"] = str(self.f[weights])
        return self.c.ask(**req)[0]

    @staticmethod
    def circ_dist(a, b):
        d = abs(a - b) % 180.0
        return min(d, 180.0 - d)

    def test_one_and_179_degrees_average_to_zero_not_ninety(self):
        r = self.axial(shape="rect", x0=0, y0=0, x1=1, y1=0)["stats"]["unweighted"]   # row 0 starts 1 deg, 179 deg
        self.assertTrue(r["defined"])
        self.assertLess(self.circ_dist(r["mean_deg"], 0.0), 1e-9)
        self.assertGreater(r["resultant"], 0.999)

    def test_perpendicular_orientations_have_no_mean(self):
        r = self.axial(shape="rect", x0=0, y0=3, x1=1, y1=3)["stats"]["unweighted"]   # 0 deg and 90 deg
        self.assertFalse(r["defined"])
        self.assertIsNone(r["mean_deg"])
        self.assertLess(r["resultant"], 1e-9)
        self.assertIn("cancel", r["reason"])

    def test_whole_map_matches_the_independent_oracle_and_skips_nan(self):
        st = self.axial()["stats"]
        angles = [a for a in g.vgt_angle_map()["angle_deg"].ravel() if not np.isnan(a)]
        mean, r = g.axial_mean_deg(angles)
        self.assertEqual(st["angles_excluded"], 1)
        self.assertEqual(st["unweighted"]["n"], len(angles))
        self.assertLess(self.circ_dist(st["unweighted"]["mean_deg"], mean), 1e-9)
        self.assertAlmostEqual(st["unweighted"]["resultant"], r, places=12)

    def test_radians_declared_as_degrees_is_flagged_and_the_right_unit_is_not(self):
        wrong = self.axial("vgt_psi_rad.fits", unit="deg")["stats"]
        self.assertTrue(wrong["values_fit_radians"])                 # every value is within pi: a hint to check the unit
        self.assertFalse(self.axial("vgt_psi_deg.fits", unit="deg")["stats"]["values_fit_radians"])
        self.assertFalse(self.axial("vgt_psi_rad.fits", unit="rad")["stats"]["values_fit_radians"])

    def test_radians_and_degrees_agree(self):
        a = self.axial(unit="deg")["stats"]["unweighted"]
        b = self.axial("vgt_psi_rad.fits", unit="rad")["stats"]["unweighted"]
        self.assertLess(self.circ_dist(a["mean_deg"], b["mean_deg"]), 1e-9)
        self.assertAlmostEqual(a["resultant"], b["resultant"], places=12)

    def test_reliability_weights_unknown_excluded_zero_kept(self):
        st = self.axial(weights="vgt_reliability.fits")["stats"]
        ang = g.vgt_angle_map()["angle_deg"].ravel()
        rel = (g.vgt_angle_map()["reliability"] * 0.5).astype(np.float32).astype(np.float64).ravel()
        ok = np.isfinite(ang) & np.isfinite(rel)
        z = (rel[ok] * np.exp(2j * np.radians(ang[ok]))).sum() / rel[ok].sum()
        w = st["weighted"]
        self.assertEqual(w["n"], int(ok.sum()))                       # NaN reliability or angle excluded
        self.assertAlmostEqual(w["resultant"], abs(z), places=6)
        self.assertLess(self.circ_dist(w["mean_deg"], (np.degrees(np.angle(z)) / 2.0) % 180.0), 1e-4)
        zero = (rel[ok] == 0).sum()
        self.assertGreater(zero, 0)                                   # the fixture has real zero reliabilities; they stay counted

    def test_tangential_field_has_the_documented_handedness(self):
        c = g.TANGENTIAL_N // 2
        def at(dx, dy):
            return self.axial("vgt_tangential_deg.fits", shape="rect", x0=c + dx, y0=c + dy, x1=c + dx, y1=c + dy)["stats"]["unweighted"]["mean_deg"]
        self.assertLess(self.circ_dist(at(5, 0), 90.0), 1e-9)       # right of the centre: vertical
        self.assertLess(self.circ_dist(at(0, 5), 0.0), 1e-9)        # above the centre (y up): horizontal
        self.assertLess(self.circ_dist(at(5, 5), 135.0), 1e-9)      # up-right diagonal
        self.assertLess(self.circ_dist(at(-5, 5), 45.0), 1e-9)      # up-left diagonal

    def test_axial_requests_that_make_no_sense_are_refused(self):
        self.assertEqual(self.axial(unit="turns")["code"], "bad_request")
        self.assertEqual(self.axial("hi_cube.fits")["code"], "unsupported")                       # a cube is not an orientation map
        self.assertEqual(self.axial(weights="vgt_tangential_deg.fits")["code"], "bad_request")   # different grid
        self.assertTrue(self.c.plane(self.f["hi_cube.fits"], 0)[0]["ok"])

    def test_a_2d_map_that_kept_the_cubes_third_axis_keywords_still_opens(self):
        hdul = g.vgt_tangential_deg()
        h = hdul[0].header
        h["CTYPE3"], h["CRVAL3"], h["CDELT3"], h["CRPIX3"], h["CUNIT3"] = "VELO-LSR", 181689.5, 1000.0, 1.0, "m/s"   # as in real VGT psi files
        path = Path(self.tmp.name) / "stray_axis3.fits"
        hdul.writeto(path)
        v, _ = self.c.ask(op="view", path=str(path))
        self.assertTrue(v["ok"], v)
        self.assertEqual((v["channels"], v["fits_axes"]["channel"]), (1, None))
        self.assertIsNone(v["spectral"])

    def test_a_reversed_spectral_axis_follows_the_header_not_the_channel_index(self):
        path = self.f["hi_cube_reverse_spectral.fits"]
        v, _ = self.c.ask(op="view", path=str(path))
        self.assertTrue(v["spectral"]["resolved"])
        first = self.c.plane(path, 0)[0]["spectral_value"]
        last = self.c.plane(path, g.NC - 1)[0]["spectral_value"]
        self.assertAlmostEqual(first, g.V0_MS + (g.NC - 1) * g.DV_MS, places=6)
        self.assertAlmostEqual(last, g.V0_MS, places=6)
        self.assertGreater(first, last)                                    # velocity falls as the channel number rises

    def test_a_rotated_wcs_is_read_with_its_pc_matrix(self):
        x, y = g.SOURCE_PIX
        r, _ = self.c.ask(op="coord", path=str(self.f["hi_cube_rotated.fits"]), x=x, y=y, channel=0)
        dx, dy = x + 1 - g.CRPIX[0], y + 1 - g.CRPIX[1]                    # 1-based pixel minus reference pixel
        c, s = math.cos(math.radians(g.ROT_DEG)), math.sin(math.radians(g.ROT_DEG))
        xi = math.radians(-g.CDELT_DEG * (c * dx - s * dy))                # independent TAN with the PC matrix applied
        eta = math.radians(g.CDELT_DEG * (s * dx + c * dy))
        a0, d0 = math.radians(g.CRVAL_RA), math.radians(g.CRVAL_DEC)
        den = math.cos(d0) - eta * math.sin(d0)
        ra = math.degrees(a0 + math.atan2(xi, den)) % 360.0
        dec = math.degrees(math.atan2(math.sin(d0) + eta * math.cos(d0), math.hypot(xi, den)))
        self.assertAlmostEqual(r["sky"]["lon"], ra, places=8)
        self.assertAlmostEqual(r["sky"]["lat"], dec, places=8)
        plain, _ = self.c.ask(op="coord", path=str(self.f["hi_cube.fits"]), x=x, y=y, channel=0)
        self.assertGreater(abs(plain["sky"]["lon"] - r["sky"]["lon"]), 1e-3)  # and it differs from the unrotated readout

    def test_no_celestial_wcs_falls_back_to_pixels(self):
        path = self.f["hi_cube_no_celestial_wcs.fits"]
        v, _ = self.c.ask(op="view", path=str(path))
        self.assertEqual(v["sky"]["kind"], "pixel")
        self.assertEqual(v["fits_axes"], {"x": 1, "y": 2, "channel": 3})
        r, _ = self.c.ask(op="coord", path=str(path), x=2, y=2, channel=1)
        self.assertIsNone(r["sky"])

    def test_flags_are_declared_only_when_the_extension_exists(self):
        v, _ = self.c.ask(op="view", path=str(self.f["hi_cube.fits"]))
        self.assertTrue(v["flags"]["declared"])
        self.assertEqual(v["flags"]["channels"], list(g.FLAGGED_CHANNELS))
        h10, p10 = self.c.plane(self.f["hi_cube.fits"], 10)
        self.assertIs(h10["flagged"], True)
        self.assertTrue(np.isfinite(p10).any())                        # flagged data is kept, not blanked
        self.assertIs(self.c.plane(self.f["hi_cube.fits"], 12)[0]["flagged"], False)
        bare = Path(self.tmp.name) / "no_flags.fits"
        fits.HDUList([g.hi_cube()[0]]).writeto(bare)
        vb, _ = self.c.ask(op="view", path=str(bare))
        self.assertFalse(vb["flags"]["declared"])
        self.assertIsNone(self.c.plane(bare, 10)[0]["flagged"])       # absent is not "not flagged"

    def test_bad_requests_fail_in_a_controlled_way_and_the_worker_survives(self):
        p = str(self.f["hi_cube.fits"])
        for req in ({"op": "plane", "path": p, "channel": g.NC}, {"op": "plane", "path": p, "channel": -1},
                    {"op": "plane", "path": p, "channel": True}, {"op": "coord", "path": p, "x": g.NX, "y": 0, "channel": 0},
                    {"op": "plane", "path": "relative.fits", "channel": 0}, {"op": "nope", "path": p}):
            head, _ = self.c.ask(**req)
            self.assertFalse(head["ok"], req)
            self.assertEqual(head["code"], "bad_request", req)
        bad = Path(self.tmp.name) / "junk.fits"
        bad.write_bytes(b"x" * 5000)
        head, _ = self.c.ask(op="view", path=str(bad))
        self.assertEqual(head["code"], "unreadable")
        head, _ = self.c.plane(self.f["hi_cube.fits"], 0)
        self.assertTrue(head["ok"])

    def test_truncated_data_is_an_error_not_a_zero_filled_plane(self):
        full = Path(self.tmp.name) / "full.fits"
        g.hi_cube().writeto(full)
        cut = Path(self.tmp.name) / "cut.fits"
        cut.write_bytes(full.read_bytes()[: 2880 * 2 + 4 * g.NX * g.NY * 3])
        head, plane = self.c.plane(cut, g.NC - 1)
        self.assertFalse(head["ok"])
        self.assertIsNone(plane)

    def test_changed_file_is_re_read(self):
        path = Path(self.tmp.name) / "changing.fits"
        g.hi_cube().writeto(path)
        a = self.c.plane(path, 0)[1].copy()
        data = g.hi_cube()[0].data * 2
        hdul = g.hi_cube()
        hdul[0].data = data
        hdul.writeto(path, overwrite=True)
        b = self.c.plane(path, 0)[1]
        np.testing.assert_array_equal(b, a * 2)

    @unittest.skipUnless(sys.platform.startswith("linux"), "uses /proc and sparse files")
    def test_one_plane_of_a_multi_gigabyte_cube_does_not_load_the_cube(self):
        nx, ny, nc = 2000, 2000, 300                                   # 4.8 GB of float32
        h = fits.PrimaryHDU().header
        h["BITPIX"], h["NAXIS"], h["NAXIS1"], h["NAXIS2"], h["NAXIS3"] = -32, 3, nx, ny, nc
        h["CTYPE3"], h["CRVAL3"], h["CRPIX3"], h["CDELT3"], h["CUNIT3"] = "VRAD", 0.0, 1.0, 1000.0, "m/s"
        path = Path(self.tmp.name) / "big.fits"
        head_bytes = h.tostring(padding=True).encode("ascii")
        with open(path, "wb") as f:
            f.write(head_bytes)
            f.truncate(len(head_bytes) + nx * ny * nc * 4 + 2880)      # sparse: reads back as zeros
        c = Client()
        try:
            head, plane = c.plane(path, 150)
            self.assertTrue(head["ok"])
            self.assertEqual(plane.shape, (ny, nx))
            hwm_kb = int([ln for ln in open(f"/proc/{c.p.pid}/status") if ln.startswith("VmHWM")][0].split()[1])
            self.assertLess(hwm_kb, 800 * 1024, "peak RSS suggests the whole cube was read")
        finally:
            c.close()


class RemoteRoots(unittest.TestCase):
    """A worker started with VESQIRA_ROOTS enforces the roots itself, as it must on a host Go cannot inspect."""

    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        base = Path(cls.tmp.name).resolve()
        cls.root, cls.other = base / "root", base / "other"
        (cls.root / "sub").mkdir(parents=True)
        cls.other.mkdir()
        g.write_all(cls.root)
        g.hi_cube().writeto(cls.other / "secret.fits")
        (cls.root / "link_out.fits").symlink_to(cls.other / "secret.fits")
        (cls.root / "link_in.fits").symlink_to(cls.root / "hi_cube.fits")
        (cls.root / "link_dir").symlink_to(cls.other)
        cls.c = Client(roots=[cls.root])

    @classmethod
    def tearDownClass(cls):
        cls.c.close()
        cls.tmp.cleanup()

    def ask(self, **req):
        return self.c.ask(**req)[0]

    def test_ping_reports_versions_and_that_roots_are_enforced(self):
        r = self.ask(op="ping")
        self.assertTrue(r["ok"] and r["enforces_roots"])
        self.assertEqual(r["roots"], [str(self.root)])
        self.assertTrue(r["astropy"])

    def test_header_stat_and_resolve_work_inside_the_roots(self):
        p = str(self.root / "hi_cube.fits")
        h = self.ask(op="header", path=p)
        self.assertTrue(h["ok"])
        self.assertEqual([a["ctype"] for a in h["axes"]][2], "VRAD")
        st = self.ask(op="stat", path=p)
        self.assertEqual(st["size"], (self.root / "hi_cube.fits").stat().st_size)
        self.assertEqual(self.ask(op="resolve", path=str(self.root / "link_in.fits"))["real"], p)   # a link inside resolves

    def test_register_many_gives_what_the_single_ops_give_and_refuses_files_alone(self):
        bad = self.root / "not_fits.fits"
        bad.write_bytes(b"this is not a FITS file\n" * 200)
        good = [str(self.root / "hi_cube.fits"), str(self.root / "vgt_psi_deg.fits")]
        paths = [good[0], str(self.root / "missing.fits"), str(self.other / "secret.fits"), str(bad), good[1], str(self.root / "link_in.fits")]
        r = self.ask(op="register_many", paths=paths)
        self.assertTrue(r["ok"])
        self.assertEqual(len(r["files"]), len(paths))
        f = r["files"]
        for i in (0, 4, 5):                                              # the same header, size, time and fingerprint as the single ops
            one = self.ask(op="header", path=paths[i])
            for k in ("nbytes", "op", "size", "mtime_ns"):
                one.pop(k)
            self.assertTrue(f[i]["ok"])
            self.assertEqual(f[i]["header"], one)
            self.assertEqual(f[i]["real"], self.ask(op="resolve", path=paths[i])["real"])
            fp = self.ask(op="fingerprint", path=paths[i])
            self.assertEqual((f[i]["sampled"], f[i]["algorithm"], f[i]["size"], f[i]["mtime_ns"]), (fp["sampled"], fp["algorithm"], fp["size"], fp["mtime_ns"]))
            self.assertNotIn("sampled_error", f[i])
        self.assertEqual(f[5]["real"], good[0])                          # a link inside the roots resolves
        single = {p: self.ask(op="header", path=p) for p in (paths[1], paths[2], str(bad))}
        self.assertEqual((f[1]["ok"], f[1]["stage"], f[1]["code"], f[1]["error"]), (False, "resolve", "forbidden", "not available"))
        self.assertEqual((f[2]["code"], f[2]["error"]), (single[paths[2]]["code"], single[paths[2]]["error"]))
        self.assertEqual((f[3]["ok"], f[3]["stage"], f[3]["code"], f[3]["error"]), (False, "header", single[str(bad)]["code"], single[str(bad)]["error"]))

    def test_register_many_bounds_its_input(self):
        for bad in ([], "x", ["/x"] * 33):
            r = self.ask(op="register_many", paths=bad)
            self.assertEqual((r["ok"], r["code"]), (False, "bad_request"))
        r = self.ask(op="register_many", paths=[1, "relative.fits"])    # a path that is not an absolute string is that file's refusal
        self.assertEqual([(f["ok"], f["code"]) for f in r["files"]], [(False, "bad_request")] * 2)

    def test_register_many_gives_no_fingerprint_for_a_file_that_changed_while_it_was_read(self):
        import plane_worker as pw
        p = str(self.root / "hi_cube.fits")
        real_stat, calls = pw.stat_source, []

        def moving(path):
            st = real_stat(path)
            calls.append(1)
            return os.stat_result((st.st_mode, st.st_ino, st.st_dev, st.st_nlink, st.st_uid, st.st_gid, st.st_size + len(calls) % 2, st.st_atime, st.st_mtime, st.st_ctime))

        saved = pw.ROOTS
        pw.ROOTS = [str(self.root)]
        pw.stat_source = moving
        try:
            f = pw.register_many({"paths": [p]})["files"][0]
        finally:
            pw.stat_source, pw.ROOTS = real_stat, saved
        self.assertTrue(f["ok"])
        self.assertNotIn("sampled", f)
        self.assertEqual(f["sampled_error"]["code"], "changed")

    def test_register_many_stops_at_its_time_budget_and_leaves_the_rest_for_the_caller(self):
        import plane_worker as pw
        paths = [str(self.root / n) for n in ("hi_cube.fits", "vgt_psi_deg.fits", "link_in.fits")]
        saved = pw.ROOTS
        pw.ROOTS = [str(self.root)]
        try:
            r = pw.register_many({"paths": paths}, budget=-1.0)           # spent before the second file; the first is always done
            self.assertEqual(len(r["files"]), 1)
            self.assertTrue(r["files"][0]["ok"])
            self.assertEqual(len(pw.register_many({"paths": paths})["files"]), 3)
        finally:
            pw.ROOTS = saved

    def test_raw_header_cards_are_the_files_own_text_and_stay_inside_the_roots(self):
        r = self.ask(op="cards", path=str(self.root / "hi_cube.fits"))
        self.assertTrue(r["ok"] and not r["truncated"])
        self.assertEqual(r["cards"][0][:30], "SIMPLE  =                    T")
        self.assertTrue(all(len(c) <= 80 for c in r["cards"]))
        self.assertTrue(any(c.startswith("CTYPE3") and "VRAD" in c for c in r["cards"]))

    def test_everything_outside_the_roots_is_the_same_refusal(self):
        outside = [str(self.other / "secret.fits"), str(self.root / "link_out.fits"), str(self.root / ".." / "other" / "secret.fits"),
                   str(self.root / "link_dir" / "secret.fits"), "/etc/passwd", str(self.other), "/nonexistent/x.fits"]
        replies = []
        for op in ("header", "stat", "resolve", "view", "list", "cards", "walk", "readtext"):
            for p in outside:
                r = self.ask(op=op, path=p)
                self.assertFalse(r["ok"], (op, p))
                self.assertEqual(r["code"], "forbidden", (op, p))
                replies.append(r["error"])
        self.assertEqual(len(set(replies)), 1)                           # nothing tells existing and missing files apart
        for op in ("plane",):
            self.assertEqual(self.c.ask(op=op, path=str(self.root / "link_out.fits"), channel=0)[0]["code"], "forbidden")
        for p in outside:                                                # one file outside refuses the whole batch, with the same refusal
            r = self.ask(op="stat_many", paths=[str(self.root / "hi_cube.fits"), p])
            self.assertEqual((r["ok"], r["code"], r["error"]), (False, "forbidden", replies[0]))
        ok = self.ask(op="stat_many", paths=[str(self.root / "hi_cube.fits"), str(self.root / "vgt_psi_deg.fits")])
        self.assertEqual([f["size"] for f in ok["files"]], [(self.root / n).stat().st_size for n in ("hi_cube.fits", "vgt_psi_deg.fits")])
        for bad in ([], "x", [1], ["/x"] * 193):
            self.assertEqual(self.ask(op="stat_many", paths=bad)["ok"], False)
        self.assertEqual(self.ask(op="axial", path=str(self.root / "vgt_psi_deg.fits"), unit="deg", weights_path=str(self.other / "secret.fits"))["code"], "forbidden")

    def test_completion_lists_names_only_and_may_leave_the_roots(self):
        # the worker enforces roots for reading; completion only names entries of the one folder asked for
        (self.other / "alpha").mkdir(exist_ok=True)
        (self.other / ".hidden").write_text("x")
        r = self.ask(op="complete", prefix=str(self.other) + "/")
        self.assertTrue(r["ok"])
        self.assertEqual(set(r), {"ok", "op", "nbytes", "base", "entries", "truncated"})
        self.assertEqual([e["name"] for e in r["entries"]], ["alpha", "secret.fits"])      # folders first, dot entry hidden
        self.assertEqual(r["entries"][0], {"name": "alpha", "path": str(self.other / "alpha"), "dir": True})
        self.assertEqual(self.ask(op="complete", prefix=str(self.other) + "/.h")["entries"][0]["name"], ".hidden")

    def test_completion_filters_by_prefix_kind_and_extension(self):
        r = self.ask(op="complete", prefix=str(self.root / "hi_cube.f"))
        self.assertEqual([e["name"] for e in r["entries"]], ["hi_cube.fits"])
        self.assertEqual(self.ask(op="complete", prefix=str(self.root / "HI_CUBE.F"))["entries"][0]["name"], "hi_cube.fits")  # case fallback
        r = self.ask(op="complete", prefix=str(self.root) + "/", kind="dir")
        self.assertTrue(r["entries"] and all(e["dir"] for e in r["entries"]))
        self.assertTrue(any(e["name"] == "link_dir" for e in r["entries"]))                  # a link to a folder is a folder
        r = self.ask(op="complete", prefix=str(self.root) + "/", kind="file", ext=[".FITS"])
        self.assertTrue(any(not e["dir"] and e["name"].endswith(".fits") for e in r["entries"]))
        self.assertTrue(all(e["dir"] or e["name"].endswith(".fits") for e in r["entries"]))  # folders stay for navigation
        self.assertEqual(self.ask(op="complete", prefix=str(self.root / "nothing-here"))["entries"], [])
        self.assertEqual(self.ask(op="complete", prefix="/nonexistent/dir/")["entries"], [])

    def test_completion_is_limited_to_fifty_entries(self):
        many = self.root / "many"
        many.mkdir(exist_ok=True)
        for i in range(60):
            (many / f"f{i:02d}.txt").write_text("")
        r = self.ask(op="complete", prefix=str(many) + "/")
        self.assertEqual(len(r["entries"]), 50)
        self.assertTrue(r["truncated"])
        self.assertEqual(r["entries"][0]["name"], "f00.txt")

    def test_completion_expands_home_and_rejects_bad_prefixes(self):
        r = self.ask(op="complete", prefix="~/")
        self.assertTrue(r["ok"] and r["base"] == os.path.normpath(os.path.expanduser("~")))
        for p in ("relative/dir", "", "a\0b", "x" * 5000, "~other/x", 5):
            self.assertEqual(self.ask(op="complete", prefix=p)["code"], "bad_request", p)
        self.assertEqual(self.ask(op="complete", prefix="/tmp/", kind="weird")["code"], "bad_request")
        self.assertEqual(self.ask(op="complete", prefix="/tmp/", ext=["fits"])["code"], "bad_request")

    def test_relative_and_odd_paths_are_bad_requests(self):
        for p in ("hi_cube.fits", "", "a\0b", "x" * 5000):
            self.assertEqual(self.ask(op="resolve", path=p)["code"], "bad_request")

    def test_listing_shows_only_what_is_inside_the_roots(self):
        r = self.ask(op="list", path=str(self.root))
        names = {e["name"]: e["kind"] for e in r["entries"]}
        self.assertEqual(names["sub"], "dir")
        self.assertEqual(names["hi_cube.fits"], "file")
        self.assertIn("link_in.fits", names)
        self.assertNotIn("link_out.fits", names)                         # links that leave the roots are not even listed
        self.assertNotIn("link_dir", names)

    def test_readtext_reads_small_text_inside_the_roots_only(self):
        t = self.root / "run_metadata.toml"
        t.write_text('gitcommit = "abc"\n')
        r = self.ask(op="readtext", path=str(t))
        self.assertTrue(r["ok"])
        self.assertEqual(r["text"], 'gitcommit = "abc"\n')
        (self.other / "x.toml").write_text("secret = 1\n")
        self.assertEqual(self.ask(op="readtext", path=str(self.other / "x.toml"))["code"], "forbidden")
        big = self.root / "big.txt"
        big.write_bytes(b"x" * (300 * 1024))
        self.assertEqual(self.ask(op="readtext", path=str(big))["code"], "unsupported")

    def test_planes_still_work_inside_the_roots(self):
        head, plane = Client.plane(self.c, self.root / "hi_cube.fits", 3)
        self.assertTrue(head["ok"])
        self.assertEqual(plane.shape, (g.NY, g.NX))


RULES = dict(skip_names=[".git", "node_modules", "__pycache__"], never_files=["*do_not_open*"], never_dirs=["*do_not_open*", "blind*key*"],
             skip_globs=["old/**"], leaf_dir_suffixes=[".zarr"])


class ProjectWalk(unittest.TestCase):
    """The host side of opening a project: the same skip rules as the API's scanner, bounded pages, links never followed."""

    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        base = Path(cls.tmp.name).resolve()
        cls.root, cls.other = base / "proj", base / "other"
        for d in ("data/raw", "node_modules/pkg", ".git/objects", "blind_set_key", "old/x", "cube.zarr/0", "results"):
            (cls.root / d).mkdir(parents=True)
        cls.other.mkdir()
        for f in ("data/raw/a.fits", "node_modules/pkg/x.js", ".git/HEAD", "blind_set_key/key.csv", "old/x/y.fits", "cube.zarr/0/c",
                  "results/run.json", "results/key_DO_NOT_OPEN.csv", "z.txt"):
            (cls.root / f).write_text("x")
        (cls.other / "secret.txt").write_text("s")
        (cls.root / "link_out").symlink_to(cls.other)
        (cls.root / "link_in").symlink_to(cls.root / "z.txt")
        (cls.root / "dangling").symlink_to(cls.root / "nothing")
        cls.c = Client(roots=[cls.root])

    @classmethod
    def tearDownClass(cls):
        cls.c.close()
        cls.tmp.cleanup()

    def ask(self, **req):
        return self.c.ask(**req)[0]

    def full(self, **extra):
        r = self.ask(op="walk", path=str(self.root), **RULES, **extra)
        self.assertTrue(r["ok"], r)
        return r

    def test_rules_are_applied_before_anything_is_entered(self):
        r = self.full()
        by = {e["p"]: e for e in r["entries"]}
        self.assertIsNone(r["next"])
        for p in ("node_modules", ".git", "blind_set_key", "old", "cube.zarr"):
            self.assertTrue(by[p].get("leaf"), p)                         # reported, not entered
        for p in by:
            self.assertFalse(p.startswith(("node_modules/", ".git/", "blind_set_key/", "old/", "cube.zarr/")), p)
        self.assertIn("data/raw/a.fits", by)
        self.assertEqual(by["data/raw/a.fits"]["size"], 1)
        # a file marked as not to be opened is named, and nothing else about it is looked at
        self.assertEqual(by["results/key_DO_NOT_OPEN.csv"], {"p": "results/key_DO_NOT_OPEN.csv", "never": True})

    def test_links_are_classified_and_never_followed(self):
        by = {e["p"]: e for e in self.full()["entries"]}
        self.assertEqual(by["link_out"]["link"], "outside")
        self.assertEqual(by["link_in"]["link"], "inside")
        self.assertEqual(by["dangling"]["link"], "dangling")
        self.assertFalse(any(p.startswith("link_out/") for p in by))

    def test_pages_join_into_the_same_walk(self):
        whole = [e["p"] for e in self.full()["entries"]]
        for size in (1, 2, 3, 5):
            got, after = [], ""
            for _ in range(100):
                r = self.ask(op="walk", path=str(self.root), limit=size, after=after, **RULES)
                self.assertTrue(r["ok"] and len(r["entries"]) <= size)
                got += [e["p"] for e in r["entries"]]
                if r["next"] is None:
                    break
                after = r["next"]
            self.assertEqual(got, whole, size)

    def test_folder_must_be_inside_the_roots_and_requests_are_checked(self):
        self.assertEqual(self.ask(op="walk", path=str(self.other), **RULES)["code"], "forbidden")
        self.assertEqual(self.ask(op="walk", path=str(self.root / "link_out"), **RULES)["code"], "forbidden")
        for bad in (dict(limit=0), dict(limit=10 ** 6), dict(after="../x"), dict(after="/etc"), dict(skip_names="x")):
            self.assertEqual(self.ask(op="walk", path=str(self.root), **{**RULES, **bad})["code"], "bad_request", bad)

    def test_canon_says_where_a_folder_really_is_without_a_roots_check(self):
        r = self.ask(op="canon", path=str(self.root / "link_out"))
        self.assertTrue(r["ok"] and r["is_dir"])
        self.assertEqual(r["real"], str(self.other))
        self.assertTrue(os.path.isabs(r["home"]))
        self.assertFalse(self.ask(op="canon", path=str(self.root / "z.txt"))["is_dir"])
        self.assertEqual(self.ask(op="canon", path="relative")["code"], "bad_request")

    def test_readtext_can_return_just_the_start_of_a_file(self):
        t = self.root / "job.sh"
        t.write_text("#!/bin/bash\n#SBATCH -n 4\n" + "x" * 100)
        r = self.ask(op="readtext", path=str(t), max=20)
        self.assertTrue(r["ok"] and r["truncated"])
        self.assertEqual(len(r["text"]), 20)
        self.assertFalse(self.ask(op="readtext", path=str(t))["truncated"])
        self.assertEqual(self.ask(op="readtext", path=str(t), max=0)["code"], "bad_request")


class RequestIdLogging(unittest.TestCase):
    """The API's correlation id appears in the worker's stderr line for a failing request, and only there."""

    def run_worker(self, *reqs):
        lines = b"".join(json.dumps(r).encode() + b"\n" for r in reqs)
        done = subprocess.run([sys.executable, "-P", SCRIPT], input=lines, capture_output=True, timeout=60)
        heads = [json.loads(x) for x in done.stdout.splitlines()]
        return heads, done.stderr.decode()

    def test_a_failing_request_logs_its_id_and_the_reply_does_not_carry_it(self):
        heads, err = self.run_worker({"op": "plane", "path": "relative", "requestId": "abcd1234ef567890"})
        self.assertFalse(heads[0]["ok"])
        self.assertIn("request=abcd1234ef567890 op=plane refused", err)
        self.assertNotIn("abcd1234ef567890", json.dumps(heads))
        self.assertNotIn("relative", err)

    def test_a_request_without_an_id_still_works_and_logs_nothing(self):
        heads, err = self.run_worker({"op": "plane", "path": "relative"}, {"op": "ping"})
        self.assertEqual(len(heads), 2)
        self.assertEqual(err, "")

    def test_an_id_that_is_not_a_plain_token_is_not_logged(self):
        _, err = self.run_worker({"op": "plane", "path": "relative", "requestId": "bad id\nforged line"})
        self.assertEqual(err, "")
        self.assertNotIn("forged", err)


if __name__ == "__main__":
    unittest.main()
