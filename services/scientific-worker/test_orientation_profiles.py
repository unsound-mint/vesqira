"""Orientation conventions as data (ADR 0068, item 2): the VGT.jl entry reads what the old hard-coded handling read, a header a profile
cannot place stays unresolved, and a second tool that is not VGT (a HOG-style map with cards of its own) goes through the same reader.

Independence: expected values are written out from VGT.jl's documented cards (PSIFRAME, PSIANGLE, BUNIT: src/pipeline/outputs.jl), not
derived from the table under test. The HOG profile below is defined by this test: its card names are invented for the fixture and
are not any public tool's convention.
"""
import copy
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

from astropy.io import fits

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
sys.path.insert(0, str(HERE.parent.parent / "fixtures"))
import analysis  # noqa: E402
import generate as g  # noqa: E402
import orientation_profiles as op  # noqa: E402

HOG_PROFILE = {
    "name": "hog-fixture", "version": "0.1", "detect": ["HOGFRAME", "HOGANGLE"], "producer_cards": ["HOG[A-Z]{2,5}"],
    "statistics": {"value": "axial"},
    "fields": {
        "frame": {"card": "HOGFRAME", "values": {"IMAGE": {"frame": "pixel", "zero_direction": "+x", "sense": "counter-clockwise"},
                                                  "EQ": {"frame": "sky", "sky_frame": "icrs", "zero_direction": "north", "sense": "east of north"}}},
        "quantity": {"card": "HOGANGLE", "values": {"GRADIENT": "gradient", "ORTHOGONAL": "b_field"}},
        "unit": {"card": "HOGUNIT", "values": {"DEGREES": "deg", "RADIANS": "rad"}},
    },
}


def describe(path):
    p = subprocess.run([sys.executable, "-P", str(HERE / "fits_header.py"), str(path)], capture_output=True, text=True, timeout=20)
    return json.loads(p.stdout)


class Vgt(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        cls.f = g.write_all(Path(cls.tmp.name))

    @classmethod
    def tearDownClass(cls):
        cls.tmp.cleanup()

    def read(self, name):
        return describe(self.f[name])["orientation"]

    def test_each_documented_psiframe_is_placed_and_labelled_as_declared_by_the_profile(self):
        for name, frame, sky in (("vgt_psi_pixel_frame.fits", "pixel", None), ("vgt_psi_galactic_frame.fits", "sky", "galactic"),
                                 ("vgt_psi_icrs_frame.fits", "sky", "icrs")):
            o = self.read(name)
            self.assertEqual((o["frame"], o["sky_frame"], o["quantity"], o["unit"]), (frame, sky, "b_field", "rad"), name)
            self.assertEqual((o["label"], o["statistics"], o["unresolved"], o["notes"]), ("profile:VGT.jl@1", "axial", [], []))
            self.assertEqual(o["from"]["frame"], "PSIFRAME=" + o["cards"]["PSIFRAME"])
            self.assertEqual(set(o["basis"].values()), {"declared"})                       # never "verified"
            self.assertEqual((o["cards"]["PSIFRAME"], o["cards"]["PSIANGLE"], o["cards"]["BUNIT"]), (o["cards"]["PSIFRAME"], "BFIELD", "rad"))
        self.assertEqual(self.read("vgt_psi_pixel_frame.fits")["zero_direction"], "+axis 1")
        self.assertEqual(self.read("vgt_psi_galactic_frame.fits")["sense"], "east of north")

    def test_a_product_without_psiframe_is_unresolved_and_the_wcs_is_not_asked(self):
        o = self.read("vgt_psi_no_frame.fits")                  # a VGT product on an ICRS grid, BUNIT rad, no PSIFRAME or PSIANGLE
        self.assertEqual((o["frame"], o["sky_frame"], o["quantity"]), (None, None, None))
        self.assertEqual((o["unit"], o["unresolved"]), ("rad", ["frame", "quantity"]))     # the unit is the file's own BUNIT
        self.assertEqual(o["basis"], {"unit": "declared"})

    def test_an_unknown_psiframe_is_unresolved_with_a_reason(self):
        o = self.read("vgt_psi_unknown_frame.fits")
        self.assertIsNone(o["frame"])
        self.assertEqual(o["unresolved"], ["frame"])
        self.assertTrue(any("ECLIPTIC" in n and "unresolved" in n for n in o["notes"]), o["notes"])

    def test_a_missing_bunit_is_not_replaced_by_a_unit_the_tool_usually_writes(self):
        h = fits.Header({"VGTPROD": "psi", "PSIFRAME": "PIXEL", "PSIANGLE": "BFIELD"})
        o = op.read(h)
        self.assertEqual((o["frame"], o["quantity"], o["unit"]), ("pixel", "b_field", None))
        self.assertEqual(o["unresolved"], ["unit"])

    def test_a_file_with_none_of_a_profiles_cards_has_no_orientation_block(self):
        self.assertIsNone(self.read("vgt_psi_rad.fits"))
        self.assertIsNone(self.read("hi_cube.fits"))

    def test_the_producer_cards_of_the_table_are_dropped_from_a_derived_header(self):
        h = fits.Header()
        for k, v, c in g.vgt_product_cards("psi", "GALACTIC"):
            h[k] = (v, c)
        h["CTYPE1"] = "RA---TAN"
        analysis.drop_producer_cards(h)
        self.assertEqual([k for k in h.keys()], ["BUNIT", "CTYPE1"])      # BUNIT and the WCS are the data's own, not the producer's


class SecondTool(unittest.TestCase):
    """A HOG-style orientation map: an axial angle in degrees from a gradient histogram, with cards of its own."""

    def header(self, **cards):
        h = fits.Header()
        for k, v in {"HOGFRAME": "IMAGE", "HOGANGLE": "GRADIENT", "HOGUNIT": "degrees", **cards}.items():
            if v is not None:
                h[k] = v
        return h

    def test_its_profile_places_the_cards_without_any_code_of_vgt(self):
        o = op.read(self.header(), [HOG_PROFILE])
        self.assertEqual((o["label"], o["frame"], o["quantity"], o["unit"], o["statistics"]), ("profile:hog-fixture@0.1", "pixel", "gradient", "deg", "axial"))
        self.assertEqual((o["unresolved"], o["zero_direction"], o["sense"]), ([], "+x", "counter-clockwise"))
        s = op.read(self.header(HOGFRAME="EQ", HOGANGLE="ORTHOGONAL", HOGUNIT="RADIANS"), [HOG_PROFILE])
        self.assertEqual((s["frame"], s["sky_frame"], s["quantity"], s["unit"]), ("sky", "icrs", "b_field", "rad"))

    def test_the_vgt_profile_does_not_read_it_and_it_does_not_read_a_vgt_product(self):
        self.assertIsNone(op.read(self.header()))                                         # shipped profiles only: nothing detected
        vgt = fits.Header({"VGTPROD": "psi", "PSIFRAME": "PIXEL", "PSIANGLE": "BFIELD", "BUNIT": "rad"})
        self.assertIsNone(op.read(vgt, [HOG_PROFILE]))

    def test_what_it_cannot_place_stays_unresolved(self):
        o = op.read(self.header(HOGFRAME="SKYWARD", HOGUNIT=None), [HOG_PROFILE])
        self.assertEqual((o["frame"], o["unit"], o["quantity"]), (None, None, "gradient"))
        self.assertEqual(o["unresolved"], ["frame", "unit"])
        self.assertTrue(any("SKYWARD" in n for n in o["notes"]))

    def test_its_producer_cards_are_dropped_with_the_others(self):
        self.assertTrue(op.producer_cards([HOG_PROFILE]).fullmatch("HOGANGLE"))
        self.assertFalse(op.producer_cards([HOG_PROFILE]).fullmatch("PSIFRAME"))

    def test_a_generated_fits_file_goes_through_the_whole_header_reader(self):
        with tempfile.TemporaryDirectory() as d:
            path = Path(d) / "hog_orientation.fits"
            g.vgt_psi_with_cards("deg", [("HOGFRAME", "IMAGE", "angle from +x, counter-clockwise"), ("HOGANGLE", "GRADIENT", "gradient direction"),
                                         ("HOGUNIT", "degrees", "")]).writeto(path)
            h = fits.getheader(path)
            o = op.read({k: h[k] for k in h.keys()}, [HOG_PROFILE])
            self.assertEqual((o["frame"], o["quantity"], o["unit"], o["unresolved"]), ("pixel", "gradient", "deg", []))
            self.assertIsNone(describe(path)["orientation"])                                # not a tool Vesqira ships a profile for


class Table(unittest.TestCase):
    def test_the_shipped_table_is_valid_and_names_vgt_jl(self):
        names = [(p["name"], p["version"]) for p in op.shipped()]
        self.assertEqual(names, [("VGT.jl", "1")])

    def test_a_malformed_profile_is_refused(self):
        doc = {"format": 1, "profiles": [copy.deepcopy(HOG_PROFILE)]}
        op.check(doc)
        bad = copy.deepcopy(doc)
        bad["profiles"][0]["name"] = "a@b"
        with self.assertRaises(ValueError):
            op.check(bad)
        bad = copy.deepcopy(doc)
        bad["profiles"][0]["fields"]["frame"]["values"]["EQ"]["frame"] = "ecliptic"
        with self.assertRaises(ValueError):
            op.check(bad)
        bad = copy.deepcopy(doc)
        bad["profiles"][0]["fields"]["frame"]["values"]["lower"] = {"frame": "pixel"}
        with self.assertRaises(ValueError):
            op.check(bad)


if __name__ == "__main__":
    unittest.main()
