"""Golden numbers for a small synthetic family (ADR 0070): scalar and axial members on a 2 by 2 grid, with a missing pixel in each.

The expected values are worked out by hand (the comments show the arithmetic) and, for the spreads, again by a plain-Python loop with
the `math` and `statistics` modules only, so no number here comes from the code under test or from numpy."""
import json
import math
import statistics
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

import numpy as np
from astropy.io import fits

HERE = Path(__file__).resolve().parent
SCRIPT = str(HERE / "analysis.py")
NAN = float("nan")

SCALAR = {"ref": [[1.0, 2.0], [3.0, 4.0]], "a": [[2.0, 2.0], [5.0, NAN]], "b": [[0.0, 4.0], [3.0, 6.0]]}
AXIAL = {"ref": [[10.0, 0.0], [45.0, 90.0]], "a": [[170.0, 90.0], [50.0, NAN]], "b": [[12.0, 179.0], [40.0, 100.0]]}


def run(*args):
    p = subprocess.run([sys.executable, "-P", SCRIPT, *map(str, args)], capture_output=True, text=True, timeout=120)
    return p.returncode, json.loads(p.stdout)


def header(unit=None):
    h = fits.Header()
    for i, (ct, cv, cd) in enumerate([("RA---TAN", 150.0, -0.01), ("DEC--TAN", 2.0, 0.01)], start=1):
        h[f"CTYPE{i}"], h[f"CRVAL{i}"], h[f"CDELT{i}"], h[f"CRPIX{i}"], h[f"CUNIT{i}"] = ct, cv, cd, 1.0, "deg"
    if unit:
        h["BUNIT"] = unit
    return h


def family(tmp: Path, maps: dict, axial: bool, *opts):
    paths, members = {}, []
    for k, v in maps.items():
        paths[k] = tmp / f"{k}.fits"
        fits.PrimaryHDU(np.array(v, dtype=np.float32), header=header(None if axial else "K")).writeto(paths[k])
        members.append({"path": str(paths[k]), "label": k, **({"axial_unit": "deg"} if axial else {})})
    out = tmp / "out.fits"
    code, r = run("family_agreement", "--input", paths["ref"], "--family", json.dumps(members), "--reference-index", 0, "--out", out, *opts)
    assert code == 0, r
    return r["summary"], tmp


def pixels(grid):
    return [grid[0][0], grid[0][1], grid[1][0], grid[1][1]]


def axial_diff(x, y):                      # the axial distance of two orientations, in [0, 90]
    d = (x - y) % 180.0
    return min(d, 180.0 - d)


class ScalarGolden(unittest.TestCase):
    def test_scalar_family(self):
        with tempfile.TemporaryDirectory() as t:
            s, t = family(Path(t), SCALAR, False)
            self.assertEqual(s["unit"], "K")
            self.assertEqual(s["comparison_maps"], [1, 2])
            # a minus ref = [[1, 0], [2, -]], b minus ref = [[-1, 2], [0, 2]]
            a, b = s["members"][1], s["members"][2]
            self.assertEqual((a["pixels_compared"], a["pixels_without_data"]), (3, 1))
            self.assertAlmostEqual(a["mean"], 1.0)                       # (1 + 0 + 2) / 3
            self.assertAlmostEqual(a["median"], 1.0)
            self.assertAlmostEqual(a["rms"], math.sqrt(5.0 / 3.0))       # (1 + 0 + 4) / 3
            self.assertEqual((a["min"], a["max"]), (0.0, 2.0))
            self.assertEqual(b["pixels_compared"], 4)
            self.assertAlmostEqual(b["mean"], 0.75)                      # (-1 + 2 + 0 + 2) / 4
            self.assertAlmostEqual(b["rms"], 1.5)                        # sqrt((1 + 4 + 0 + 4) / 4)
            self.assertAlmostEqual(s["pairwise_rms"][1][2], 2.0)         # a - b = [[2, -2], [2, -]] -> sqrt(12 / 3)
            self.assertAlmostEqual(s["pairwise_rms"][1][1], 0.0)
            da, db = fits.getdata(t / "out-m1.fits"), fits.getdata(t / "out-m2.fits")
            self.assertTrue(np.array_equal(np.isnan(da), [[False, False], [False, True]]))   # a pixel without a value stays without one
            self.assertEqual([float(x) for x in da[~np.isnan(da)]], [1.0, 0.0, 2.0])
            self.assertEqual(db.tolist(), [[-1.0, 2.0], [0.0, 2.0]])

    def test_std_and_mad_spread_by_hand_and_by_a_plain_loop(self):
        # per pixel the values of (ref, a, b): (1, 2, 0), (2, 2, 4), (3, 5, 3), (4, -, 6)
        by_hand_std = [1.0, math.sqrt(4.0 / 3.0), math.sqrt(4.0 / 3.0), math.sqrt(2.0)]
        by_hand_mad = [1.0, 0.0, 0.0, 1.0]       # medians 1, 2, 3, 5 and deviations (0, 1, 1), (0, 0, 2), (0, 2, 0), (1, 1)
        cols = [[m[i // 2][i % 2] for m in SCALAR.values() if not math.isnan(m[i // 2][i % 2])] for i in range(4)]
        loop_std = [statistics.stdev(c) for c in cols]
        loop_mad = [statistics.median(abs(x - statistics.median(c)) for x in c) for c in cols]
        for how, hand, loop in (("std", by_hand_std, loop_std), ("mad", by_hand_mad, loop_mad)):
            with tempfile.TemporaryDirectory() as t:
                s, t = family(Path(t), SCALAR, False, "--spread", how)
                got = pixels(fits.getdata(t / "out.fits").tolist())
                for g, h, lp in zip(got, hand, loop):
                    self.assertAlmostEqual(g, h, places=5)
                    self.assertAlmostEqual(h, lp, places=9)
                self.assertEqual(s["spread"], how)
                self.assertEqual(s["spread_stats"]["pixels"], 4)

    def test_a_lone_value_has_no_spread(self):
        maps = {"ref": [[1.0, NAN]], "a": [[3.0, 5.0]]}
        with tempfile.TemporaryDirectory() as t:
            s, t = family(Path(t), maps, False)
            sp = fits.getdata(t / "out.fits")
            self.assertAlmostEqual(float(sp[0, 0]), math.sqrt(2.0), places=5)     # (1, 3), ddof 1
            self.assertTrue(np.isnan(sp[0, 1]))                                    # one value: undefined, never 0


class AxialGolden(unittest.TestCase):
    def test_axial_family(self):
        with tempfile.TemporaryDirectory() as t:
            s, t = family(Path(t), AXIAL, True)
            self.assertTrue(s["axial"])
            a, b = s["members"][1], s["members"][2]
            # a against ref: 10 vs 170 is 20 degrees (170 is -10), 0 vs 90 is 90, 45 vs 50 is 5; the last pixel has no value in a
            self.assertEqual(a["pixels_compared"], 3)
            self.assertAlmostEqual(a["mean_abs_deg"], 115.0 / 3.0)
            self.assertAlmostEqual(a["rms_deg"], math.sqrt((400 + 8100 + 25) / 3.0))
            self.assertAlmostEqual(a["within_10_deg"], 1.0 / 3.0)
            self.assertAlmostEqual(a["am"], (math.cos(math.radians(40)) + math.cos(math.radians(180)) + math.cos(math.radians(10))) / 3.0)
            # b against ref: 2, 1 (0 vs 179), 5, 10
            self.assertEqual(b["pixels_compared"], 4)
            self.assertAlmostEqual(b["mean_abs_deg"], 18.0 / 4.0)
            self.assertAlmostEqual(b["within_10_deg"], 1.0)                    # 10 is within 10
            self.assertAlmostEqual(b["am"], sum(math.cos(math.radians(2 * d)) for d in (2, 1, 5, 10)) / 4.0)
            d = fits.getdata(t / "out-m1.fits")
            self.assertAlmostEqual(float(d[0, 0]), 20.0, places=4)
            self.assertAlmostEqual(float(d[0, 1]), 90.0, places=4)
            self.assertTrue(np.isnan(d[1, 1]))
            self.assertAlmostEqual(float(np.nanmax(fits.getdata(t / "out-m2.fits"))), 10.0, places=4)
            # pairwise AM of a and b over the three pixels both have: 170 vs 12 is 22, 90 vs 179 is 89, 50 vs 40 is 10
            want = sum(math.cos(math.radians(2 * axial_diff(x, y))) for x, y in ((170, 12), (90, 179), (50, 40))) / 3.0
            self.assertAlmostEqual(s["pairwise_am"][1][2], want)
            self.assertAlmostEqual(s["pairwise_am"][2][1], want)

    def test_the_axial_spread_by_a_plain_loop(self):
        # the doubled angles' resultant length R over the members with a value; spread = sqrt(-2 ln R) / 2 (the root in degrees)
        cols = [[m[i // 2][i % 2] for m in AXIAL.values() if not math.isnan(m[i // 2][i % 2])] for i in range(4)]
        want = []
        for c in cols:
            cs = sum(math.cos(math.radians(2 * x)) for x in c) / len(c)
            sn = sum(math.sin(math.radians(2 * x)) for x in c) / len(c)
            r = math.hypot(cs, sn)
            want.append(math.degrees(math.sqrt(-2.0 * math.log(r))) / 2.0)
        with tempfile.TemporaryDirectory() as t:
            s, t = family(Path(t), AXIAL, True)
            got = pixels(fits.getdata(t / "out.fits").tolist())
            for g, w in zip(got, want):
                self.assertAlmostEqual(g, w, places=3)
        # pixel (0, 0): 10, 170 and 12 degrees agree to about 10 degrees of spread, not the ~90 of a plain standard deviation
        self.assertAlmostEqual(want[0], 10.0, delta=0.1)
        self.assertGreater(statistics.stdev([10.0, 170.0, 12.0]), 80.0)

    def test_orientations_that_cancel_are_blank_and_counted(self):
        maps = {"ref": [[0.0, 10.0]], "a": [[90.0, 12.0]]}
        with tempfile.TemporaryDirectory() as t:
            s, t = family(Path(t), maps, True)
            sp = fits.getdata(t / "out.fits")
            self.assertTrue(np.isnan(sp[0, 0]))                                    # perpendicular: R = 0, undefined
            self.assertFalse(np.isnan(sp[0, 1]))
            self.assertEqual(s["orientations_cancel_pixels"], 1)


if __name__ == "__main__":
    unittest.main()
