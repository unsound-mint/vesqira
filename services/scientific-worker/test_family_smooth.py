"""Spectral smoothing and the family agreement (ADR 0069), checked against answers worked out in the tests: a Gaussian line broadened by a
known kernel (widths add in quadrature, the peak falls by the width ratio), a delta spike giving the kernel back, NaN handling, an
uncalibrated spectral axis, and the 1 and 179 degree axial case."""
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
import family as fam  # noqa: E402
import specsmooth as ss  # noqa: E402

SCRIPT = str(HERE / "analysis.py")
F = 2.0 * math.sqrt(2.0 * math.log(2.0))


def run(*args):
    p = subprocess.run([sys.executable, "-P", SCRIPT, *map(str, args)], capture_output=True, text=True, timeout=120)
    return p.returncode, json.loads(p.stdout)


def cube_header(nc, ny, nx, calibrated=True):
    h = fits.Header()
    h["BUNIT"] = "K"
    for i, (ct, cv, cd, cp, cu) in enumerate([("RA---TAN", 150.0, -0.01, nx / 2 + 0.5, "deg"), ("DEC--TAN", 2.0, 0.01, ny / 2 + 0.5, "deg"),
                                              ("VRAD", -32000.0, 500.0, 1.0, "m/s")], start=1):
        if i == 3 and not calibrated:
            continue
        h[f"CTYPE{i}"], h[f"CRVAL{i}"], h[f"CDELT{i}"], h[f"CRPIX{i}"], h[f"CUNIT{i}"] = ct, cv, cd, cp, cu
    return h


class SmoothSpectral(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        cls.dir = Path(cls.tmp.name)

    @classmethod
    def tearDownClass(cls):
        cls.tmp.cleanup()

    def write(self, name, data, header):
        p = self.dir / name
        fits.PrimaryHDU(np.asarray(data, dtype=np.float32), header=header).writeto(p, overwrite=True)
        return p

    def smooth(self, src, name, *opts):
        out = self.dir / name
        code, r = run("smooth_spectral", "--input", src, "--out", out, *opts)
        return code, r, out

    def test_a_gaussian_line_broadened_by_a_gaussian_kernel_adds_widths_in_quadrature(self):
        nc, sig_line, fwhm_k = 201, 6.0, 8.0
        k = np.arange(nc)
        spec = 10.0 * np.exp(-0.5 * ((k - 100) / sig_line) ** 2)
        src = self.write("line.fits", np.broadcast_to(spec[:, None, None], (nc, 3, 4)), cube_header(nc, 3, 4))
        code, r, out = self.smooth(src, "line_s.fits", "--kernel", "gaussian", "--width", fwhm_k)
        self.assertEqual(code, 0, r)
        got = fits.getdata(out)[:, 1, 2].astype(float)
        sig_k = fwhm_k / F
        sig = math.hypot(sig_line, sig_k)
        want = 10.0 * sig_line / sig * np.exp(-0.5 * ((k - 100) / sig) ** 2)        # the exact convolution of two Gaussians
        self.assertLess(np.max(np.abs(got - want)), 2e-3)
        # width measured from the data: second moment about the centre
        self.assertAlmostEqual(math.sqrt((got * (k - 100) ** 2).sum() / got.sum()), sig, places=2)
        s = r["summary"]
        self.assertEqual(s["kernel"]["kind"], "gaussian")
        self.assertAlmostEqual(sum(s["kernel"]["weights"]), 1.0, places=12)
        self.assertAlmostEqual(s["kernel"]["fwhm_channels"], fwhm_k)
        self.assertTrue(s["spectral"]["calibrated"])
        self.assertAlmostEqual(s["spectral"]["fwhm_unit"]["value"], fwhm_k * 500.0)       # 500 m/s per channel, from the header
        self.assertEqual(s["spectral"]["fwhm_unit"]["unit"], "m/s")
        self.assertIsNone(s["resolution"]["effective_fwhm"])                              # no native response declared: not known
        self.assertIn("not declared", s["resolution"]["statement"])
        with fits.open(out) as h:
            self.assertEqual(h[0].header["VESQOP"], "smooth_spectral")
            self.assertEqual(h[0].header["BUNIT"], "K")
            self.assertEqual(h[0].data.shape, (nc, 3, 4))

    def test_a_declared_native_response_adds_in_quadrature_for_a_gaussian_only(self):
        src = self.write("flat.fits", np.ones((40, 2, 2)), cube_header(40, 2, 2))
        _, r, _ = self.smooth(src, "g.fits", "--kernel", "gaussian", "--width", 4, "--native-fwhm", 3)
        self.assertAlmostEqual(r["summary"]["resolution"]["effective_fwhm"]["channels"], 5.0)
        _, r, _ = self.smooth(src, "b.fits", "--kernel", "boxcar", "--width", 5, "--native-fwhm", 3)
        self.assertIsNone(r["summary"]["resolution"]["effective_fwhm"])
        self.assertIn("not Gaussian", r["summary"]["resolution"]["statement"])

    def test_kernels_are_what_they_say(self):
        self.assertTrue(np.allclose(ss.build_kernel("hanning", 3)["weights"], [0.25, 0.5, 0.25]))      # the radio-astronomy Hanning
        self.assertEqual(ss.build_kernel("hanning", 3)["fwhm_channels"], 2.0)
        self.assertTrue(np.allclose(ss.build_kernel("boxcar", 5)["weights"], 0.2))
        for bad in (("boxcar", 4), ("hanning", 2), ("boxcar", 1), ("gaussian", 0.5), ("gaussian", -1), ("lorentz", 3), ("boxcar", 3.5)):
            with self.assertRaises(ss.Bad, msg=str(bad)):
                ss.build_kernel(*bad)

    def test_a_spike_gives_the_kernel_back_and_the_channel_response_is_symmetric(self):
        d = np.zeros((21, 2, 2))
        d[10] = 1.0
        src = self.write("spike.fits", d, cube_header(21, 2, 2))
        for kind, width in (("boxcar", 5), ("hanning", 5), ("gaussian", 3.0)):
            code, r, out = self.smooth(src, f"spike_{kind}.fits", "--kernel", kind, "--width", width)
            self.assertEqual(code, 0, r)
            got = fits.getdata(out)[:, 0, 0].astype(float)
            w = np.array(r["summary"]["kernel"]["weights"])
            h = len(w) // 2
            self.assertTrue(np.allclose(got[10 - h:10 + h + 1], w, atol=1e-6), kind)
            self.assertTrue(np.allclose(w, w[::-1]))

    def test_missing_samples_stay_missing_and_neighbours_average_the_finite_ones(self):
        d = np.full((9, 1, 2), 2.0)
        d[4, 0, 0] = np.nan
        d[:, 0, 1] = np.arange(9)
        src = self.write("nan.fits", d, cube_header(9, 1, 2))
        code, r, out = self.smooth(src, "nan_s.fits", "--kernel", "boxcar", "--width", 3)
        self.assertEqual(code, 0, r)
        got = fits.getdata(out).astype(float)
        self.assertTrue(np.isnan(got[4, 0, 0]))                    # NaN in, NaN out
        self.assertAlmostEqual(got[3, 0, 0], 2.0)                  # mean of the finite neighbours, not (2+2+0)/3
        self.assertAlmostEqual(got[5, 0, 0], 2.0)
        self.assertAlmostEqual(got[4, 0, 1], 4.0)                  # a clean ramp is unchanged in the middle
        self.assertFalse(np.any(got[:, 0, 0][np.isfinite(got[:, 0, 0])] == 0.0))        # no zero invented
        # edge: only 2 of 3 weights are inside the cube, 2/3 >= 0.5, so it has a value and is the mean of the two
        self.assertAlmostEqual(got[0, 0, 1], 0.5)
        self.assertEqual(r["summary"]["missing_policy"]["min_valid_weight"], 0.5)

    def test_a_sample_with_too_little_valid_weight_is_dropped_and_counted(self):
        d = np.full((9, 1, 1), 1.0)
        d[[3, 5], 0, 0] = np.nan                                   # sample 4 has 1 of 3 valid weights
        src = self.write("sparse.fits", d, cube_header(9, 1, 1))
        _, r, out = self.smooth(src, "sparse_s.fits", "--kernel", "boxcar", "--width", 3, "--min-valid-weight", 0.5)
        got = fits.getdata(out)[:, 0, 0]
        self.assertTrue(np.isnan(got[4]))
        self.assertEqual(r["summary"]["missing_policy"]["finite_samples_dropped"], 1)
        _, r, out = self.smooth(src, "sparse_t.fits", "--kernel", "boxcar", "--width", 3, "--min-valid-weight", 0.3)
        self.assertAlmostEqual(float(fits.getdata(out)[4, 0, 0]), 1.0)

    def test_an_uncalibrated_spectral_axis_gives_channels_only_with_a_warning(self):
        src = self.write("nocal.fits", np.ones((10, 2, 2)), cube_header(10, 2, 2, calibrated=False))
        code, r, _ = self.smooth(src, "nocal_s.fits", "--kernel", "boxcar", "--width", 3)
        self.assertEqual(code, 0, r)
        s = r["summary"]
        self.assertFalse(s["spectral"]["calibrated"])
        self.assertNotIn("fwhm_unit", s["spectral"])
        self.assertIsNone(s["spectral"]["channel_width"])
        self.assertIn("channels only", s["spectral"]["warning"])
        self.assertIn("no channel width is assumed", s["policy"])

    def test_decimation_only_when_asked_and_it_rewrites_the_spectral_axis(self):
        src = self.write("dec.fits", np.arange(12)[:, None, None] * np.ones((12, 2, 2)), cube_header(12, 2, 2))
        _, r, out = self.smooth(src, "dec_keep.fits", "--kernel", "hanning", "--width", 3)
        self.assertEqual(r["summary"]["decimation"]["step"], 1)
        self.assertEqual(fits.getdata(out).shape[0], 12)
        code, r, out = self.smooth(src, "dec_2.fits", "--kernel", "hanning", "--width", 3, "--decimate", 2)
        self.assertEqual(code, 0, r)
        with fits.open(out) as h:
            hd = h[0].header
            self.assertEqual(h[0].data.shape[0], 6)
            self.assertAlmostEqual(hd["CDELT3"], 1000.0)
            # channel j of the result is channel 2j of the input: the same velocity
            self.assertAlmostEqual(-32000.0 + (1 - hd["CRPIX3"] + 3) * hd["CDELT3"], -32000.0 + 6 * 500.0)
        self.assertEqual(r["summary"]["decimation"]["channels_out"], 6)
        self.assertIn("chosen by you", r["summary"]["policy"])

    def test_refusals_and_the_input_is_untouched(self):
        d = np.ones((10, 2, 2))
        src = self.write("ref.fits", d, cube_header(10, 2, 2))
        before = src.read_bytes()
        for opts, text in ((["--kernel", "boxcar", "--width", 4], "odd"), (["--kernel", "gaussian", "--width", 0.4], "narrower"),
                           (["--kernel", "boxcar", "--width", 3, "--min-valid-weight", 0], "minimum valid weight"),
                           (["--kernel", "boxcar", "--width", 3, "--axial-unit", "deg"], "axial")):
            code, r, _ = self.smooth(src, "refused.fits", *opts)
            self.assertEqual(code, 2)
            self.assertIn(text, r["error"])
        self.assertEqual(src.read_bytes(), before)
        self.assertFalse((self.dir / "refused.fits").exists())

    def test_a_cube_is_read_in_strips_and_equals_one_pass(self):
        rng = np.random.default_rng(3)
        d = rng.normal(size=(30, 7, 5))
        d[rng.random(d.shape) < 0.1] = np.nan
        w = ss.build_kernel("gaussian", 4.0)["weights"]
        whole, _ = ss.smooth_block(d, w, 0.5)
        parts = np.concatenate([ss.smooth_block(d[:, a:b], w, 0.5)[0] for a, b in ((0, 3), (3, 7))], axis=1)
        self.assertTrue(np.array_equal(np.nan_to_num(whole, nan=-9), np.nan_to_num(parts, nan=-9)))


class Agreement(unittest.TestCase):
    def test_the_one_and_179_degree_case(self):
        a, b = np.array([[1.0]]), np.array([[179.0]])
        self.assertAlmostEqual(float(fam.axial_difference(a, b)[0, 0]), 2.0)                # not 178
        am, n = fam.alignment_measure(a, b)
        self.assertAlmostEqual(am, math.cos(math.radians(4.0)))                              # cos 2 delta, delta = 2 degrees
        self.assertEqual(n, 1)
        # orthogonal and parallel
        self.assertAlmostEqual(fam.alignment_measure(np.array([[0.0]]), np.array([[90.0]]))[0], -1.0)
        self.assertAlmostEqual(fam.alignment_measure(np.array([[10.0]]), np.array([[10.0]]))[0], 1.0)
        # the axial spread of 1 and 179 is small (their doubled angles are 2 and 358), not the 89 a plain std would give
        sp, r, cancel = fam.axial_spread(np.array([[[1.0]], [[179.0]]]))
        self.assertLess(float(sp[0, 0]), 3.0)
        self.assertEqual(cancel, 0)
        # two perpendicular orientations cancel: undefined, not a number
        sp, r, cancel = fam.axial_spread(np.array([[[0.0]], [[90.0]]]))
        self.assertTrue(np.isnan(sp[0, 0]))
        self.assertEqual(cancel, 1)

    def test_scalar_spread_needs_two_values_and_states_std_or_mad(self):
        st = np.array([[[1.0, np.nan]], [[3.0, 5.0]], [[8.0, np.nan]]])
        self.assertAlmostEqual(float(fam.scalar_spread(st, "std")[0, 0]), float(np.std([1, 3, 8], ddof=1)))
        self.assertAlmostEqual(float(fam.scalar_spread(st, "mad")[0, 0]), 2.0)               # median 3, |dev| 2, 0, 5 -> 2
        self.assertTrue(np.isnan(fam.scalar_spread(st, "std")[0, 1]))                         # one value: no spread, not 0

    def test_end_to_end_axial_family_through_the_worker_never_a_plain_difference(self):
        with tempfile.TemporaryDirectory() as t:
            t = Path(t)
            h = fits.Header()
            for i, (ct, cv, cd) in enumerate([("RA---TAN", 150.0, -0.01), ("DEC--TAN", 2.0, 0.01)], start=1):
                h[f"CTYPE{i}"], h[f"CRVAL{i}"], h[f"CDELT{i}"], h[f"CRPIX{i}"], h[f"CUNIT{i}"] = ct, cv, cd, 2.0, "deg"
            maps = {"ref": [[1.0, 90.0], [np.nan, 45.0]], "m1": [[179.0, 0.0], [10.0, 135.0]], "m2": [[3.0, 90.0], [20.0, 45.0]]}
            paths = {}
            for k, v in maps.items():
                paths[k] = t / f"{k}.fits"
                fits.PrimaryHDU(np.array(v, dtype=np.float32), header=h).writeto(paths[k])
            fam_json = json.dumps([{"path": str(paths[k]), "label": k, "axial_unit": "deg"} for k in ("ref", "m1", "m2")])
            code, r = run("family_agreement", "--input", paths["ref"], "--family", fam_json, "--reference-index", 0, "--out", t / "sp.fits")
            self.assertEqual(code, 0, r)
            s = r["summary"]
            self.assertTrue(s["axial"])
            m1 = s["members"][1]
            self.assertEqual(m1["pixels_compared"], 3)                    # the NaN pixel of the reference is not compared
            # 1 vs 179: 2 deg; 90 vs 0: 90; 45 vs 135: 90
            self.assertAlmostEqual(m1["mean_abs_deg"], (2 + 90 + 90) / 3)
            self.assertAlmostEqual(m1["am"], (math.cos(math.radians(4)) + (-1) + (-1)) / 3)
            d1 = fits.getdata(t / "sp-m1.fits")
            self.assertAlmostEqual(float(d1[0, 0]), 2.0, places=4)
            self.assertTrue(np.isnan(d1[1, 0]))
            self.assertAlmostEqual(float(d1[0, 1]), 90.0, places=4)
            self.assertTrue(np.nanmax(d1) <= 90.0 and np.nanmin(d1) >= 0.0)
            self.assertEqual(s["comparison_maps"], [1, 2])
            self.assertIn("no plain difference is taken", s["policy"])
            self.assertAlmostEqual(s["pairwise_am"][1][2] if s["pairwise_am"][1][2] is not None else 0, s["pairwise_am"][2][1])
            sp = fits.getdata(t / "sp.fits")
            self.assertLess(float(sp[0, 0]), 3.0)                         # 1, 179 and 3 degrees agree to within a few degrees

    def test_a_scalar_family_needs_one_unit_and_mixed_kinds_are_refused(self):
        with tempfile.TemporaryDirectory() as t:
            t = Path(t)
            h = fits.Header()
            for i, (ct, cv, cd) in enumerate([("RA---TAN", 150.0, -0.01), ("DEC--TAN", 2.0, 0.01)], start=1):
                h[f"CTYPE{i}"], h[f"CRVAL{i}"], h[f"CDELT{i}"], h[f"CRPIX{i}"], h[f"CUNIT{i}"] = ct, cv, cd, 2.0, "deg"
            ps = []
            for k, (v, u) in enumerate([(1.0, "K"), (2.0, "K"), (4.0, "Jy/beam")]):
                hh = h.copy()
                hh["BUNIT"] = u
                p = t / f"s{k}.fits"
                fits.PrimaryHDU(np.full((2, 2), v, dtype=np.float32), header=hh).writeto(p)
                ps.append(p)
            ok = json.dumps([{"path": str(p), "label": f"s{i}"} for i, p in enumerate(ps[:2])])
            code, r = run("family_agreement", "--input", ps[0], "--family", ok, "--out", t / "o.fits", "--spread", "mad")
            self.assertEqual(code, 0, r)
            self.assertAlmostEqual(r["summary"]["members"][1]["mean"], 1.0)
            self.assertEqual(r["summary"]["unit"], "K")
            bad = json.dumps([{"path": str(p), "label": f"s{i}"} for i, p in enumerate(ps)])
            code, r = run("family_agreement", "--input", ps[0], "--family", bad, "--out", t / "o2.fits")
            self.assertEqual(code, 2)
            self.assertIn("different brightness units", r["error"])
            mixed = json.dumps([{"path": str(ps[0]), "label": "a", "axial_unit": "deg"}, {"path": str(ps[1]), "label": "b"}])
            code, r = run("family_agreement", "--input", ps[0], "--family", mixed, "--out", t / "o3.fits")
            self.assertEqual(code, 2)
            self.assertIn("mixes orientation maps", r["error"])


if __name__ == "__main__":
    unittest.main()
