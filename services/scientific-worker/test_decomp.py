"""The decomposition record (decomp.py, ADR 0068 slice 4), checked on a tool that is neither VGT nor ROHSA.

The fixture is fixtures/generate.py write_synth_decomp_run: a synthetic tool whose fit is FITS maps (amplitude in K from BUNIT, centre in
m/s from BUNIT, width a FWHM in km/s that the profile declares, the third component slot NaN where (x + y) % 4 == 0). The expected values are
computed here from the closed-form truth arrays and plain loops, never through the reader under test. The ROHSA entry of the shipped
profile file must declare exactly what the worker's built-in reading of a .dat does.
"""
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

import numpy as np
from astropy.io import fits

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parent.parent / "fixtures"))
sys.path.insert(0, str(HERE))
import decomp  # noqa: E402
import generate as g  # noqa: E402
from test_plane_worker import Client  # noqa: E402

SCRIPT = str(HERE / "analysis.py")
FWHM = 2.0 * np.sqrt(2.0 * np.log(2.0))
S2PI = float(np.sqrt(2 * np.pi))
CUBES = {"source": {"kind": "fits_maps", "maps": {p: {"hdu": 0} for p in ("amplitude", "centre", "width")}, "componentAxis": 3},
         "amplitude": {}, "centre": {"frame": "LSRK"}, "width": {"kind": "fwhm", "unit": "km/s"},
         "order": "slot index along the component axis, as the tool numbers them"}
CUBE_FILES = {"amplitude": ["synth_decomp_amp.fits"], "centre": ["synth_decomp_centre.fits"], "width": ["synth_decomp_fwhm.fits"]}
EACH = {**CUBES, "source": {"kind": "fits_maps", "maps": CUBES["source"]["maps"], "componentAxis": None}}
EACH_FILES = {p: [f"synth_decomp_{n}_g{k}.fits" for k in (1, 2, 3)] for p, n in (("amplitude", "amp"), ("centre", "centre"), ("width", "fwhm"))}
V_BINS = {"min": -20.0, "max": 30.0, "n": 10}
S_BINS = {"min": 0.0, "max": 4.0, "n": 8}


def sigma_kms(t):
    return t["fwhm_kms"] / FWHM


class Base(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        cls.dir = Path(cls.tmp.name) / "run"
        g.write_synth_decomp_run(cls.dir)
        cls.cache = tempfile.TemporaryDirectory()
        cls.t = g.decomp_truth()
        cls.c = Client()

    @classmethod
    def tearDownClass(cls):
        cls.c.close()
        cls.tmp.cleanup()
        cls.cache.cleanup()

    def pixel(self, x, y, decl=CUBES, files=CUBE_FILES, folder=None):
        folder = folder or self.dir
        head, payload = self.c.ask(op="rohsa_pixel", path=str(folder / "synth_cube.fits"), dat_path=str(folder), cache_dir=self.cache.name,
                                   n_gauss=g.DN, x=x, y=y, decomposition={"declaration": decl, "files": files})
        return head, payload

    def analysis(self, op, decl=CUBES, files=CUBE_FILES, folder=None, out=None, **params):
        folder = folder or self.dir
        p = {"dat_path": str(folder), "n_gauss": g.DN, "decomposition": {"declaration": decl, "files": files}, "cache_dir": self.cache.name, **params}
        args = [sys.executable, "-P", SCRIPT, op, "--input", str(folder / "synth_cube.fits"), "--params", json.dumps(p)]
        if out:
            args += ["--out", str(out)]
        r = subprocess.run(args, capture_output=True, text=True, timeout=120)
        return r.returncode, json.loads(r.stdout)


class Components(Base):
    def test_the_components_of_a_pixel_equal_the_closed_form(self):
        x, y = 3, 2
        head, payload = self.pixel(x, y)
        self.assertTrue(head["ok"], head)
        self.assertTrue(head["drawn"])
        v = g.D_V0_KMS + g.D_DV_KMS * np.arange(g.DC)
        curves = np.frombuffer(payload, dtype="<f8")[(2) * g.DC:].reshape(g.DN, g.DC)
        for k, comp in enumerate(head["components"]):
            a, c, s = self.t["amp"][k, y, x], self.t["centre_kms"][k, y, x], self.t["fwhm_kms"][k, y, x] / FWHM
            self.assertTrue(comp["present"] and comp["valid"])
            self.assertAlmostEqual(comp["amp"], a, places=12)
            self.assertAlmostEqual(comp["centre"], c, places=9)
            self.assertAlmostEqual(comp["sigma"], s, places=9)                    # the FWHM was divided by 2 sqrt(2 ln 2)
            self.assertAlmostEqual(comp["fwhm"], s * FWHM, places=9)
            np.testing.assert_allclose(curves[k], a * np.exp(-((v - c) ** 2) / (2 * s * s)), rtol=1e-9, atol=1e-12)
            self.assertAlmostEqual(comp["centre_raw"], c * 1000.0, places=6)      # what the file says, in its own unit (m/s)
            self.assertIsNone(comp["mean_channel_file"])                          # that field is the file's channel number; this centre is not one
        model = np.frombuffer(payload, dtype="<f8")[g.DC:2 * g.DC]
        np.testing.assert_allclose(model, curves.sum(axis=0), rtol=1e-12)

    def test_the_record_says_where_each_unit_came_from(self):
        head, _ = self.pixel(3, 2)
        r = head["record"]
        self.assertEqual((r["amplitude"]["unit"], r["amplitude"]["basis"], r["amplitude"]["matches_grid"]), ("K", "file", True))   # BUNIT of the amplitude map
        self.assertEqual((r["centre"]["unit"], r["centre"]["frame"], r["centre"]["status"]), ("m/s", "LSRK", "resolved"))           # BUNIT of the centre map
        self.assertEqual((r["width"]["kind"], r["width"]["unit"], r["width"]["status"]), ("fwhm", "km/s", "resolved"))            # declared: the map has no BUNIT
        self.assertIn("2.3548", r["width"]["to_sigma"])
        self.assertEqual(head["bunit"], "K")
        self.assertNotIn("t_doppler_k", r)                                        # derived quantities stay outside the record

    def test_a_slot_the_tool_did_not_fit_is_absent_not_zero(self):
        x, y = 1, 3                                                               # (1 + 3) % 4 == 0: the third slot is NaN
        head, payload = self.pixel(x, y)
        third = head["components"][2]
        self.assertFalse(third["present"])
        self.assertFalse(third["valid"])
        self.assertIsNone(third["amp"])
        self.assertIsNone(third["centre"])
        curves = np.frombuffer(payload, dtype="<f8")[2 * g.DC:].reshape(g.DN, g.DC)
        self.assertFalse(curves[2].any())                                         # not drawn: all zeros are the absence of a curve, flagged by present
        self.assertTrue(head["components"][0]["present"] and head["components"][1]["present"])

    def test_one_map_per_component_gives_the_same_components(self):
        for x, y in ((0, 0), (3, 2), (6, 4)):
            a, pa = self.pixel(x, y)
            b, pb = self.pixel(x, y, EACH, EACH_FILES, self.dir / "per_component")
            self.assertEqual(a["components"], b["components"])
            self.assertEqual(pa, pb)

    def test_an_unresolved_field_is_listed_not_computed(self):
        for edit, words in (
            (lambda d: d["centre"].pop("frame"), "frame is not declared"),
            (lambda d: d["centre"].update(frame="BARY"), "not the same frame"),
            (lambda d: d["width"].update(kind=None), "convention (sigma, FWHM or dispersion) is not declared"),
            (lambda d: d["width"].update(unit=None), "unit is not declared"),
        ):
            decl = json.loads(json.dumps(CUBES))
            edit(decl)
            head, payload = self.pixel(3, 2, decl)
            self.assertTrue(head["ok"], head)
            self.assertFalse(head["drawn"])
            self.assertIn(words, head["not_drawn_reason"])
            comp = head["components"][0]
            self.assertFalse(comp["valid"])
            self.assertIsNone(comp["centre"])
            self.assertIsNone(comp["sigma"])
            self.assertAlmostEqual(comp["amp"], self.t["amp"][0, 2, 3], places=12)   # the amplitude is still shown: it needed none of the missing facts
            self.assertFalse(np.frombuffer(payload, dtype="<f8")[2 * g.DC:].any())
            self.assertEqual(head["record"]["centre"]["status"] if "frame" in str(words) or "frame" in words else head["record"]["width"]["status"], "unresolved")

    def test_a_channel_centre_needs_its_declared_base(self):
        # the same numbers read as channel numbers: with no declared base nothing is computed, with one the base is subtracted and nothing else
        decl = json.loads(json.dumps(CUBES))
        decl["centre"] = {"unit": "channel"}
        head, _ = self.pixel(3, 2, decl)
        self.assertFalse(head["drawn"])
        self.assertIn("index base", head["not_drawn_reason"])
        decl["centre"] = {"unit": "channel", "base": 1}
        decl["width"] = {"kind": "sigma", "unit": "channel"}
        head, _ = self.pixel(3, 2, decl)
        self.assertTrue(head["drawn"], head)
        self.assertAlmostEqual(head["components"][0]["mean_channel"], self.t["centre_kms"][0, 2, 3] * 1000.0 - 1.0, places=6)

    def test_maps_that_are_not_the_declared_shape_are_refused_before_any_read(self):
        decl = json.loads(json.dumps(CUBES))
        decl["source"]["componentAxis"] = 1
        head, _ = self.pixel(0, 0, decl)
        self.assertFalse(head["ok"])
        self.assertIn("axes", head["error"])

    def test_a_map_on_another_sky_grid_is_refused(self):
        other = Path(self.tmp.name) / "other"
        g.write_synth_decomp_run(other)
        with fits.open(other / "synth_decomp_amp.fits", mode="update") as h:
            h[0].header["CRVAL1"] = 151.0
        head, _ = self.pixel(0, 0, folder=other)
        self.assertFalse(head["ok"])
        self.assertIn("sky grid", head["error"])

    def test_a_file_outside_the_runs_folder_is_refused(self):
        head, _ = self.pixel(0, 0, files={**CUBE_FILES, "amplitude": ["../synth_decomp_amp.fits"]})
        self.assertFalse(head["ok"])

    def test_the_index_route_is_ready_after_the_header_checks(self):
        head, _ = self.c.ask(op="rohsa_index", path=str(self.dir / "synth_cube.fits"), dat_path=str(self.dir), cache_dir=self.cache.name, n_gauss=g.DN,
                             decomposition={"declaration": CUBES, "files": CUBE_FILES})
        self.assertEqual(head["state"], "ready")


class Review(Base):
    def expected(self, weight):
        """Cells of the sigma-v diagram by plain loops over the closed-form arrays: {(width bin, centre bin): (count, weight)}."""
        cells, absent = {}, 0
        for k in range(g.DN):
            for y in range(g.DY):
                for x in range(g.DX):
                    a, c, s = self.t["amp"][k, y, x], self.t["centre_kms"][k, y, x], self.t["fwhm_kms"][k, y, x] / FWHM
                    if np.isnan(a):
                        absent += 1
                        continue
                    iv, js = int(np.floor((c - V_BINS["min"]) / 5.0)), int(np.floor((s - S_BINS["min"]) / 0.5))
                    if 0 <= iv < V_BINS["n"] and 0 <= js < S_BINS["n"]:
                        w = 1.0 if weight == "count" else a * s * S2PI
                        n0, w0 = cells.get((js, iv), (0, 0.0))
                        cells[(js, iv)] = (n0 + 1, w0 + w)
        return cells, absent

    def test_the_sigma_v_diagram_equals_a_plain_loop_over_the_truth(self):
        for weight in ("count", "integrated"):
            code, r = self.analysis("rohsa_sigmav", velocity_bins=V_BINS, sigma_bins=S_BINS, weight=weight)
            self.assertEqual(code, 0, r)
            s = r["summary"]
            want, absent = self.expected(weight)
            got = {(c[0], c[1]): (c[2], c[3]) for c in s["cells"]}
            self.assertEqual(set(got), set(want))
            for key, (n, w) in want.items():
                self.assertEqual(got[key][0], n, key)
                self.assertAlmostEqual(got[key][1], w, places=9, msg=str(key))
            self.assertEqual(s["not_fitted"], absent)
            self.assertEqual(s["components"], g.DN * g.DX * g.DY - absent)           # an absent slot is not a component, so it is not counted as one
            self.assertEqual(s["invalid_sigma"], 0)
            self.assertEqual(s["weight_unit"], "components" if weight == "count" else "K km/s")   # the unit of the amplitude map's BUNIT
            self.assertEqual(s["record"]["centre"]["frame"], "LSRK")
            self.assertEqual(s["dat"]["name"], "synth_decomp_amp.fits, synth_decomp_centre.fits, synth_decomp_fwhm.fits")

    def test_class_maps_equal_the_per_pixel_sums_and_the_cut_is_half_open(self):
        out = Path(self.tmp.name) / "classes.fits"
        code, r = self.analysis("rohsa_classmaps", out=out, edges=[2.03], weight="integrated")
        self.assertEqual(code, 0, r)
        want = np.zeros((2, g.DY, g.DX))
        for k in range(g.DN):
            for y in range(g.DY):
                for x in range(g.DX):
                    a, s = self.t["amp"][k, y, x], self.t["fwhm_kms"][k, y, x] / FWHM
                    if not np.isnan(a):
                        want[0 if s < 2.03 else 1, y, x] += a * s * S2PI
        for i in (0, 1):
            with fits.open(Path(self.tmp.name) / f"classes-class{i}.fits") as h:
                np.testing.assert_allclose(h[0].data, want[i], rtol=1e-6)            # written as float32
                self.assertEqual(h[0].header["BUNIT"], "K km/s")
        self.assertEqual(r["summary"]["not_fitted"], int(np.isnan(self.t["amp"]).sum()))
        self.assertEqual(sum(c["components"] for c in r["summary"]["classes"]), int(np.isfinite(self.t["amp"]).sum()))

    def test_the_pixels_behind_a_cell_are_the_pixels_with_a_component_in_it(self):
        code, r = self.analysis("rohsa_sigmav", velocity_bins=V_BINS, sigma_bins=S_BINS, weight="count")
        self.assertEqual(code, 0, r)
        want, _ = self.expected("count")
        (js, iv), (n, _) = sorted(want.items())[0]
        code, m = self.analysis("rohsa_cellmask", velocity_bins=V_BINS, sigma_bins=S_BINS, cell={"s": js, "v": iv})
        self.assertEqual(code, 0, m)
        pixels = set()
        for k in range(g.DN):
            for y in range(g.DY):
                for x in range(g.DX):
                    a, c, s = self.t["amp"][k, y, x], self.t["centre_kms"][k, y, x], self.t["fwhm_kms"][k, y, x] / FWHM
                    if not np.isnan(a) and int(np.floor((c + 20.0) / 5.0)) == iv and int(np.floor(s / 0.5)) == js:
                        pixels.add((y, x))
        import gaussreview as gr
        mask = gr.unpack_mask(m["summary"]["mask"])
        self.assertEqual({(int(y), int(x)) for y, x in zip(*np.nonzero(mask))}, pixels)
        self.assertEqual(m["summary"]["components"], n)

    def test_per_component_maps_give_the_same_diagram(self):
        a = self.analysis("rohsa_sigmav", velocity_bins=V_BINS, sigma_bins=S_BINS)[1]["summary"]
        b = self.analysis("rohsa_sigmav", decl=EACH, files=EACH_FILES, folder=self.dir / "per_component", velocity_bins=V_BINS, sigma_bins=S_BINS)[1]["summary"]
        self.assertEqual(a["cells"], b["cells"])
        self.assertEqual((a["components"], a["not_fitted"]), (b["components"], b["not_fitted"]))

    def test_an_unresolved_declaration_is_refused_with_its_sentence(self):
        decl = json.loads(json.dumps(CUBES))
        decl["centre"].pop("frame")
        code, r = self.analysis("rohsa_sigmav", decl=decl, velocity_bins=V_BINS, sigma_bins=S_BINS)
        self.assertNotEqual(code, 0)
        self.assertIn("frame is not declared", json.dumps(r))

    def test_a_declared_amplitude_unit_that_is_not_the_cubes_is_carried_not_swapped(self):
        decl = json.loads(json.dumps(CUBES))
        # the amplitude map's own BUNIT (K) wins over a different declared unit; with a note
        decl["amplitude"] = {"unit": "Jy/beam"}
        head, _ = self.pixel(3, 2, decl)
        self.assertEqual(head["record"]["amplitude"]["unit"], "K")
        self.assertTrue(any("BUNIT" in n for n in head["record"]["notes"]))


NAMED = {**CUBES, "source": {"kind": "fits_maps", "componentAxis": 3,
                             "maps": {"amplitude": {"hdu": "AMP", "extver": 1}, "centre": {"hdu": "CENTRE"}, "width": {"hdu": "fwhm"}}}}
NAMED_FILES = {p: ["synth_decomp_ext.fits"] for p in ("amplitude", "centre", "width")}


class NamedHdus(Base):
    """The three maps are HDUs of one file found by EXTNAME (and EXTVER). The file holds a decoy AMP HDU (EXTVER 2, ten times the
    amplitudes) ahead of the real one, so reading the first HDU of that name would give the wrong numbers."""

    @classmethod
    def setUpClass(cls):
        super().setUpClass()
        cls.named = cls.dir / "named_hdus"

    def named_pixel(self, x, y, decl=NAMED, files=NAMED_FILES):
        return self.pixel(x, y, decl, files, self.named)

    def test_a_name_and_an_extver_pick_the_hdu_and_the_numbers_are_the_closed_form(self):
        for x, y in ((0, 0), (3, 2), (6, 4)):
            head, payload = self.named_pixel(x, y)
            self.assertTrue(head["ok"] and head["drawn"], head)
            for k, comp in enumerate(head["components"]):
                a = self.t["amp"][k, y, x]
                if np.isnan(a):
                    self.assertFalse(comp["present"])
                    continue
                self.assertAlmostEqual(comp["amp"], a, places=12)                        # AMP EXTVER 1, not the decoy (10 x)
                self.assertAlmostEqual(comp["centre"], self.t["centre_kms"][k, y, x], places=9)
                self.assertAlmostEqual(comp["sigma"], self.t["fwhm_kms"][k, y, x] / FWHM, places=9)
            ref, pref = self.pixel(x, y)                                                 # the same fit as separate files by number
            self.assertEqual(head["components"], ref["components"])
            self.assertEqual(payload, pref)

    def test_the_decoy_version_is_read_when_it_is_the_one_declared(self):
        decl = json.loads(json.dumps(NAMED))
        decl["source"]["maps"]["amplitude"]["extver"] = 2
        head, _ = self.named_pixel(3, 2, decl)
        self.assertAlmostEqual(head["components"][0]["amp"], 10.0 * self.t["amp"][0, 2, 3], places=11)

    def test_the_review_reads_named_hdus_the_same_way(self):
        a = self.analysis("rohsa_sigmav", velocity_bins=V_BINS, sigma_bins=S_BINS)[1]["summary"]
        code, r = self.analysis("rohsa_sigmav", decl=NAMED, files=NAMED_FILES, folder=self.named, velocity_bins=V_BINS, sigma_bins=S_BINS)
        self.assertEqual(code, 0, r)
        self.assertEqual(a["cells"], r["summary"]["cells"])

    def refused(self, edit, words):
        decl = json.loads(json.dumps(NAMED))
        edit(decl["source"]["maps"])
        head, _ = self.named_pixel(3, 2, decl)
        self.assertFalse(head["ok"], head)
        self.assertIn(words, head["error"])
        return head["error"]

    def test_a_name_that_two_hdus_carry_is_refused_not_resolved_to_the_first(self):
        msg = self.refused(lambda m: m["amplitude"].pop("extver"), "2 HDUs named 'AMP'")
        self.assertIn("EXTVER 2", msg)
        self.assertIn("EXTVER 1", msg)

    def test_a_missing_name_or_version_is_refused(self):
        self.refused(lambda m: m["centre"].update(hdu="VELOCITY"), "no HDU named 'VELOCITY'")
        self.refused(lambda m: m["amplitude"].update(extver=3), "no HDU named 'AMP' with EXTVER 3")
        self.refused(lambda m: m["centre"].update(extver=2), "no HDU named 'CENTRE' with EXTVER 2")     # an absent EXTVER card counts as 1, not as any
        self.refused(lambda m: m["amplitude"].update(hdu=9, extver=None), "no HDU number 9")

    def test_an_hdu_that_is_not_an_image_is_refused(self):
        self.refused(lambda m: m["amplitude"].update(hdu="AMPTAB", extver=None), "is not an image")

    def test_the_component_axis_is_never_reordered_to_the_declared_one(self):
        decl = json.loads(json.dumps(NAMED))
        decl["source"]["componentAxis"] = 1
        head, _ = self.named_pixel(3, 2, decl)
        self.assertFalse(head["ok"])
        self.assertIn("axes", head["error"])

    def test_extver_is_only_valid_with_a_name(self):
        for bad in ({"hdu": 1, "extver": 1}, {"hdu": "AMP", "extver": 0}, {"hdu": "AMP", "extver": "1"}, {"hdu": "AMP", "extver": True}):
            decl = json.loads(json.dumps(NAMED))
            decl["source"]["maps"]["amplitude"] = bad
            with self.assertRaises(decomp.Bad):
                decomp.parse(decl)


class PixelIndex(Base):
    """A click on a cell of the diagram reads the pixel index the diagram wrote, for maps as for a .dat: no map is opened, and a map that
    changed since the diagram was drawn is a refusal that says to draw it again (the index's key holds every map's path, size and time)."""

    def setUp(self):
        import gaussreview as gr
        from plane_worker import cube_for, decomposition_grid
        self.gr = gr
        self.folder = Path(self.tmp.name) / "indexed"
        g.write_synth_decomp_run(self.folder)
        self.cache_dir = tempfile.mkdtemp(dir=self.cache.name)
        c = cube_for(str(self.folder / "synth_cube.fits"))
        self.grid, _, _, axis = decomposition_grid(c)
        self.sc = gr.Scales(c.channels, self.grid.w0, self.grid.step, axis, "K", "crop", self.grid.specsys)
        self.nx, self.ny = c.shape[c.x], c.shape[c.y]
        self.c_hdr = c.h
        self.v, self.s = gr.linear_edges(V_BINS, "centre"), gr.linear_edges(S_BINS, "width")

    def open(self):
        return decomp.open_decomposition({"decomposition": {"declaration": CUBES, "files": CUBE_FILES}}, self.grid, self.nx, self.ny, g.DN,
                                         str(self.folder), lambda p: p, self.c_hdr)

    def test_a_cell_click_opens_no_map_and_equals_the_full_read(self):
        gr = self.gr
        gr.sigmav(self.open(), self.nx, self.ny, g.DN, self.sc, "count", self.v, self.s, cache_dir=self.cache_dir)
        want, _ = Review.expected(self, "count")
        reads, original = [0], decomp.MapsSource._read
        decomp.MapsSource._read = lambda me, *a: (reads.__setitem__(0, reads[0] + 1), original(me, *a))[1]
        try:
            for (js, iv) in sorted(want)[:3]:
                m = gr.cell_mask(self.open(), self.nx, self.ny, g.DN, self.sc, self.v, self.s, js, iv, self.cache_dir)
                self.assertEqual(reads[0], 0)                                           # the index, not the maps
                self.assertEqual(m["source"], "pixel index written by the diagram")
                ref = gr.cell_mask_stream(self.open(), self.nx, self.ny, g.DN, self.sc, self.v, self.s, js, iv)
                self.assertEqual((m["pixels"], m["components"]), (ref["pixels"], ref["components"]))
                np.testing.assert_array_equal(gr.unpack_mask(m["mask"]), gr.unpack_mask(ref["mask"]))
                self.assertEqual(m["components"], want[(js, iv)][0])
                reads[0] = 0
        finally:
            decomp.MapsSource._read = original

    def test_a_changed_map_invalidates_the_index(self):
        gr = self.gr
        gr.sigmav(self.open(), self.nx, self.ny, g.DN, self.sc, "count", self.v, self.s, cache_dir=self.cache_dir)
        gr.cell_mask(self.open(), self.nx, self.ny, g.DN, self.sc, self.v, self.s, 0, 0, self.cache_dir)
        path = self.folder / "synth_decomp_centre.fits"
        st = path.stat()
        import os
        os.utime(path, ns=(st.st_atime_ns, st.st_mtime_ns + 1_000_000_000))
        with self.assertRaises(gr.Refusal) as cm:
            gr.cell_mask(self.open(), self.nx, self.ny, g.DN, self.sc, self.v, self.s, 0, 0, self.cache_dir)
        self.assertIn("draw the diagram again", str(cm.exception))

    def test_a_different_declaration_is_a_different_index(self):
        gr = self.gr
        gr.sigmav(self.open(), self.nx, self.ny, g.DN, self.sc, "count", self.v, self.s, cache_dir=self.cache_dir)
        other = decomp.open_decomposition({"decomposition": {"declaration": {**CUBES, "width": {"kind": "sigma", "unit": "km/s"}}, "files": CUBE_FILES}},
                                          self.grid, self.nx, self.ny, g.DN, str(self.folder), lambda p: p, self.c_hdr)
        with self.assertRaises(gr.Refusal):
            gr.cell_mask(other, self.nx, self.ny, g.DN, self.sc, self.v, self.s, 0, 0, self.cache_dir)


class Declaration(unittest.TestCase):
    def test_the_shipped_rohsa_profile_declares_what_the_worker_reads_by_default(self):
        data = json.loads((HERE.parent / "api" / "internal" / "runs" / "run_profiles.json").read_text())
        rohsa = next(p for p in data["profiles"] if p["name"] == "ROHSA")
        declared = {k: v for k, v in rohsa["decomposition"].items() if k not in ("tool",)}
        self.assertEqual(decomp.parse(declared), decomp.parse(decomp.ROHSA_DECLARATION))

    def test_unknown_fields_and_wrong_values_are_refused(self):
        for bad in ({**CUBES, "extra": 1}, {**CUBES, "width": {"kind": "variance"}}, {**CUBES, "centre": {"base": 2}},
                    {**CUBES, "source": {**CUBES["source"], "componentAxis": 4}}):
            with self.assertRaises(decomp.Bad):
                decomp.parse(bad)
        table = json.loads(json.dumps(decomp.ROHSA_DECLARATION))
        table["source"]["columns"]["width"] = 3
        with self.assertRaises(decomp.Bad):
            decomp.parse(table)                                                   # two columns the same

    def test_a_declaration_for_a_request_reads_as_rohsa_when_absent(self):
        class Grid:
            nc, w0, step, specsys, bunit, bunit_source = 4, -12.0, 1.0, None, "K", "file"
            velocity, dv = True, 1.0
        d = decomp.coerce("/nonexistent.dat", Grid)
        m0, s = d.normalise(np.array([1.0, 11.0]), np.array([11.0, 3.0]), np.array([1.0, 2.0]))[1:]
        np.testing.assert_array_equal(m0, [10.0, 2.0])                           # 1-based file mean: minus one
        np.testing.assert_array_equal(s, [1.0, 2.0])


if __name__ == "__main__":
    unittest.main()
