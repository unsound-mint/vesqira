"""Typed masks (ADR 0055): every expected number is computed here with plain numpy, never by masks.py's own helpers.

The heart is AGENTS.md invariant 5: a pixel with no value (NaN) is `missing`, a value of zero is a value, and no mask flags either
by accident. Masks are derived files; the data file is byte-identical afterwards."""
import hashlib
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

import numpy as np
from astropy.io import fits

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import relorient as ro  # noqa: E402
from test_relorient import args as ro_args, field, header, write_map  # noqa: E402

SCRIPT = str(HERE / "analysis.py")
nan = float("nan")


def run(*a):
    p = subprocess.run([sys.executable, "-P", SCRIPT, *map(str, a)], capture_output=True, text=True, timeout=120)
    return p.returncode, json.loads(p.stdout)


def sha(p):
    return hashlib.sha256(Path(p).read_bytes()).hexdigest()


def celestial(nx, ny):
    h = fits.Header()
    h["CTYPE1"], h["CTYPE2"] = "RA---TAN", "DEC--TAN"
    h["CRVAL1"], h["CRVAL2"] = 80.0, -69.0
    h["CRPIX1"], h["CRPIX2"] = nx // 2 + 1, ny // 2 + 1
    h["CDELT1"], h["CDELT2"] = -1 / 60.0, 1 / 60.0
    h["CUNIT1"] = h["CUNIT2"] = "deg"
    h["BUNIT"] = "K"
    return h


def cube_header(nx, ny, nc):
    h = celestial(nx, ny)
    h["CTYPE3"], h["CRVAL3"], h["CRPIX3"], h["CDELT3"], h["CUNIT3"] = "VRAD", -10000.0, 1.0, 2000.0, "m/s"
    return h


class Masks(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        cls.d = Path(cls.tmp.name)
        # 6 channels of 8 x 10, with real zeros and real NaNs, a bright saturated pixel and a noise map
        r = np.random.default_rng(5)
        cube = r.normal(2.0, 1.0, (6, 10, 8))
        cube[:, 2, 2] = 0.0                  # zeros: values, not holes
        cube[1, 3, 3] = 0.0
        cube[:, 7, 5] = nan                  # holes in every channel
        cube[2, 0, 0] = nan
        cube[3, 4, 4] = 50.0                 # saturated
        cls.cube = cube
        cls.f = cls.d / "cube.fits"
        fits.PrimaryHDU(cube.astype("<f4"), header=cube_header(8, 10, 6)).writeto(cls.f)
        cls.cube32 = cube.astype("<f4").astype(float)
        sig = r.uniform(0.5, 2.0, (10, 8))
        sig[0, 1] = 0.0                      # no usable sigma here
        sig[1, 1] = nan
        cls.sig = sig
        cls.sigf = cls.d / "sigma.fits"
        fits.PrimaryHDU(sig.astype("<f4"), header=celestial(8, 10)).writeto(cls.sigf)
        cls.sig32 = sig.astype("<f4").astype(float)
        cls.before = sha(cls.f)

    @classmethod
    def tearDownClass(cls):
        assert sha(cls.f) == cls.before, "the data file was modified"
        cls.tmp.cleanup()

    def make(self, name, **params):
        out = self.d / f"{name}.fits"
        code, r = run("mask", "--input", self.f, "--out", out, "--params", json.dumps(params))
        return code, r, out

    def flags(self, out):
        with fits.open(out) as h:
            return h[0].data.astype(bool), h[0].header

    # ---- zero is not missing -------------------------------------------------------------------------------------------
    def test_missing_flags_the_nan_pixels_and_never_a_zero(self):
        code, r, out = self.make("missing", method="missing", type="missing")
        self.assertEqual(code, 0, r)
        m, h = self.flags(out)
        self.assertEqual(m.shape, self.cube.shape)
        self.assertTrue(np.array_equal(m, np.isnan(self.cube)))
        self.assertFalse(m[:, 2, 2].any(), "a zero is a value")
        self.assertFalse(m[1, 3, 3])
        self.assertEqual(r["summary"]["flagged"], int(np.isnan(self.cube).sum()))
        self.assertEqual((h["VESQMTYP"], h["VESQMTHD"]), ("missing", "computed"))

    def test_the_type_missing_cannot_be_declared_and_other_types_cannot_be_computed_as_missing(self):
        code, r, _ = self.make("x1", method="saturated", type="missing", threshold=1.0, source="s")
        self.assertEqual(code, 2)
        code, r, _ = self.make("x2", method="missing", type="low_snr")
        self.assertEqual(code, 2)

    def test_no_threshold_mask_flags_a_nan_and_saturated_never_flags_zero(self):
        _, _, out = self.make("sat0", method="saturated", type="saturated", threshold=0.0, source="a test value")
        m, _ = self.flags(out)
        # threshold 0: value >= 0 flags every value that is >= 0, zeros included BY THE RULE, but never a NaN
        want = np.isfinite(self.cube32) & (self.cube32 >= 0.0)
        self.assertTrue(np.array_equal(m, want))
        self.assertFalse(m[:, 7, 5].any())
        _, _, out2 = self.make("sat2", method="saturated", type="saturated", threshold=10.0, source="a test value")
        m2, _ = self.flags(out2)
        self.assertFalse(m2[:, 2, 2].any())
        self.assertTrue(np.array_equal(m2, np.isfinite(self.cube32) & (self.cube32 >= 10.0)))

    # ---- low SNR ---------------------------------------------------------------------------------------------------------
    def test_low_snr_from_a_noise_map_matches_a_direct_computation(self):
        k = 1.5
        code, r, out = self.make("snr", method="low_snr", type="low_snr", k=k, sigma={"kind": "map", "path": str(self.sigf)})
        self.assertEqual(code, 0, r)
        m, _ = self.flags(out)
        sg = np.broadcast_to(self.sig32, self.cube.shape)
        with np.errstate(all="ignore"):
            usable = np.isfinite(self.cube32) & np.isfinite(sg) & (sg > 0)
            want = usable & (self.cube32 / sg < k)
        self.assertTrue(np.array_equal(m, want))
        self.assertFalse(m[:, 7, 5].any(), "a pixel with no value is `missing`, not low SNR")
        self.assertTrue(m[1, 3, 3], "a measured zero over a known sigma is below the threshold by the stated rule")
        self.assertFalse(m[:, 0, 1].any(), "sigma zero: no ratio exists, so not flagged")
        self.assertFalse(m[:, 1, 1].any(), "sigma unknown: not flagged")
        s = r["summary"]["rule"]
        self.assertEqual((s["rule"], s["estimator"], s["threshold"]), ("value / sigma < 1.5", "map", 1.5))
        self.assertEqual(s["sigma_unknown_pixels"], int((np.isfinite(self.cube32) & ~(np.isfinite(sg) & (sg > 0))).sum()))

    def test_low_snr_with_the_robust_estimate_names_its_estimator(self):
        code, r, out = self.make("snrmad", method="low_snr", type="low_snr", k=2.0, sigma={"kind": "mad"})
        self.assertEqual(code, 0, r)
        m, _ = self.flags(out)
        want = np.zeros(self.cube.shape, dtype=bool)
        for ch in range(6):
            p = self.cube32[ch]
            fin = p[np.isfinite(p)]
            s = 1.4826 * np.median(np.abs(fin - np.median(fin)))
            with np.errstate(invalid="ignore"):
                want[ch] = np.isfinite(p) & (p / s < 2.0)
        self.assertTrue(np.array_equal(m, want))
        self.assertEqual(r["summary"]["rule"]["estimator"], "mad")
        self.assertIn("median absolute deviation", r["summary"]["rule"]["sigma"]["source"])

    def test_low_snr_refuses_a_bad_threshold_and_a_noise_map_on_another_grid(self):
        self.assertEqual(self.make("b1", method="low_snr", type="low_snr", k=0, sigma={"kind": "mad"})[0], 2)
        other = self.d / "other_grid.fits"
        fits.PrimaryHDU(np.ones((5, 5), dtype="<f4")).writeto(other, overwrite=True)
        code, r, _ = self.make("b2", method="low_snr", type="low_snr", k=2, sigma={"kind": "map", "path": str(other)})
        self.assertEqual(code, 2)
        self.assertIn("same pixel grid", r["error"])

    # ---- saturated -------------------------------------------------------------------------------------------------------
    def test_saturated_needs_a_source_for_its_threshold_and_flags_at_or_above(self):
        self.assertEqual(self.make("s1", method="saturated", type="saturated", threshold=50.0)[0], 2)
        code, r, out = self.make("s2", method="saturated", type="saturated", threshold=float(self.cube32[3, 4, 4]), source="detector datasheet")
        self.assertEqual(code, 0, r)
        m, _ = self.flags(out)
        self.assertTrue(m[3, 4, 4], "at the threshold counts")
        self.assertEqual(int(m.sum()), 1)
        self.assertEqual(r["summary"]["rule"]["source"], "detector datasheet")

    # ---- user excluded from regions ------------------------------------------------------------------------------------
    def test_a_circle_region_gives_the_pixel_centre_rule_for_every_channel(self):
        reg = {"shape": "circle", "cx": 3, "cy": 4, "r": 2, "name": "knot"}
        code, r, out = self.make("circ", method="regions", type="user_excluded", regions=[reg])
        self.assertEqual(code, 0, r)
        m, _ = self.flags(out)
        self.assertEqual(m.shape, (10, 8), "a region mask is one plane that applies to every channel")
        yy, xx = np.mgrid[0:10, 0:8]
        self.assertTrue(np.array_equal(m, (xx - 3) ** 2 + (yy - 4) ** 2 <= 4))
        self.assertEqual(r["summary"]["rule"]["regions"][0]["name"], "knot")
        # a rectangle is inclusive, and a region overhanging the image is clipped
        _, _, out2 = self.make("rect", method="regions", type="user_excluded", regions=[{"shape": "rect", "x0": 6, "y0": 8, "x1": 12, "y1": 12}])
        m2, _ = self.flags(out2)
        want = np.zeros((10, 8), dtype=bool)
        want[8:10, 6:8] = True
        self.assertTrue(np.array_equal(m2, want))

    # ---- import ----------------------------------------------------------------------------------------------------------
    def test_import_keeps_the_declared_meaning_of_nonzero_and_refuses_a_file_with_nan(self):
        src = np.zeros((10, 8), dtype="<i2")
        src[1:3, 1:3] = 1
        p = self.d / "theirs.fits"
        fits.PrimaryHDU(src).writeto(p, overwrite=True)
        code, r, out = self.make("imp1", method="import", type="invalid", path=str(p), nonzero="flagged")
        self.assertEqual(code, 0, r)
        self.assertTrue(np.array_equal(self.flags(out)[0], src != 0))
        code, r, out = self.make("imp2", method="import", type="user_excluded", path=str(p), nonzero="valid")
        self.assertEqual(code, 0, r)
        self.assertTrue(np.array_equal(self.flags(out)[0], src == 0), "nonzero = valid, so zero is what is flagged")
        self.assertIn("valid", r["summary"]["rule"]["rule"])
        self.assertEqual(self.make("imp3", method="import", type="invalid", path=str(p))[0], 2, "the meaning of nonzero is required")
        bad = self.d / "nanmask.fits"
        fits.PrimaryHDU(np.full((10, 8), nan, dtype="<f4")).writeto(bad, overwrite=True)
        code, r, _ = self.make("imp4", method="import", type="invalid", path=str(bad), nonzero="flagged")
        self.assertEqual(code, 2)
        self.assertIn("no value", r["error"])
        wrong = self.d / "wrongshape.fits"
        fits.PrimaryHDU(np.zeros((4, 4), dtype="<i2")).writeto(wrong, overwrite=True)
        self.assertEqual(self.make("imp5", method="import", type="invalid", path=str(wrong), nonzero="flagged")[0], 2)

    def test_type_other_needs_a_description(self):
        p = self.d / "o.fits"
        fits.PrimaryHDU(np.zeros((10, 8), dtype="<i2")).writeto(p, overwrite=True)
        self.assertEqual(self.make("o1", method="import", type="other", path=str(p), nonzero="flagged")[0], 2)
        self.assertEqual(self.make("o2", method="import", type="other", path=str(p), nonzero="flagged", meaning="ghosting from the bright star")[0], 0)

    # ---- applying: policies are per mask and recorded -----------------------------------------------------------------
    def spec(self, out, mtype, policy, **kw):
        return {"path": str(out), "type": mtype, "policy": policy, "id": kw.pop("id", 1), **kw}

    def stats(self, *specs):
        a = ["stats", "--input", self.f]
        if specs:
            a += ["--masks", json.dumps(list(specs))]
        code, r = run(*a)
        self.assertEqual(code, 0, r)
        return r["summary"]

    def test_stats_exclude_and_keep_give_different_correct_numbers_and_record_the_policy(self):
        _, _, sat = self.make("a_sat", method="saturated", type="saturated", threshold=10.0, source="datasheet")
        base = self.stats()
        kept = self.stats(self.spec(sat, "saturated", "keep"))
        excl = self.stats(self.spec(sat, "saturated", "exclude"))
        fin = self.cube32[np.isfinite(self.cube32)]
        self.assertEqual(base["finite"], fin.size)
        self.assertAlmostEqual(base["max"], float(fin.max()), places=5)
        self.assertEqual(kept["finite"], base["finite"], "keep changes nothing")
        self.assertEqual(kept["max"], base["max"])
        sel = np.isfinite(self.cube32) & ~(self.cube32 >= 10.0)
        self.assertEqual(excl["finite"], int(sel.sum()))
        self.assertAlmostEqual(excl["mean"], float(self.cube32[sel].mean()), places=6)
        self.assertAlmostEqual(excl["max"], float(self.cube32[sel].max()), places=5)
        self.assertNotEqual(excl["max"], base["max"])
        self.assertEqual(excl["no_value"], base["no_value"], "pixels with no value are counted apart from masked ones")
        self.assertEqual(excl["excluded_by_mask"], 1)
        self.assertEqual(excl["masks"][0]["policy"], "exclude")
        self.assertEqual(kept["masks"][0]["policy"], "keep")
        self.assertEqual((excl["masks"][0]["type"], excl["masks"][0]["pixels_flagged_with_value"]), ("saturated", 1))
        self.assertIn("never treated as masked", excl["policy"])

    def test_zeros_are_counted_by_stats_whatever_mask_is_applied_to_other_pixels(self):
        _, _, miss = self.make("a_miss", method="missing", type="missing")
        s = self.stats(self.spec(miss, "missing", "exclude"))
        fin = self.cube32[np.isfinite(self.cube32)]
        self.assertEqual(s["finite"], fin.size, "excluding `missing` removes nothing that has a value")
        self.assertEqual(s["excluded_by_mask"], 0)
        self.assertEqual(s["min"], float(fin.min()))
        self.assertLessEqual(s["min"], 0.0)
        zeros = int((self.cube32 == 0).sum())
        self.assertGreaterEqual(zeros, 7)
        zero_only = self.d / "zeros.fits"
        fits.PrimaryHDU(np.zeros((10, 8), dtype="<f4"), header=celestial(8, 10)).writeto(zero_only)
        code, r = run("stats", "--input", zero_only)
        self.assertEqual((r["summary"]["finite"], r["summary"]["no_value"]), (80, 0), "an all-zero plane is not an all-missing one")

    def test_a_pixel_with_no_value_cannot_be_kept_and_weight_is_refused_where_there_are_no_weights(self):
        _, _, miss = self.make("b_miss", method="missing", type="missing")
        code, r = run("stats", "--input", self.f, "--masks", json.dumps([self.spec(miss, "missing", "keep")]))
        self.assertEqual(code, 2)
        self.assertIn("no value", r["error"])
        _, _, sat = self.make("b_sat", method="saturated", type="saturated", threshold=10.0, source="s")
        code, r = run("stats", "--input", self.f, "--masks", json.dumps([self.spec(sat, "saturated", "weight", weight=0.5)]))
        self.assertEqual(code, 2)
        self.assertIn("no weights", r["error"])
        code, r = run("stats", "--input", self.f, "--masks", json.dumps([{"path": str(sat), "type": "saturated"}]))
        self.assertEqual(code, 2)
        self.assertIn("no default", r["error"])
        code, r = run("stats", "--input", self.f, "--masks", json.dumps([self.spec(sat, "low_snr", "exclude")]))
        self.assertEqual(code, 2, "a mask file of another type than the one named is refused")

    def test_moment_with_an_excluding_mask_matches_a_direct_sum_and_records_it(self):
        _, _, sat = self.make("m_sat", method="saturated", type="saturated", threshold=10.0, source="s")
        outs = {}
        for pol in ("keep", "exclude"):
            o = self.d / f"mom_{pol}.fits"
            code, r = run("moment", "--input", self.f, "--order", 0, "--out", o, "--masks", json.dumps([self.spec(sat, "saturated", pol)]))
            self.assertEqual(code, 0, r)
            with fits.open(o) as h:
                outs[pol] = (h[0].data.astype(float), r["summary"])
        dv = 2.0  # km/s per channel (2000 m/s)
        for pol, ex in (("keep", False), ("exclude", True)):
            ok = np.isfinite(self.cube32)
            if ex:
                ok &= ~(self.cube32 >= 10.0)
            want = np.where(ok.any(axis=0), np.where(ok, self.cube32, 0.0).sum(axis=0) * 2000.0, np.nan)
            got = outs[pol][0]
            np.testing.assert_allclose(got, want, rtol=1e-5, equal_nan=True)
        self.assertNotEqual(outs["keep"][0][4, 4], outs["exclude"][0][4, 4])
        self.assertEqual(outs["exclude"][1]["masks"][0]["policy"], "exclude")
        self.assertTrue(np.isnan(outs["exclude"][0][7, 5]) and np.isnan(outs["keep"][0][7, 5]))
        del dv

    def test_relative_orientation_takes_masks_and_weights_with_their_policy(self):
        n = 40
        h = header(n, n, 1.0 / 60.0, extra={"BMAJ": 3.0 / 60.0, "BMIN": 2.0 / 60.0, "BPA": 0.0})
        a = field(n, n, 31)
        b = (a + np.random.default_rng(1).normal(0, 15, (n, n))) % 180.0
        pa, pb = write_map(self.d / "ra.fits", a, h), write_map(self.d / "rb.fits", b, h)
        # a mask on the first map only: a block excluded
        ma = self.d / "ra_mask.fits"
        flags = np.zeros((n, n), dtype=np.uint8)
        flags[:10, :20] = 1
        fits.PrimaryHDU(flags, header=celestial(n, n)).writeto(ma)
        with fits.open(ma, mode="update") as hh:
            hh[0].header["VESQMTYP"] = "user_excluded"

        def go(policy, **kw):
            sp = [{"path": str(ma), "type": "user_excluded", "policy": policy, **kw}]
            ns = ro_args(input=pa, reference=pb, out=str(self.d / "rd.fits"), table_out=str(self.d / "rt.csv"), masks=json.dumps(sp))
            return ro.op_maps(ns, {"n_shifts": 0})["summary"]
        keep, excl, wt = go("keep"), go("exclude"), go("weight", weight=0.0)
        self.assertEqual(keep["counts"]["used"], n * n)
        self.assertEqual(excl["counts"]["used"], n * n - 200)
        self.assertEqual(excl["counts"]["excluded"]["excluded by a mask"], 200)
        d = (a - b + 90.0) % 180.0 - 90.0
        sel = np.ones((n, n), dtype=bool)
        sel[:10, :20] = False
        self.assertAlmostEqual(excl["am"], float(np.mean(np.cos(np.radians(2 * d[sel])))), places=5)
        self.assertNotAlmostEqual(excl["am"], keep["am"], places=4)
        self.assertEqual(excl["masks"]["map"][0]["policy"], "exclude")
        # weight 0 gives the unweighted statistic of the kept pixels, the weighted one is reported
        self.assertAlmostEqual(wt["am_weighted"], excl["am"], places=5)
        self.assertIn("weight policy", wt["weighting"])


if __name__ == "__main__":
    unittest.main()
