"""The decomposition review (gaussreview.py, analysis ops rohsa_sigmav, rohsa_cellmask, rohsa_classmaps), checked by hand against a
small decomposition whose components are written in the test.

The crop is 3 x 2 pixels (not square) with 10 channels on a velocity axis of 1 km/s per channel, v(channel c) = -12 + c km/s, so a
width in channels is a width in km/s and a file mean m (1-based) sits at -12 + (m - 1) km/s. Components, (amp, file mean, sigma):

  pixel (y, x)   components
  (0, 0)         (2, 3, 1)   v=-10 s=1       (1, 6, 3)   v=-7 s=3
  (0, 1)         (4, 3, 2)   v=-10 s=2 (on the cut)      (0, 8, 1.5) v=-5 s=1.5, amplitude 0
  (0, 2)         (1, 3, 0)   invalid sigma   (3, 5, 2.5) v=-8 s=2.5
  (1, 0)         (1.5, 3, 1) v=-10 s=1       (2, 3, 1)   v=-10 s=1
  (1, 1)         (1, 3, -1)  invalid         (2, 4, 0)   invalid
  (1, 2)         (1, 10, 1)  v=-3 s=1        (1, 11, 1)  v=-2 s=1 (on the last edge of the diagram below)

Diagram bins: centre -12..-2 in 5 bins of 2 km/s, width 0..4 in 4 bins of 1 km/s.
"""
import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

import numpy as np
from astropy.io import fits

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import gaussreview as gr  # noqa: E402

SCRIPT = str(HERE / "analysis.py")
S = float(np.sqrt(2 * np.pi))          # integral of a unit Gaussian: amp x sigma x S x dv, dv = 1 km/s
NX, NY, NC, N = 3, 2, 10, 2
COMPONENTS = {
    (0, 0): [(2.0, 3.0, 1.0), (1.0, 6.0, 3.0)],
    (0, 1): [(4.0, 3.0, 2.0), (0.0, 8.0, 1.5)],
    (0, 2): [(1.0, 3.0, 0.0), (3.0, 5.0, 2.5)],
    (1, 0): [(1.5, 3.0, 1.0), (2.0, 3.0, 1.0)],
    (1, 1): [(1.0, 3.0, -1.0), (2.0, 4.0, 0.0)],
    (1, 2): [(1.0, 10.0, 1.0), (1.0, 11.0, 1.0)],
}
V_BINS = {"min": -12.0, "max": -2.0, "n": 5}
S_BINS = {"min": 0.0, "max": 4.0, "n": 4}


def write_dat(path: Path, comps=COMPONENTS, header=True):
    lines = [" # ", " # i, j, A, mean, sigma"] if header else []
    for y in range(NY):
        for x in range(NX):
            for a, m, s in comps[(y, x)]:
                lines.append(f"{y:12d}{x:12d}{a:26.17g}{m:26.17g}{s:26.17g}")
    path.write_text("\n".join(lines) + "\n")


def write_crop(path: Path, bunit="K", velocity=True):
    h = fits.PrimaryHDU(np.zeros((NC, NY, NX), dtype=np.float32)).header
    h["CTYPE1"], h["CRVAL1"], h["CDELT1"], h["CRPIX1"], h["CUNIT1"] = "RA---TAN", 150.0, -0.01, 2.0, "deg"
    h["CTYPE2"], h["CRVAL2"], h["CDELT2"], h["CRPIX2"], h["CUNIT2"] = "DEC--TAN", 2.0, 0.01, 1.5, "deg"
    if velocity:
        h["CTYPE3"], h["CRVAL3"], h["CDELT3"], h["CRPIX3"], h["CUNIT3"] = "VRAD", -12000.0, 1000.0, 1.0, "m/s"
    else:
        h["CTYPE3"], h["CRVAL3"], h["CDELT3"], h["CRPIX3"], h["CUNIT3"] = "FREQ", 1.42e9, 1.0e4, 1.0, "Hz"   # no rest frequency: not a velocity
    h["RADESYS"] = "ICRS"
    if bunit is not None:
        h["BUNIT"] = bunit
    fits.PrimaryHDU(np.zeros((NC, NY, NX), dtype=np.float32), header=h).writeto(path, overwrite=True)


def run(op, crop, dat, out=None, **params):
    args = [sys.executable, "-P", SCRIPT, op, "--input", str(crop), "--params", json.dumps({"dat_path": str(dat), "n_gauss": N, **params})]
    if out:
        args += ["--out", str(out)]
    p = subprocess.run(args, capture_output=True, text=True, timeout=120)
    return p.returncode, json.loads(p.stdout)


class Base(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.dir = Path(self.tmp.name)
        self.crop, self.dat = self.dir / "crop.fits", self.dir / "rohsa_n2.dat"
        write_crop(self.crop)
        write_dat(self.dat)
        self.cache_tmp = tempfile.TemporaryDirectory()      # never inside the .dat's folder
        self.cache = self.cache_tmp.name

    def tearDown(self):
        self.tmp.cleanup()
        self.cache_tmp.cleanup()

    def sigmav(self, **kw):
        kw.setdefault("velocity_bins", V_BINS)
        kw.setdefault("sigma_bins", S_BINS)
        kw.setdefault("weight", "integrated")
        kw.setdefault("cache_dir", self.cache)
        return run("rohsa_sigmav", self.crop, self.dat, **kw)


class Sigmav(Base):
    def cells(self, summary):
        return {(c[0], c[1]): (c[2], c[3]) for c in summary["cells"]}

    def test_counts_and_weights_match_the_hand_calculation(self):
        code, r = self.sigmav()
        self.assertEqual(code, 0, r)
        s = r["summary"]
        cells = self.cells(s)
        # (width bin, centre bin): (contributors, integrated K km/s)
        want = {(1, 1): (3, 5.5 * S), (3, 2): (1, 3.0 * S), (2, 1): (1, 8.0 * S), (1, 3): (1, 0.0), (2, 2): (1, 7.5 * S), (1, 4): (1, 1.0 * S)}
        self.assertEqual(set(cells), set(want))
        for k, (n, w) in want.items():
            self.assertEqual(cells[k][0], n, k)
            self.assertAlmostEqual(cells[k][1], w, places=12, msg=str(k))
        self.assertEqual((s["components"], s["invalid_sigma"], s["outside"], s["in_diagram"]), (12, 3, 1, 8))
        self.assertAlmostEqual(s["weight_total"], 26.0 * S, places=12)
        self.assertAlmostEqual(s["weight_outside"], 1.0 * S, places=12)
        self.assertEqual(s["weight_unit"], "K km/s")
        self.assertEqual(s["axes"]["centre"]["unit"], "km/s")
        self.assertEqual(s["v_edges"], [-12.0, -10.0, -8.0, -6.0, -4.0, -2.0])

    def test_a_width_on_a_bin_edge_is_in_the_bin_above_and_the_last_edge_is_outside(self):
        code, r = self.sigmav()
        cells = self.cells(r["summary"])
        self.assertIn((2, 1), cells)            # width exactly 2.0 is in [2, 3), centre exactly -10 is in [-10, -8)
        self.assertNotIn((1, 1 + 0), {k: v for k, v in cells.items() if v[0] == 0})
        self.assertEqual(r["summary"]["outside"], 1)   # the component at v = -2, the last edge

    def test_a_zero_amplitude_component_is_a_contributor_with_weight_zero(self):
        code, r = self.sigmav()
        self.assertEqual(self.cells(r["summary"])[(1, 3)], (1, 0.0))

    def test_count_weight_and_column_density(self):
        _, r = self.sigmav(weight="count")
        s = r["summary"]
        self.assertEqual(s["weight_unit"], "components")
        self.assertEqual(self.cells(s)[(1, 1)], (3, 3.0))
        _, r = self.sigmav(weight="column_thin")
        s = r["summary"]
        self.assertEqual(s["weight_unit"], "cm^-2")
        self.assertAlmostEqual(self.cells(s)[(3, 2)][1] / (3.0 * S * gr.NHI_PER_K_KMS), 1.0, places=12)
        self.assertIn("optically thin", s["weight_how"])

    def test_observed_extent_helps_set_the_bins(self):
        _, r = self.sigmav()
        self.assertEqual(r["summary"]["observed"], {"sigma": [1.0, 3.0], "centre": [-10.0, -2.0]})

    def test_default_bins_come_from_the_crop_and_hold_every_channel_centred_component(self):
        code, r = run("rohsa_sigmav", self.crop, self.dat, weight="count")
        s = r["summary"]
        self.assertEqual(code, 0, r)
        self.assertEqual(s["v_edges"][0], -12.5)
        self.assertEqual(s["v_edges"][-1], -2.5)             # half a channel of margin at both ends
        self.assertEqual(len(s["v_edges"]) - 1, 10)
        self.assertEqual(s["outside"], 1)                    # only the component at channel index 10, off the axis

    def test_unknown_brightness_unit_refuses_a_flux_and_still_counts(self):
        write_crop(self.crop, bunit=None)
        code, r = self.sigmav()
        self.assertEqual(code, 2)
        self.assertIn("no brightness unit", r["error"])
        code, r = self.sigmav(weight="count")
        self.assertEqual(code, 0)
        self.assertIsNone(r["summary"]["brightness_unit"])

    def test_a_declared_unit_fills_the_gap_and_is_labelled_as_declared(self):
        write_crop(self.crop, bunit=None)
        p = subprocess.run([sys.executable, "-P", SCRIPT, "rohsa_sigmav", "--input", str(self.crop), "--declared-unit", "K",
                            "--params", json.dumps({"dat_path": str(self.dat), "n_gauss": N, "velocity_bins": V_BINS, "sigma_bins": S_BINS})],
                           capture_output=True, text=True)
        s = json.loads(p.stdout)["summary"]
        self.assertEqual(s["weight_unit"], "K km/s")
        self.assertEqual(s["unit_source"], "user")
        self.assertIn("declared by the author", s["policy"])

    def test_column_density_needs_kelvin(self):
        write_crop(self.crop, bunit="Jy/beam")
        code, r = self.sigmav(weight="column_thin")
        self.assertEqual(code, 2)
        self.assertIn("needs a brightness temperature in K", r["error"])
        code, r = self.sigmav()
        self.assertEqual(r["summary"]["weight_unit"], "Jy/beam km/s")

    def test_an_axis_that_is_not_a_velocity_stays_in_channels_and_assumes_no_km_s(self):
        write_crop(self.crop, velocity=False)
        code, r = self.sigmav()
        self.assertEqual(code, 2)
        self.assertIn("not a resolved velocity axis", r["error"])
        code, r = run("rohsa_sigmav", self.crop, self.dat, weight="count", velocity_bins={"min": -0.5, "max": 9.5, "n": 10}, sigma_bins={"min": 0, "max": 4, "n": 4})
        s = r["summary"]
        self.assertEqual(code, 0, r)
        self.assertEqual(s["axes"]["centre"]["unit"], "channels")
        self.assertEqual(s["axes"]["sigma"]["unit"], "channels")
        self.assertEqual(s["velocity_axis"]["kind"], "channel")
        cells = self.cells(s)
        self.assertEqual(cells[(1, 2)][0], 3)                  # the three narrow components on channel index 2 (file mean 3)
        code, r = run("rohsa_classmaps", self.crop, self.dat, self.dir / "m.fits", weight="count", edges=[2.0])
        self.assertEqual(code, 2)
        self.assertIn("not a resolved velocity axis", r["error"])

    def test_bins_are_validated(self):
        for bad in ({"min": 1, "max": 1, "n": 4}, {"min": 0, "max": 4, "n": 0}, {"min": 0, "max": 4, "n": gr.MAX_BINS + 1}, {"min": 0, "max": 4}, {"min": "a", "max": 4, "n": 2}):
            code, r = self.sigmav(sigma_bins=bad)
            self.assertEqual(code, 2, bad)
            self.assertFalse(r["ok"])

    def test_the_dat_is_named_by_size_and_time_never_by_folder(self):
        _, r = self.sigmav()
        d = r["summary"]["dat"]
        self.assertEqual(d["name"], "rohsa_n2.dat")
        self.assertEqual(d["size_bytes"], self.dat.stat().st_size)
        self.assertNotIn(str(self.dir), json.dumps(r["summary"]))


class Mask(Base):
    def setUp(self):
        super().setUp()
        code, r = self.sigmav()                  # the diagram writes the index a cell click reads
        self.assertEqual(code, 0, r)
        self.drawn = r["summary"]

    def cell(self, s, v, **kw):
        kw.setdefault("cache_dir", self.cache)
        return run("rohsa_cellmask", self.crop, self.dat, velocity_bins=V_BINS, sigma_bins=S_BINS, cell={"s": s, "v": v}, **kw)

    def sidecars(self):
        return sorted(Path(self.cache).glob("*.cells"))

    def test_the_index_is_two_bytes_per_row_and_stated_in_the_diagram(self):
        rows = NX * NY * N
        self.assertEqual(self.drawn["pixel_index"], {"bytes": 2 * rows, "rows": rows})
        (f,) = self.sidecars()
        self.assertEqual(f.stat().st_size, 2 * rows)
        self.assertNotIn(self.cache, json.dumps(self.drawn))

    def test_the_mask_from_the_index_equals_the_streaming_mask_for_every_cell(self):
        sc = gr.Scales(NC, -12.0, 1.0, {}, "K", "file")
        ve, se = gr.linear_edges(V_BINS, "centre"), gr.linear_edges(S_BINS, "width")
        for s in range(4):
            for v in range(5):
                code, r = self.cell(s, v)
                self.assertEqual(code, 0, r)
                ref = gr.cell_mask_stream(str(self.dat), NX, NY, N, sc, ve, se, s, v)
                self.assertEqual(r["summary"]["mask"], ref["mask"], (s, v))
                self.assertEqual((r["summary"]["pixels"], r["summary"]["components"]), (ref["pixels"], ref["components"]))

    def test_a_click_does_not_read_the_dat(self):
        sc = gr.Scales(NC, -12.0, 1.0, {}, "K", "file")
        ve, se = gr.linear_edges(V_BINS, "centre"), gr.linear_edges(S_BINS, "width")
        orig = gr.stream_components
        gr.stream_components = lambda *a, **k: self.fail("the .dat was read")
        try:
            r = gr.cell_mask(str(self.dat), NX, NY, N, sc, ve, se, 1, 1, cache_dir=self.cache)
        finally:
            gr.stream_components = orig
        self.assertEqual((r["pixels"], r["components"]), (2, 3))

    def test_no_index_is_a_refusal_that_says_to_draw_again(self):
        for f in self.sidecars():
            f.unlink()
        code, r = self.cell(1, 1)
        self.assertEqual(code, 2)
        self.assertIn("draw the diagram again", r["error"])

    def test_other_bins_or_a_changed_dat_find_no_index(self):
        code, r = run("rohsa_cellmask", self.crop, self.dat, velocity_bins={"min": -12, "max": 0, "n": 4}, sigma_bins=S_BINS, cell={"s": 1, "v": 1}, cache_dir=self.cache)
        self.assertEqual(code, 2)
        self.assertIn("draw the diagram again", r["error"])
        st = self.dat.stat()
        os.utime(self.dat, ns=(st.st_atime_ns, st.st_mtime_ns + 5_000_000_000))
        code, r = self.cell(1, 1)
        self.assertEqual(code, 2)
        self.assertIn("draw the diagram again", r["error"])

    def test_a_truncated_index_is_refused(self):
        (f,) = self.sidecars()
        f.write_bytes(f.read_bytes()[:-2])
        code, r = self.cell(1, 1)
        self.assertEqual(code, 2)
        self.assertIn("wrong size", r["error"])

    def test_only_the_latest_binning_of_a_fit_is_kept(self):
        (first,) = self.sidecars()
        code, r = self.sigmav(velocity_bins={"min": -12, "max": 0, "n": 4})
        self.assertEqual(code, 0, r)
        (second,) = self.sidecars()
        self.assertNotEqual(first, second)
        code, r = self.cell(1, 1)                # the first bins are gone: say so
        self.assertEqual(code, 2)

    def test_a_linked_sidecar_is_refused_by_the_cell_and_by_the_diagram_and_never_written_through(self):
        (f,) = self.sidecars()
        victim = self.dir / "victim"
        victim.write_bytes(b"keep")
        f.unlink()
        f.symlink_to(victim)
        code, r = self.cell(1, 1)
        self.assertEqual(code, 2)
        self.assertIn("not a plain file", r["error"])
        code, r = self.sigmav()
        self.assertEqual(code, 2)
        self.assertEqual(victim.read_bytes(), b"keep")
        f.unlink()
        tmp = Path(str(f) + ".tmp")
        tmp.symlink_to(victim)
        code, r = self.sigmav()
        self.assertEqual(code, 2)
        self.assertEqual(victim.read_bytes(), b"keep")

    def test_the_index_is_never_written_next_to_the_data(self):
        code, r = self.sigmav(cache_dir=str(self.dir))
        self.assertEqual(code, 2)
        self.assertIn("next to the data", r["error"])
        self.assertEqual(list(self.dir.glob("*.cells*")), [])

    def test_a_refused_pass_leaves_no_index(self):
        for f in self.sidecars():
            f.unlink()
        bad = self.dir / "bad.dat"
        bad.write_text(self.dat.read_text() + "1 1 1 1 1\n")   # one row too many
        code, r = run("rohsa_sigmav", self.crop, bad, velocity_bins=V_BINS, sigma_bins=S_BINS, weight="integrated", cache_dir=self.cache)
        self.assertEqual(code, 2)
        self.assertEqual(list(Path(self.cache).iterdir()), [])

    def test_the_mask_is_the_pixels_with_a_component_in_the_cell_on_a_non_square_crop(self):
        code, r = self.cell(1, 1)
        self.assertEqual(code, 0, r)
        m = r["summary"]["mask"]
        self.assertEqual((m["width"], m["height"]), (NX, NY))
        got = gr.unpack_mask(m)
        self.assertEqual(got.shape, (NY, NX))
        want = np.zeros((NY, NX), dtype=bool)
        want[0, 0] = want[1, 0] = True           # components 1, 7 and 8 (7 and 8 share pixel (1, 0))
        np.testing.assert_array_equal(got, want)
        self.assertEqual(r["summary"]["pixels"], 2)
        self.assertEqual(r["summary"]["components"], 3)

    def test_the_mask_agrees_with_the_diagram_for_every_cell(self):
        _, d = self.sigmav()
        counts = {(c[0], c[1]): c[2] for c in d["summary"]["cells"]}
        for s in range(4):
            for v in range(5):
                code, r = self.cell(s, v)
                self.assertEqual(code, 0)
                self.assertEqual(r["summary"]["components"], counts.get((s, v), 0), (s, v))
                self.assertEqual(int(gr.unpack_mask(r["summary"]["mask"]).sum()), r["summary"]["pixels"])

    def test_an_invalid_component_never_marks_a_pixel(self):
        code, r = self.cell(0, 1)               # widths below 1: the invalid sigma 0 and -1 components are not there
        self.assertEqual(r["summary"]["pixels"], 0)
        self.assertFalse(gr.unpack_mask(r["summary"]["mask"]).any())

    def test_a_cell_outside_the_diagram_is_refused(self):
        code, r = self.cell(4, 0)
        self.assertEqual(code, 2)
        code, r = self.cell(0, -1)
        self.assertEqual(code, 2)

    def test_mask_round_trip_on_an_odd_size(self):
        rng = np.random.default_rng(1)
        hit = rng.random(7 * 5) < 0.4
        back = gr.unpack_mask(gr.pack_mask(hit, 7, 5))
        np.testing.assert_array_equal(back.ravel(), hit)


class ClassMaps(Base):
    def maps(self, weight="integrated", edges=(2.0,), **kw):
        out = self.dir / "res.fits"
        code, r = run("rohsa_classmaps", self.crop, self.dat, out, weight=weight, edges=list(edges), **kw)
        return code, r, [fits.getdata(self.dir / f"res-class{i}.fits") for i in range(len(edges) + 1)] if code == 0 else None

    def test_class_maps_sum_to_the_hand_calculation_with_nan_only_where_nothing_is_valid(self):
        code, r, (narrow, rest) = self.maps()
        self.assertEqual(code, 0, r)
        nan = np.nan
        np.testing.assert_allclose(narrow, np.array([[2.0 * S, 0.0, 0.0], [3.5 * S, nan, 2.0 * S]]), rtol=1e-6)
        np.testing.assert_allclose(rest, np.array([[3.0 * S, 8.0 * S, 7.5 * S], [0.0, nan, 0.0]]), rtol=1e-6)
        s = r["summary"]
        self.assertEqual(s["pixels_without_valid_component"], 1)
        self.assertEqual([c["components"] for c in s["classes"]], [6, 3])
        self.assertAlmostEqual(sum(c["weight_total"] for c in s["classes"]), 26.0 * S, places=12)
        # the classes together are the whole decomposition: the diagram's total
        _, d = Sigmav.sigmav(self)
        self.assertAlmostEqual(sum(c["weight_total"] for c in s["classes"]), d["summary"]["weight_total"], places=12)

    def test_a_width_equal_to_the_cut_is_in_the_rest_not_the_narrow_class(self):
        _, _, (narrow, rest) = self.maps()
        self.assertEqual(narrow[0, 1], 0.0)                       # pixel (0, 1): the width-2.0 component is not narrow; its narrow one has amp 0
        self.assertAlmostEqual(float(rest[0, 1]), 8.0 * S, places=5)

    def test_three_classes_from_two_edges_and_a_header_that_says_what_it_is(self):
        code, r, (a, b, c) = self.maps(edges=(1.5, 2.5))
        self.assertEqual(code, 0, r)
        labels = [x["label"] for x in r["summary"]["classes"]]
        self.assertEqual(labels, ["sigma < 1.5 km/s", "1.5 <= sigma < 2.5 km/s", "sigma >= 2.5 km/s"])
        h = fits.getheader(self.dir / "res-class1.fits")
        self.assertEqual(h["BUNIT"], "K km/s")
        self.assertEqual(h["VESQOP"], "rohsa_class")
        self.assertEqual((h["NAXIS1"], h["NAXIS2"]), (NX, NY))
        self.assertEqual(h["CTYPE1"], "RA---TAN")                  # the crop's sky grid, no spectral axis
        self.assertNotIn("CTYPE3", h)
        np.testing.assert_allclose(b, np.array([[0.0, 8.0 * S, 0.0], [0.0, np.nan, 0.0]]), rtol=1e-6, equal_nan=True)

    def test_count_maps_have_no_flux_unit(self):
        code, r, (narrow, rest) = self.maps(weight="count")
        self.assertEqual(code, 0)
        np.testing.assert_array_equal(narrow, np.array([[1, 1, 0], [2, np.nan, 2]]))
        self.assertEqual(fits.getheader(self.dir / "res-class0.fits")["BUNIT"], "components")

    def test_unknown_unit_refuses_and_writes_nothing(self):
        write_crop(self.crop, bunit=None)
        code, r, _ = self.maps()
        self.assertEqual(code, 2)
        self.assertIn("no brightness unit", r["error"])
        self.assertEqual(list(self.dir.glob("res-class*.fits")), [])

    def test_bad_edges_are_refused(self):
        for bad in ([], [0.0], [2.0, 1.0], [2.0, 2.0], [-1.0], list(range(1, 12))):
            code, r, _ = self.maps(edges=bad)
            self.assertEqual(code, 2, bad)


class Stream(Base):
    def read(self, **kw):
        return list(gr.stream_components(str(self.dat), NX, NY, N, **kw))

    def test_chunks_cut_in_the_middle_of_a_row_give_the_same_rows(self):
        whole = np.concatenate([np.stack(c) for c in self.read()], axis=1)
        old = gr.CHUNK_BYTES
        gr.CHUNK_BYTES = 211
        try:
            parts = self.read()
        finally:
            gr.CHUNK_BYTES = old
        self.assertGreater(len(parts), 3)
        np.testing.assert_array_equal(np.concatenate([np.stack(c) for c in parts], axis=1), whole)
        self.assertEqual(whole.shape[1], NX * NY * N)

    def test_the_pixel_of_a_row_is_read_from_its_own_columns(self):
        x, y, amp, mean, sig = (np.concatenate(a) for a in zip(*self.read()))
        self.assertEqual(list(zip(y[:4], x[:4])), [(0, 0), (0, 0), (0, 1), (0, 1)])
        self.assertEqual(float(amp[-1]), 1.0)

    def test_a_layout_that_is_not_the_regular_one_is_refused(self):
        lines = self.dat.read_text().splitlines()
        swapped = lines[:3] + [lines[4], lines[3]] + lines[5:]   # rows of two different pixels: the Gaussian index within a pixel is not in the file
        self.dat.write_text("\n".join(swapped) + "\n")
        with self.assertRaises(gr.Refusal):
            self.read()
        self.dat.write_text("\n".join(lines[:-1]) + "\n")                  # one row short
        with self.assertRaises(gr.Refusal):
            self.read()
        self.dat.write_text("\n".join(lines + [lines[-1]]) + "\n")        # one row too many
        with self.assertRaises(gr.Refusal):
            self.read()
        text = list(lines)
        text[3] = text[3][:-5] + "  nan"
        self.dat.write_text("\n".join(text) + "\n")
        with self.assertRaises(gr.Refusal):
            self.read()

    def test_a_fit_that_takes_too_long_is_stopped_with_a_sentence(self):
        with self.assertRaises(gr.Refusal) as e:
            self.read(max_seconds=-1.0)
        self.assertIn("too large for one pass", str(e.exception))

    def test_bin_index_is_half_open_and_marks_the_outside(self):
        edges = np.array([0.0, 1.0, 2.0])
        got = gr.bin_index(np.array([-0.1, 0.0, 0.999, 1.0, 1.999, 2.0, np.nan]), edges)
        np.testing.assert_array_equal(got, [-1, 0, 0, 1, 1, -1, -1])
        np.testing.assert_array_equal(gr.class_index(np.array([0.5, 2.03, 2.0299, 9.0]), np.array([2.03])), [0, 1, 0, 1])


if __name__ == "__main__":
    unittest.main()
