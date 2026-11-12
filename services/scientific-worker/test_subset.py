"""Virtual subsets (ADR 0063). Three oracles, none of them the code under test:
- the source file's data sliced with numpy at (x0 + i*step, y0 + j*step, c0 + k);
- astropy's WCS of the *source* header at those source pixels (the subset's coordinates must be the source's, not a re-derivation);
- a materialised file of the same window, read as an ordinary file by a plain Cube.
The fixtures include the PC-rotated cube and a CD-matrix variant, where scaling CDELT alone for a step would be wrong."""
import json
import os
import subprocess
import sys
import tempfile
import unittest
import warnings
from pathlib import Path

import numpy as np
from astropy.io import fits
from astropy.wcs import WCS

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parent.parent / "fixtures"))
import generate as g  # noqa: E402
import analysis  # noqa: E402
import plane_worker as pw  # noqa: E402
import subset as sub  # noqa: E402
from test_plane_worker import Client  # noqa: E402

SCRIPT = str(HERE / "analysis.py")
BOX = {"x0": 3, "x1": 20, "y0": 2, "y1": 15, "step": 2, "c0": 10, "c1": 40}


def materialise(path, window, out, *extra):
    p = subprocess.run([sys.executable, "-P", SCRIPT, "subset", "--input", str(path), "--window", json.dumps(window), "--out", str(out), *map(str, extra)],
                       capture_output=True, text=True, timeout=120)
    return p.returncode, json.loads(p.stdout)


def world(header, pix):
    with warnings.catch_warnings():
        warnings.simplefilter("ignore")
        return WCS(header).all_pix2world([pix], 0)[0]


class Subset(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        cls.dir = Path(cls.tmp.name)
        cls.f = g.write_all(cls.dir / "in")
        cd = g.cube_rotated_wcs()                                    # the same rotation, as a CD matrix (no PC, no CDELT)
        h = cd[0].header
        for i in (1, 2, 3):
            for j in (1, 2, 3):
                pc = h.get(f"PC{i}_{j}", 1.0 if i == j else 0.0)
                if pc != 0.0:
                    h[f"CD{i}_{j}"] = h[f"CDELT{i}"] * pc
        for k in [k for k in list(h.keys()) if k.startswith("PC") or k in ("CDELT1", "CDELT2", "CDELT3")]:
            del h[k]
        cls.f["cd_cube.fits"] = cls.dir / "in" / "cd_cube.fits"
        cd.writeto(cls.f["cd_cube.fits"], overwrite=True)
        cls.client = Client()

    @classmethod
    def tearDownClass(cls):
        cls.client.close()
        cls.tmp.cleanup()

    def source(self, name):
        with fits.open(self.f[name]) as h:
            return h[0].data.astype(np.float64), h[0].header.copy()

    # ------------------------------------------------------------------------------------------------------------------
    def check_equal_to_materialised(self, name, window):
        path = self.f[name]
        data, header = self.source(name)
        out = self.dir / f"sub_{name}_{abs(hash(json.dumps(window, sort_keys=True)))}.fits"
        code, r = materialise(path, window, out)
        self.assertEqual(code, 0, r)
        virtual = pw.Cube(str(path), None, window)
        real = pw.Cube(str(out))
        self.assertEqual(virtual.shape, real.shape)
        self.assertEqual(virtual.channels, real.channels)
        rep = virtual.win_report()
        x0, y0, c0, step = rep["x0"], rep["y0"], rep["c0"], rep["step"]
        nx, ny, nc = virtual.shape[virtual.x], virtual.shape[virtual.y], virtual.channels
        # planes: virtual, materialised and numpy slicing of the source agree to the bit (NaN where the source has NaN)
        for k in (0, nc // 2, nc - 1):
            want = data[c0 + k, y0:y0 + ny * step:step, x0:x0 + nx * step:step].astype("<f4")
            v, _ = virtual.read_plane(k)
            m, _ = real.read_plane(k)
            np.testing.assert_array_equal(v, want)
            np.testing.assert_array_equal(m, want)
        # coordinates: the subset's pixel (i, j, k) is the source's (x0 + i*step, y0 + j*step, c0 + k), by astropy on the source header
        for i, j, k in ((0, 0, 0), (nx - 1, 0, 1), (2, ny - 1, nc - 1), (nx // 2, ny // 2, nc // 2)):
            want = world(header, [x0 + i * step, y0 + j * step, c0 + k])
            for cube in (virtual, real):
                got = cube.coord(i, j, k)
                np.testing.assert_allclose([got["sky"]["lon"], got["sky"]["lat"], got["spectral"]["value"]], want, rtol=0, atol=1e-9 * max(1.0, abs(want[2])))
        # the header's WCS says the same at every corner (astropy on the materialised file against the source)
        out_h = fits.getheader(out)
        for i, j, k in ((0, 0, 0), (nx - 1, ny - 1, nc - 1)):
            np.testing.assert_allclose(world(out_h, [i, j, k]), world(header, [x0 + i * step, y0 + j * step, c0 + k]), rtol=0, atol=1e-9 * 1e5)
        # spectrum over a region of the subset, through the real protocol, against the materialised file and against numpy on the source
        region = {"shape": "rect", "x0": 1, "y0": 1, "x1": min(4, nx - 1), "y1": min(3, ny - 1)}
        hv, pv = self.client.ask(op="spectrum", path=str(path), window=window, **region)
        hm, pm = self.client.ask(op="spectrum", path=str(out), **region)
        self.assertTrue(hv["ok"] and hm["ok"], (hv, hm))
        self.assertEqual(pv, pm)                                      # axis, mean and count, byte for byte
        axis = np.frombuffer(pv[:nc * 8], "<f8")
        mean = np.frombuffer(pv[nc * 8:nc * 16], "<f8")
        block = data[c0:c0 + nc, y0 + step:y0 + (region["y1"] + 1) * step:step, x0 + step:x0 + (region["x1"] + 1) * step:step]
        np.testing.assert_allclose(mean, np.nanmean(block.reshape(nc, -1), axis=1), rtol=1e-6)
        with warnings.catch_warnings():
            warnings.simplefilter("ignore")
            pix = np.zeros((nc, 3))
            pix[:, 2] = c0 + np.arange(nc)
            np.testing.assert_allclose(axis, WCS(header).all_pix2world(pix, 0)[:, 2], rtol=1e-12)
        self.assertEqual(hv["window"]["x0"], x0)
        self.assertEqual(hv["region"], hm["region"])
        # the file says what it is
        self.assertEqual(out_h["BUNIT"], header["BUNIT"])
        self.assertEqual(out_h["VESQOP"], "subset")
        self.assertEqual(out_h["NAXIS1"], nx)
        return out

    def test_box_channels_and_step_equal_a_materialised_cutout_on_every_wcs_fixture(self):
        for name in ("hi_cube.fits", "hi_cube_rotated.fits", "cd_cube.fits", "hi_cube_reverse_spectral.fits", "hi_cube_freq.fits"):
            with self.subTest(name):
                self.check_equal_to_materialised(name, {**BOX, "c0": 1, "c1": 6} if name == "hi_cube_freq.fits" else BOX)   # the frequency cube has 8 channels

    def test_without_a_step_or_a_box_the_subset_is_the_source(self):
        for name, window in (("hi_cube_rotated.fits", {"c0": 5, "c1": 9}), ("cd_cube.fits", {"x0": 0, "x1": 31, "y0": 0, "y1": 23}), ("hi_cube.fits", {})):
            with self.subTest(name, window=window):
                self.check_equal_to_materialised(name, window)

    def test_a_step_that_does_not_divide_the_box_keeps_the_pixels_that_fit_and_says_so(self):
        out = self.check_equal_to_materialised("hi_cube_rotated.fits", {"x0": 1, "x1": 10, "y0": 0, "y1": 9, "step": 4})
        self.assertEqual(fits.getheader(out)["NAXIS1"], 3)           # source pixels 1, 5, 9
        rep = pw.Cube(str(self.f["hi_cube_rotated.fits"]), None, {"x0": 1, "x1": 10, "y0": 0, "y1": 9, "step": 4}).win_report()
        self.assertEqual((rep["x1"], rep["width"]), (9, 3))

    def test_a_rotated_matrix_is_scaled_by_columns_not_by_cdelt_alone(self):
        _, header = self.source("hi_cube_rotated.fits")
        c = pw.Cube(str(self.f["hi_cube_rotated.fits"]), None, {"step": 3})
        self.assertEqual(c.h["CDELT1"], header["CDELT1"])            # CDELT is left alone when a PC matrix carries the step
        self.assertAlmostEqual(c.h["PC1_1"], header["PC1_1"] * 3)
        self.assertAlmostEqual(c.h["PC2_1"], header["PC2_1"] * 3)
        self.assertAlmostEqual(c.h["PC1_3"] if "PC1_3" in c.h else 0.0, 0.0)
        self.assertAlmostEqual(c.h["PC3_3"] if "PC3_3" in c.h else 1.0, 1.0)     # the channel axis is not stepped
        plain = pw.Cube(str(self.f["hi_cube.fits"]), None, {"step": 3})
        self.assertAlmostEqual(plain.h["CDELT1"], self.source("hi_cube.fits")[1]["CDELT1"] * 3)   # no matrix: CDELT scales

    def test_the_reference_pixel_moves_with_the_window(self):
        _, header = self.source("hi_cube.fits")
        c = pw.Cube(str(self.f["hi_cube.fits"]), None, {"x0": 4, "x1": 28, "y0": 3, "y1": 20, "c0": 7, "c1": 30})
        self.assertEqual(c.h["CRPIX1"], header["CRPIX1"] - 4)
        self.assertEqual(c.h["CRPIX2"], header["CRPIX2"] - 3)
        self.assertEqual(c.h["CRPIX3"], header["CRPIX3"] - 7)
        c2 = pw.Cube(str(self.f["hi_cube.fits"]), None, {"x0": 4, "x1": 28, "y0": 0, "y1": 23, "step": 2})
        self.assertEqual(c2.h["CRPIX1"], (header["CRPIX1"] - 1 - 4) / 2 + 1)

    # ------------------------------------------------------------------------------------------------------------------
    def test_a_world_interval_resolves_to_the_channels_astropy_says(self):
        for name, lo, hi, unit in (("hi_cube.fits", -20.0, -5.0, "km/s"), ("hi_cube.fits", -19500.0, -4500.0, "m/s"),
                                   ("hi_cube_reverse_spectral.fits", 10.2, 20.7, "km/s"), ("hi_cube_freq.fits", None, None, "MHz")):
            with self.subTest(name=name, unit=unit):
                data, header = self.source(name)
                with warnings.catch_warnings():
                    warnings.simplefilter("ignore")
                    pix = np.zeros((data.shape[0], 3))
                    pix[:, 2] = np.arange(data.shape[0])
                    centres = WCS(header).all_pix2world(pix, 0)[:, 2]            # the axis unit of the file: m/s or Hz
                if lo is None:
                    scale = {"MHz": 1e-6}[unit]
                    lo, hi = float(centres[2] * scale + 0.25 * abs(centres[1] - centres[0]) * scale), float(centres[5] * scale)
                to_axis = {"km/s": 1e3, "m/s": 1.0, "MHz": 1e6}[unit]
                a, b = sorted((lo * to_axis, hi * to_axis))
                want = np.flatnonzero((centres >= a) & (centres <= b))
                c = pw.Cube(str(self.f[name]), None, {"world": {"lo": lo, "hi": hi, "unit": unit, "frame": "LSRK"}})
                rep = c.win_report()
                self.assertEqual((rep["c0"], rep["c1"]), (int(want[0]), int(want[-1])))
                self.assertEqual(c.channels, want.size)
                self.assertIn("closed interval", rep["channel_rule"])
                self.assertEqual(rep["world"]["frame_declared"], "LSRK")
                # and the plane read through it is the source's plane at that channel
                v, _ = c.read_plane(0)
                np.testing.assert_array_equal(v, data[int(want[0])].astype("<f4"))

    def test_a_bound_written_at_a_channel_centre_includes_that_channel(self):
        c = pw.Cube(str(self.f["hi_cube.fits"]), None, {"world": {"lo": -20.0, "hi": -10.0, "unit": "km/s"}})   # centres are whole km/s
        rep = c.win_report()
        self.assertEqual((rep["c0"], rep["c1"]), (12, 22))

    def test_a_world_interval_is_refused_when_it_would_need_an_assumption(self):
        cases = [
            ("hi_cube.fits", {"lo": -20, "hi": -5, "unit": "km/s", "frame": "BARY"}, "declares LSRK"),                 # another frame: never converted
            ("hi_cube.fits", {"lo": -20, "hi": -5, "unit": "Hz"}, "cannot be converted"),                              # frequency for a velocity axis
            ("hi_cube.fits", {"lo": 500, "hi": 600, "unit": "km/s"}, "no channel centre"),
            ("hi_cube.fits", {"lo": -20, "hi": -5}, "needs its unit"),
            ("hi_cube_no_spectral_ref.fits", {"lo": -20, "hi": -5, "unit": "km/s"}, "not fully declared"),
        ]
        for name, world_, text in cases:
            with self.subTest(name=name, world=world_):
                with self.assertRaises(pw.Refused) as ctx:
                    pw.Cube(str(self.f[name]), None, {"world": world_})
                if text:
                    self.assertIn(text, ctx.exception.message)

    def test_a_window_outside_the_source_or_malformed_is_refused(self):
        for window, text in (({"x0": 0, "x1": 99, "y0": 0, "y1": 3}, "not inside"), ({"x0": 0, "x1": 3}, "together"), ({"c0": 0}, "together"),
                             ({"c0": 5, "c1": 99}, "not inside"), ({"step": 0}, "step"), ({"step": 65}, "step"), ({"x0": True, "x1": 1, "y0": 0, "y1": 1}, "whole number"),
                             ({"c0": 1, "c1": 2, "world": {"lo": 1, "hi": 2, "unit": "km/s"}}, "not both"), ({"nope": 1}, "names something")):
            with self.subTest(window=window):
                with self.assertRaises(pw.Refused) as ctx:
                    pw.Cube(str(self.f["hi_cube.fits"]), None, window)
                self.assertIn(text, ctx.exception.message)
        with self.assertRaises(pw.Refused):
            pw.Cube(str(self.f["vgt_psi_deg.fits"]), None, {"c0": 0, "c1": 0})      # an image has no channel axis

    # ------------------------------------------------------------------------------------------------------------------
    def test_only_the_window_is_read(self):
        """The slices asked of the source file stay inside the window: nothing is read to be cropped afterwards."""
        log = []
        real = pw.fits_array

        class Spy:
            def __init__(self, arr):
                self.arr = arr

            def __getitem__(self, idx):
                log.append(idx)
                return self.arr[idx]

        pw.fits_array = analysis.fits_array = lambda hdul: Spy(real(hdul))
        try:
            window = {"x0": 4, "x1": 20, "y0": 3, "y1": 12, "step": 2, "c0": 10, "c1": 20}
            c = pw.Cube(str(self.f["hi_cube.fits"]), None, window)
            c.read_plane(2)
            c.spectrum({"shape": "rect", "x0": 0, "y0": 0, "x1": 2, "y1": 2})
            list(analysis.planes(c, 0, c.channels - 1))
        finally:
            pw.fits_array = analysis.fits_array = real
        self.assertGreaterEqual(len(log), 3)
        for idx in log:                                              # numpy order: (channel, y, x)
            ch, ys, xs = idx
            lo_c, hi_c = (ch.start, ch.stop - 1) if isinstance(ch, slice) else (ch, ch)
            self.assertTrue(10 <= lo_c and hi_c <= 20, idx)
            for sl, first, last in ((ys, 3, 12), (xs, 4, 20)):
                self.assertTrue(first <= sl.start and sl.stop - 1 <= last and sl.step == 2, idx)
        full = sum(1 for idx in log if idx[1] == slice(None) and idx[2] == slice(None))
        self.assertEqual(full, 0)                                    # never the whole plane

    def test_a_big_source_plane_can_still_be_windowed(self):
        big = self.dir / "big.fits"
        hdu = fits.PrimaryHDU(np.zeros((1, 5000, 5000), dtype=np.float32))     # 25 Mpixel plane: over the viewer's limit as a whole
        hdu.header["CTYPE1"], hdu.header["CTYPE2"], hdu.header["CTYPE3"] = "RA---TAN", "DEC--TAN", "VRAD"
        for i, (v, d) in enumerate(((10.0, -0.001), (5.0, 0.001), (0.0, 1000.0)), start=1):
            hdu.header[f"CRVAL{i}"], hdu.header[f"CDELT{i}"], hdu.header[f"CRPIX{i}"] = v, d, 1.0
        hdu.header["CUNIT1"] = hdu.header["CUNIT2"] = "deg"
        hdu.header["CUNIT3"] = "m/s"
        hdu.writeto(big)
        with self.assertRaises(pw.Refused):
            pw.Cube(str(big))
        c = pw.Cube(str(big), None, {"x0": 100, "x1": 611, "y0": 100, "y1": 611})
        self.assertEqual((c.shape[0], c.shape[1]), (512, 512))
        self.assertEqual(c.read_plane(0)[0].shape, (512, 512))

    def test_flags_follow_the_channel_range(self):
        c = pw.Cube(str(self.f["hi_cube.fits"]), None, {"c0": 9, "c1": 12})
        self.assertEqual(c.flags()["channels"], [1, 2])             # source channels 10 and 11 are channels 1 and 2 of the subset

    def test_a_subset_of_an_image_has_no_channel_and_is_written_as_a_2d_file(self):
        out = self.dir / "vgt_sub.fits"
        code, r = materialise(self.f["vgt_psi_deg.fits"], {"x0": 0, "x1": 3, "y0": 1, "y1": 3, "step": 2}, out)
        self.assertEqual(code, 0, r)
        h = fits.getheader(out)
        self.assertEqual(h["NAXIS"], 2)
        data = fits.getdata(self.f["vgt_psi_deg.fits"]).astype(np.float32)
        np.testing.assert_array_equal(fits.getdata(out), data[1:4:2, 0:4:2])

    # ------------------------------------------------------------------------------------------------------------------
    def test_requests_carry_the_window_and_the_reply_says_it_applied_it(self):
        window = {"x0": 3, "x1": 12, "y0": 2, "y1": 9, "c0": 10, "c1": 20}
        head, _ = self.client.ask(op="view", path=str(self.f["hi_cube.fits"]), window=window)
        self.assertEqual((head["width"], head["height"], head["channels"]), (10, 8, 11))
        self.assertEqual((head["window"]["x0"], head["window"]["c0"], head["window"]["source"]["width"]), (3, 10, g.NX))
        head, payload = self.client.ask(op="plane", path=str(self.f["hi_cube.fits"]), window=window, channel=0)
        self.assertEqual((head["width"], head["height"], head["nbytes"]), (10, 8, 10 * 8 * 4))
        self.assertEqual(head["window"]["c0"], 10)
        head, _ = self.client.ask(op="cards", path=str(self.f["hi_cube.fits"]), window=window)
        self.assertTrue(any(card.startswith("NAXIS1  =") and card.split("=")[1].split()[0] == "10" for card in head["cards"]))
        self.assertTrue(any("virtual subset" in card for card in head["cards"]))
        for op, extra in (("slab", {"chan_start": 0, "chan_end": 2}), ("channel_grid", {}), ("sky2pix", {"points": []}), ("convert", {})):
            head, _ = self.client.ask(op=op, path=str(self.f["hi_cube.fits"]), window=window, **extra)
            self.assertFalse(head["ok"], op)                          # never answered for the source's pixels

    def test_materialising_carries_the_corrections_and_the_unit(self):
        path = self.f["hi_cube.fits"]
        state = {"hash": "abcdef0123456789", "bunit": None, "wcs": {"CRVAL3": -30000.0}}   # the spectral reference moved by 2 km/s
        out = self.dir / "corrected_sub.fits"
        window = {"c0": 4, "c1": 9, "x0": 0, "x1": 9, "y0": 0, "y1": 9}
        code, r = materialise(path, window, out, "--corrections", json.dumps({str(path): state}), "--note", "subset 7 version 2 of a source")
        self.assertEqual(code, 0, r)
        self.assertEqual(r["corrections_applied"], [state["hash"]])
        h = fits.getheader(out)
        self.assertEqual(h["VESQWCSC"], "corrected")
        history = " ".join(str(x) for x in h["HISTORY"])
        self.assertIn("CRVAL3", history)
        self.assertIn("subset 7 version 2", history)
        self.assertEqual(h["BUNIT"], "K")
        self.assertEqual(h["CRVAL3"], -30000.0)
        self.assertEqual(h["CRPIX3"], 1.0 - 4)
        # the virtual view of the same corrected source reads the same spectral values as the file
        virtual = pw.Cube(str(path), state, window)
        self.assertEqual(virtual.coord(0, 0, 0)["spectral"]["value"], pw.Cube(str(out)).coord(0, 0, 0)["spectral"]["value"])
        self.assertEqual(virtual.coord(0, 0, 0)["spectral"]["value"], -26000.0)       # source channel 4 under the corrected CRVAL3

    def test_the_input_is_never_modified(self):
        path = self.f["hi_cube_rotated.fits"]
        before = path.read_bytes()
        self.check_equal_to_materialised("hi_cube_rotated.fits", BOX)
        self.assertEqual(path.read_bytes(), before)


if __name__ == "__main__":
    unittest.main()
