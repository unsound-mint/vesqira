"""Carrying masks through cutout, slab, subset, align and convolve (ADR 0055). Every expected mask is computed here by index arithmetic on
the source mask read with astropy, never by masks.py's own helpers.

The invariants (AGENTS.md 5 and 8): a carried mask is a new file; zero in the data is never flagged by carrying (carrying reads the mask
only); the result's `missing` mask is computed again from the result's own values and never carried; the sources stay byte-identical."""
import hashlib
import json
import math
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

import numpy as np
from astropy.io import fits

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
from test_masks import cube_header, run  # noqa: E402

NX = NY = 24
NC = 3
nan = float("nan")


def sha(p):
    return hashlib.sha256(Path(p).read_bytes()).hexdigest()


def read(p):
    with fits.open(p) as h:
        return np.array(h[0].data), h[0].header.copy()


class Carry(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        cls.d = Path(cls.tmp.name)
        r = np.random.default_rng(11)
        cube = r.normal(2.0, 1.0, (NC, NY, NX))
        cube[:, 2, 2] = 0.0                      # a real zero in every channel: a value, flagged by nothing
        cube[:, 7, 5] = nan                      # a hole in every channel
        cube[2, 0, 0] = nan
        cube[1, 10, 12] = 99.0                   # saturated, channel 1
        cube[2, 15, 9] = 99.0                    # saturated, channel 2
        h = cube_header(NX, NY, NC)
        h["BMAJ"] = h["BMIN"] = 120.0 / 3600.0   # a 2 arcmin beam on 1 arcmin pixels
        h["BPA"] = 0.0
        cls.f = cls.d / "cube.fits"
        fits.PrimaryHDU(cube.astype("<f4"), header=h).writeto(cls.f)
        cls.cube = cube.astype("<f4").astype(float)
        cls.chan_mask = cls.mk("chan", method="saturated", type="saturated", threshold=90.0, source="datasheet")      # (3, ny, nx)
        cls.box_mask = cls.mk("box", method="regions", type="user_excluded",
                              regions=[{"shape": "rect", "x0": 6, "y0": 12, "x1": 10, "y1": 14}])                       # (ny, nx), every channel
        cls.dot_mask = cls.mk("dot", method="regions", type="user_excluded", regions=[{"shape": "rect", "x0": 10, "y0": 11, "x1": 10, "y1": 11}])
        cls.sources = [cls.f, cls.chan_mask, cls.box_mask, cls.dot_mask]
        cls.before = [sha(p) for p in cls.sources]

    @classmethod
    def tearDownClass(cls):
        assert [sha(p) for p in cls.sources] == cls.before, "a source file was modified"
        cls.tmp.cleanup()

    @classmethod
    def mk(cls, name, **params):
        out = cls.d / f"mask-{name}.fits"
        code, r = run("mask", "--input", cls.f, "--out", out, "--params", json.dumps(params))
        assert code == 0, r
        return out

    def spec(self, path, mtype, name):
        return {"path": str(path), "type": mtype, "out": str(self.d / f"carried-{name}.fits")}

    def carry(self, op, name, specs, *args, missing=True, inp=None):
        out = self.d / f"{name}.fits"
        extra = ["--carry-masks", json.dumps(specs)] if specs else []
        miss = self.d / f"{name}-missing.fits"
        if missing:
            extra += ["--missing-out", str(miss)]
        code, r = run(op, "--input", inp or self.f, "--out", out, *args, *extra)
        return code, r, out, miss

    def test_cutout_carries_the_same_window_of_each_mask_and_nothing_else(self):
        x0, x1, y0, y1 = 1, 13, 1, 17
        specs = [self.spec(self.chan_mask, "saturated", "cut-a"), self.spec(self.box_mask, "user_excluded", "cut-b")]
        code, r, out, _ = self.carry("cutout", "cut", specs, "--x0", x0, "--x1", x1, "--y0", y0, "--y1", y1)
        self.assertEqual(code, 0, r)
        src_a, src_b = read(self.chan_mask)[0].astype(bool), read(self.box_mask)[0].astype(bool)
        got_a, ha = read(specs[0]["out"])
        got_b, _ = read(specs[1]["out"])
        self.assertTrue(np.array_equal(got_a.astype(bool), src_a[:, y0:y1 + 1, x0:x1 + 1]))
        self.assertTrue(np.array_equal(got_b.astype(bool), src_b[y0:y1 + 1, x0:x1 + 1]))
        self.assertEqual(got_a.shape, (NC, y1 - y0 + 1, x1 - x0 + 1))
        self.assertEqual(got_b.shape, (y1 - y0 + 1, x1 - x0 + 1), "a one-plane mask stays one plane")
        self.assertEqual((ha["VESQMTYP"], ha["VESQMTHD"], ha["VESQMSRC"]), ("saturated", "carried", out.name))
        self.assertTrue(any("cutout x 1-13" in str(c) for c in ha["HISTORY"]), "the file states what was done to the data")
        # the data is not read: the zero of every channel at (x 2, y 2) and the hole at (5, 7) are flagged by neither carried mask
        self.assertFalse(got_a[:, 2 - y0, 2 - x0].any() or got_b[2 - y0, 2 - x0])
        self.assertFalse(got_a[:, 7 - y0, 5 - x0].any() or got_b[7 - y0, 5 - x0])
        c = r["summary"]["mask_carry"]
        self.assertEqual([m["type"] for m in c["carried"]], ["saturated", "user_excluded"])
        self.assertEqual(c["carried"][0]["flagged"], int(src_a[:, y0:y1 + 1, x0:x1 + 1].sum()))
        self.assertNotIn("path", json.dumps(c))

    def test_the_result_missing_mask_is_computed_from_its_own_values(self):
        x0, x1, y0, y1 = 1, 13, 1, 17
        code, r, out, miss = self.carry("cutout", "cutm", [], "--x0", x0, "--x1", x1, "--y0", y0, "--y1", y1)
        self.assertEqual(code, 0, r)
        data, _ = read(out)
        m, h = read(miss)
        self.assertTrue(np.array_equal(m.astype(bool), ~np.isfinite(data)))
        self.assertTrue(m[:, 7 - y0, 5 - x0].all(), "the hole is in the window")
        self.assertFalse(m[:, 2 - y0, 2 - x0].any(), "the zero is a value, not missing")
        self.assertEqual((h["VESQMTYP"], h["VESQMTHD"]), ("missing", "computed"))
        self.assertEqual(r["summary"]["mask_carry"]["missing"]["flagged"], int(m.sum()))
        self.assertEqual(r["summary"]["mask_carry"]["carried"], [])

    def test_a_no_value_mask_is_never_carried_and_a_mask_of_another_shape_is_refused(self):
        miss_src = self.mk("miss", method="missing", type="missing")
        code, r, *_ = self.carry("cutout", "bad1", [self.spec(miss_src, "missing", "bad1")], "--x0", 0, "--x1", 5, "--y0", 0, "--y1", 5)
        self.assertEqual(code, 2)
        self.assertIn("never carried", r["error"])
        other = self.d / "small.fits"
        fits.PrimaryHDU(np.zeros((4, 4), "<f4"), header=fits.Header({"CTYPE1": "RA---TAN"})).writeto(other)
        wrong = self.d / "wrong-mask.fits"
        h = fits.Header()
        h["VESQMTYP"] = "saturated"
        fits.PrimaryHDU(np.zeros((5, 5), np.uint8), header=h).writeto(wrong)
        code, r, *_ = self.carry("cutout", "bad2", [self.spec(wrong, "saturated", "bad2")], "--x0", 0, "--x1", 5, "--y0", 0, "--y1", 5)
        self.assertEqual(code, 2)
        self.assertIn("not the data's", r["error"])

    def test_slab_follows_the_channel_range_for_a_per_channel_mask_and_keeps_a_plane_mask_whole(self):
        specs = [self.spec(self.chan_mask, "saturated", "slab-a"), self.spec(self.box_mask, "user_excluded", "slab-b")]
        code, r, *_ = self.carry("slab", "slab", specs, "--chan-start", 1, "--chan-end", 2)
        self.assertEqual(code, 0, r)
        src_a, src_b = read(self.chan_mask)[0].astype(bool), read(self.box_mask)[0].astype(bool)
        got_a, got_b = read(specs[0]["out"])[0].astype(bool), read(specs[1]["out"])[0].astype(bool)
        self.assertTrue(np.array_equal(got_a, src_a[1:3]))
        self.assertEqual(got_a.shape[0], 2)
        self.assertTrue(np.array_equal(got_b, src_b))
        # channel 0 left the window: its saturated pixel is not there, the other two channels' are
        self.assertEqual(int(got_a.sum()), 2)

    def test_a_virtual_subset_materialises_with_the_same_window_cut_and_strided(self):
        window = {"x0": 2, "x1": 20, "y0": 3, "y1": 19, "step": 2, "c0": 1, "c1": 2}
        specs = [self.spec(self.chan_mask, "saturated", "sub-a"), self.spec(self.box_mask, "user_excluded", "sub-b")]
        code, r, *_ = self.carry("subset", "sub", specs, "--window", json.dumps(window))
        self.assertEqual(code, 0, r)
        src_a, src_b = read(self.chan_mask)[0].astype(bool), read(self.box_mask)[0].astype(bool)
        got_a, got_b = read(specs[0]["out"])[0].astype(bool), read(specs[1]["out"])[0].astype(bool)
        ys, xs = list(range(3, 20, 2)), list(range(2, 21, 2))
        self.assertTrue(np.array_equal(got_a, src_a[1:3][:, ys][:, :, xs]))
        self.assertTrue(np.array_equal(got_b, src_b[ys][:, xs]))
        data, _ = read(self.d / "sub.fits")
        self.assertEqual(data.shape[1:], got_a.shape[1:], "the masks and the data share one window")
        self.assertEqual(data.shape[0], got_a.shape[0])

    def test_a_flagged_pixel_convolved_flags_exactly_the_kernel_footprint(self):
        tmaj = tmin = 240.0
        sigma_px = math.sqrt(tmaj ** 2 - 120.0 ** 2) / 60.0 / (2 * math.sqrt(2 * math.log(2)))     # arcsec to pixels (60 arcsec each)
        n = math.ceil(4 * sigma_px)
        self.assertEqual(n, 6)
        specs = [self.spec(self.dot_mask, "user_excluded", "cv-dot"), self.spec(self.chan_mask, "saturated", "cv-chan")]
        code, r, out, _ = self.carry("convolve", "cv", specs, "--target-bmaj", tmaj, "--target-bmin", tmin, "--target-bpa", 0)
        self.assertEqual(code, 0, r)
        self.assertEqual(r["summary"]["kernel_half_width_pixels"], n)
        want = np.zeros((NY, NX), dtype=bool)
        want[11 - n:11 + n + 1, 10 - n:10 + n + 1] = True                       # the dot is at x 10, y 11
        got = read(specs[0]["out"])[0].astype(bool)
        self.assertTrue(np.array_equal(got, want))
        self.assertEqual(int(got.sum()), (2 * n + 1) ** 2)
        per = read(specs[1]["out"])[0].astype(bool)                              # per channel: each channel's own pixel, spread
        self.assertEqual(per.shape, (NC, NY, NX))
        w = np.zeros((NC, NY, NX), dtype=bool)
        w[1, 10 - n:10 + n + 1, 12 - n:12 + n + 1] = True
        w[2, 15 - n:min(NY, 15 + n + 1), 9 - n:9 + n + 1] = True
        self.assertTrue(np.array_equal(per, w))
        self.assertFalse(per[0].any(), "channel 0 had no flagged pixel and gets none")
        # the data's own zero is not a flag: the 4-sigma footprint of a flagged pixel does not reach (2, 2)
        self.assertFalse(got[2, 2] or per[:, 2, 2].any())

    def test_a_beam_already_reached_leaves_the_mask_as_it_was(self):
        code, r, *_ = self.carry("convolve", "cv0", [self.spec(self.dot_mask, "user_excluded", "cv0")], "--target-bmaj", 120, "--target-bmin", 120, "--target-bpa", 0)
        self.assertEqual(code, 0, r)
        self.assertEqual(r["summary"]["kernel_half_width_pixels"], 0)
        self.assertEqual(int(read(self.d / "carried-cv0.fits")[0].sum()), 1)

    def reference(self, name, shift):
        """A 2D reference image on the cube's celestial grid with its reference pixel moved: output pixel i is source pixel i + shift."""
        h = cube_header(NX, NY, NC)
        for k in ("CTYPE3", "CRVAL3", "CRPIX3", "CDELT3", "CUNIT3"):
            del h[k]
        h["CRPIX1"] = float(h["CRPIX1"]) - shift
        p = self.d / f"{name}.fits"
        fits.PrimaryHDU(np.ones((NY, NX), "<f4"), header=h).writeto(p)
        return p

    def align(self, name, shift, method="interp"):
        ref = self.reference(f"ref-{name}", shift)
        specs = [self.spec(self.chan_mask, "saturated", f"{name}-a"), self.spec(self.box_mask, "user_excluded", f"{name}-b")]
        code, r, out, miss = self.carry("align", name, specs, "--reference", ref, "--method", method)
        self.assertEqual(code, 0, r)
        return specs, r, out, miss

    def test_reprojecting_masks_under_a_known_integer_shift_moves_them_exactly(self):
        specs, r, out, _ = self.align("al3", 3)
        src_a, src_b = read(self.chan_mask)[0].astype(bool), read(self.box_mask)[0].astype(bool)
        want_a = np.zeros_like(src_a)
        want_a[:, :, :NX - 3] = src_a[:, :, 3:]                  # output x = source x - 3; the last 3 columns are outside the footprint
        want_b = np.zeros_like(src_b)
        want_b[:, :NX - 3] = src_b[:, 3:]
        self.assertTrue(np.array_equal(read(specs[0]["out"])[0].astype(bool), want_a))
        self.assertTrue(np.array_equal(read(specs[1]["out"])[0].astype(bool), want_b))
        self.assertIn("exact", r["summary"]["mask_carry"]["rule"])

    def test_a_half_pixel_shift_flags_every_output_pixel_a_flagged_pixel_touches(self):
        specs, r, out, _ = self.align("al25", 2.5, method="nearest")      # the data is nearest-neighbour; the mask is conservative whatever the method
        got = read(specs[0]["out"])[0].astype(bool)
        want = np.zeros((NC, NY, NX), dtype=bool)
        want[1, 10, 9:11] = True                    # source pixel x 12 spans [11.5, 12.5]; output pixels 9 and 10 span [11, 12] and [12, 13]
        want[2, 15, 6:8] = True                     # source pixel x 9: output pixels 6 and 7
        self.assertTrue(np.array_equal(got, want))

    def test_the_aligned_result_gets_a_missing_mask_computed_from_its_values(self):
        specs, r, out, miss = self.align("alm", 3)
        data, _ = read(out)
        m = read(miss)[0].astype(bool)
        self.assertTrue(np.array_equal(m, ~np.isfinite(data)))
        src_nan = np.isnan(self.cube)
        carried_style = np.zeros_like(src_nan)
        carried_style[:, :, :NX - 3] = src_nan[:, :, 3:]
        self.assertGreater(int(m.sum()), int(carried_style.sum()), "outside the footprint is also no value; interpolation spreads the holes")
        self.assertTrue((~np.isfinite(data[:, :, NX - 3:])).all())

    def test_an_orientation_map_declared_axial_carries_masks_through_align_and_convolve_too(self):
        h = cube_header(NX, NY, NC)
        for k in ("CTYPE3", "CRVAL3", "CRPIX3", "CDELT3", "CUNIT3"):
            del h[k]
        h["BMAJ"] = h["BMIN"] = 120.0 / 3600.0
        h["BPA"] = 0.0
        theta = np.full((NY, NX), 30.0)
        theta[2, 2] = 0.0                                      # an angle of zero is a value, not a flag
        psi = self.d / "psi.fits"
        fits.PrimaryHDU(theta.astype("<f4"), header=h).writeto(psi)
        dot = self.d / "mask-psi-dot.fits"
        code, r = run("mask", "--input", psi, "--out", dot, "--params", json.dumps({"method": "regions", "type": "user_excluded", "regions": [{"shape": "rect", "x0": 10, "y0": 11, "x1": 10, "y1": 11}]}))
        self.assertEqual(code, 0, r)
        axial = ["--axial-unit", "deg", "--axial-frame", "sky"]
        spec = {"path": str(dot), "type": "user_excluded", "out": str(self.d / "carried-psi-cv.fits")}
        code, r = run("convolve", "--input", psi, "--out", self.d / "psi-cv.fits", "--rel-out", self.d / "psi-cv-rel.fits", "--target-bmaj", 240, "--target-bmin", 240, "--target-bpa", 0,
                      *axial, "--carry-masks", json.dumps([spec]))
        self.assertEqual(code, 0, r)
        got = read(spec["out"])[0].astype(bool)
        want = np.zeros((NY, NX), dtype=bool)
        want[5:18, 4:17] = True                                 # (10, 11) grown by the half-width, 6
        self.assertTrue(np.array_equal(got, want))
        ref = self.reference("ref-psi", 3)
        spec = {"path": str(dot), "type": "user_excluded", "out": str(self.d / "carried-psi-al.fits")}
        code, r = run("align", "--input", psi, "--reference", ref, "--out", self.d / "psi-al.fits", "--rel-out", self.d / "psi-al-rel.fits", *axial, "--carry-masks", json.dumps([spec]))
        self.assertEqual(code, 0, r)
        got = read(spec["out"])[0].astype(bool)
        want = np.zeros((NY, NX), dtype=bool)
        want[11, 7] = True                                      # source x 10 is output x 7 under a shift of 3
        self.assertTrue(np.array_equal(got, want))

    def test_the_sources_are_untouched_after_every_operation(self):
        # tearDownClass compares the SHA-256 of the cube and of the source masks with what they were before any test ran
        for p, h in zip(self.sources, self.before):
            self.assertEqual(sha(p), h)


if __name__ == "__main__":
    unittest.main()
