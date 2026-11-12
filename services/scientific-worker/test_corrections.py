"""WCS and unit corrections (ADR 0061). The oracle is astropy built directly, in the test, from a corrected copy of the header
(and closed-form Doppler formulas), never the worker's code path. The file must be byte-identical afterwards."""
import hashlib
import json
import math
import subprocess
import sys
import tempfile
import unittest
import warnings
from pathlib import Path

from astropy.io import fits
from astropy.wcs import WCS

from test_plane_worker import HERE, Client, g

C = 299792458.0


def st(bunit=None, wcs=None):
    """A correction state as the API sends it; its hash names the content (the worker caches by it)."""
    wcs = wcs or {}
    return {"hash": hashlib.sha256(json.dumps([bunit, wcs], sort_keys=True).encode()).hexdigest(), "bunit": bunit, "wcs": wcs}


def sha(p):
    return hashlib.sha256(Path(p).read_bytes()).hexdigest()


def world(header, x, y, channel=0):
    """Independent oracle: astropy on a header the test corrected itself."""
    with warnings.catch_warnings():
        warnings.simplefilter("ignore")
        w = WCS(header, naxis=header["NAXIS"])
        pix = [x, y] + ([channel] if header["NAXIS"] == 3 else [])
        return w.all_pix2world([pix], 0)[0]


class Corrections(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        cls.dir = Path(cls.tmp.name)
        cls.f = g.write_all(cls.dir)
        cls.cube = cls.f["hi_cube.fits"]
        cls.freq = cls.f["hi_cube_freq.fits"]
        cls.before = {p: sha(p) for p in (cls.cube, cls.freq)}
        cls.c = Client()

    @classmethod
    def tearDownClass(cls):
        cls.c.close()
        # every op below read these files with corrections applied in memory; not one byte changed
        for p, digest in cls.before.items():
            assert sha(p) == digest, f"{p.name} changed"
        cls.tmp.cleanup()

    def ask(self, path, state=None, **req):
        if state is not None:
            req["corrections"] = {str(path): state}
        head, _ = self.c.ask(path=str(path), **req)
        return head

    # ----- a CRVAL / CDELT correction moves the world coordinates of known pixels by the expected amount -----
    def test_crval_and_cdelt_correction_matches_astropy_on_a_corrected_header(self):
        state = st(None, {"CRVAL2": g.CRVAL_DEC + 0.05, "CDELT1": -0.012})
        fixed = fits.getheader(self.cube).copy()
        fixed["CRVAL2"], fixed["CDELT1"] = g.CRVAL_DEC + 0.05, -0.012
        for x, y in ((0, 0), (15, 11), (31, 23), (20, 9)):
            plain = self.ask(self.cube, op="coord", x=x, y=y, channel=0)
            got = self.ask(self.cube, state, op="coord", x=x, y=y, channel=0)
            self.assertTrue(got["ok"], got)
            want = world(fixed, x, y)
            self.assertAlmostEqual(got["sky"]["lon"], want[0], places=10)
            self.assertAlmostEqual(got["sky"]["lat"], want[1], places=10)
            self.assertEqual(got["corrections_applied"], [state["hash"]])
            self.assertEqual(got["wcs_corrected"], ["CDELT1", "CRVAL2"])
            self.assertNotIn("corrections_applied", plain)       # an uncorrected request is answered from the file
            self.assertAlmostEqual(plain["sky"]["lat"], world(fits.getheader(self.cube), x, y)[1], places=10)
        # at the reference pixel (FITS 16, 12 = 0-based 15, 11) the latitude moves by exactly the CRVAL2 change
        a = self.ask(self.cube, op="coord", x=15, y=11, channel=0)["sky"]
        b = self.ask(self.cube, state, op="coord", x=15, y=11, channel=0)["sky"]
        self.assertAlmostEqual(b["lat"] - a["lat"], 0.05, places=10)
        self.assertAlmostEqual(b["lon"], a["lon"], places=10)

    def test_a_cached_corrected_reading_is_never_served_for_the_file(self):
        state = st(None, {"CRVAL1": g.CRVAL_RA + 1.0})
        first = self.ask(self.cube, state, op="coord", x=15, y=11, channel=0)["sky"]["lon"]
        plain = self.ask(self.cube, op="coord", x=15, y=11, channel=0)["sky"]["lon"]
        again = self.ask(self.cube, state, op="coord", x=15, y=11, channel=0)["sky"]["lon"]
        self.assertAlmostEqual(first - plain, 1.0, places=9)
        self.assertEqual(first, again)

    # ----- spectral: RESTFRQ and SPECSYS -----
    def test_restfrq_correction_changes_radio_velocity_as_doppler_says(self):
        rest_new = 1.4200e9
        state = st(None, {"RESTFRQ": rest_new})
        for ch in (0, 3, 7):
            f = self.ask(self.freq, op="convert", x=3, y=3, channel=ch, spectral="km/s", convention="radio")["spectral"]
            fc = self.ask(self.freq, state, op="convert", x=3, y=3, channel=ch, spectral="km/s", convention="radio")["spectral"]
            nu = f["native"]["value"]
            self.assertAlmostEqual(f["converted"]["value"], C * (1 - nu / g.REST_HI_HZ) / 1000, places=6)
            self.assertAlmostEqual(fc["converted"]["value"], C * (1 - nu / rest_new) / 1000, places=6)
            self.assertNotAlmostEqual(f["converted"]["value"], fc["converted"]["value"], places=3)
            self.assertEqual(fc["rest"]["source"], "RESTFRQ (corrected by the researcher)")
            self.assertIn("the RESTFRQ (corrected by the researcher)", fc["basis"])
            self.assertIn("the file's RESTFRQ", f["basis"])

    def test_specsys_correction_is_what_the_view_says(self):
        state = st(None, {"SPECSYS": "BARYCENT"})
        plain = self.ask(self.freq, op="view")
        got = self.ask(self.freq, state, op="view")
        self.assertEqual(plain["spectral"]["specsys"], "LSRK")
        self.assertEqual(got["spectral"]["specsys"], "BARYCENT")
        self.assertEqual(got["wcs_corrected"], ["SPECSYS"])

    def test_each_corrected_fact_says_it_is_corrected(self):
        state = st(None, {"RADESYS": "FK5", "CTYPE3": "VOPT", "CUNIT3": "km/s", "CRVAL3": -32.0, "CDELT3": 1.0, "SPECSYS": "BARYCENT"})
        v = self.ask(self.cube, state, op="view")
        self.assertTrue(v["ok"], v)
        self.assertEqual((v["sky"]["frame"], v["sky"]["frame_status"]), ("FK5", "corrected"))
        sp = v["spectral"]
        self.assertEqual((sp["ctype"], sp["velocity"], sp["unit"], sp["unit_status"]), ("VOPT", "corrected", "m/s", "corrected"))  # wcslib reports the axis in SI
        self.assertEqual(sp["corrected"], ["CDELT3", "CRVAL3", "CTYPE3", "CUNIT3", "SPECSYS"])
        plain = self.ask(self.cube, op="view")
        self.assertEqual((plain["sky"]["frame_status"], plain["spectral"]["velocity"], plain["spectral"]["unit_status"]), ("declared", "declared", "declared"))
        self.assertNotIn("corrected", plain["spectral"])
        c = self.ask(self.cube, state, op="convert", x=3, y=3, channel=0, spectral="m/s", frame="galactic")
        self.assertEqual(c["spectral"]["convention_source"], "corrected")
        self.assertIn("corrected axis", c["spectral"]["basis"])
        self.assertIn("researcher's correction", c["spectral"]["reference_frame_note"])
        self.assertEqual(c["sky"]["native"]["frame_status"], "corrected")
        self.assertIn("researcher's correction", c["sky"]["frame_note"])
        pc = self.ask(self.cube, op="convert", x=3, y=3, channel=0, spectral="m/s")
        self.assertEqual(pc["spectral"]["convention_source"], "file")

    # ----- unit -----
    def test_unit_correction_replaces_the_files_unit_and_says_what_the_file_says(self):
        state = st("Jy/beam", {})
        head = self.ask(self.cube, state, op="plane", channel=0)
        self.assertEqual((head["bunit"], head["bunit_status"], head["file_bunit"]), ("Jy/beam", "corrected", "K"))
        plain = self.ask(self.cube, op="plane", channel=0)
        self.assertEqual((plain["bunit"], plain["bunit_status"]), ("K", "declared"))
        # a file with no BUNIT gets a declaration (ADR 0038), not a correction
        no = self.dir / "no_bunit.fits"
        hd = g.hi_cube()
        del hd[0].header["BUNIT"]
        hd.writeto(no, overwrite=True)
        r = self.ask(no, state, op="view")
        self.assertFalse(r["ok"])
        self.assertIn("declare its unit", r["error"])

    # ----- refusals -----
    def refused(self, path, wcs, needle):
        r = self.ask(path, st(None, wcs), op="view")
        self.assertFalse(r["ok"], wcs)
        self.assertIn(needle, r["error"])
        p = self.c.ask(op="wcs_preview", path=str(path), proposed=st(None, wcs))[0]
        self.assertTrue(p["ok"])
        self.assertIsNone(p["after"])
        self.assertIn(needle, p["refused"])

    def test_refuses_a_change_of_axis_kind(self):
        self.refused(self.cube, {"CTYPE1": "FREQ"}, "axis 1 would change kind (lon to spectral)")
        self.refused(self.cube, {"CTYPE3": "RA---TAN"}, "axis 3 would change kind")
        self.refused(self.cube, {"CTYPE1": "DEC--TAN", "CTYPE2": "RA---TAN"}, "would change kind")

    def test_refuses_an_axis_the_image_does_not_have(self):
        self.refused(self.cube, {"CRVAL4": 1.0}, "names an axis the image does not have")
        self.refused(self.cube, {"PC1_4": 0.0}, "names an axis the image does not have")
        self.refused(self.cube, {"NAXIS": 4}, "is not a keyword that can be corrected")

    def test_refuses_a_wcs_astropy_does_not_accept(self):
        self.refused(self.cube, {"CTYPE1": "RA---XYZ", "CTYPE2": "DEC--XYZ"}, "astropy does not accept the corrected WCS")
        self.refused(self.cube, {"CDELT1": 0}, "cannot be zero")
        self.refused(self.cube, {"CDELT1": "a lot"}, "finite number")
        self.refused(self.cube, {"SPECSYS": "NEARBY"}, "SPECSYS is one of")

    def test_refuses_mixing_cd_with_cdelt_and_two_rest_frequencies(self):
        self.refused(self.cube, {"CD1_1": -0.01}, "correct those, not CD")
        self.refused(self.cube, {"RESTFREQ": 1.0e9}, "two different rest frequencies")

    # ----- preview -----
    def test_preview_gives_centre_and_corners_before_and_after(self):
        cur = st(None, {"CRVAL1": g.CRVAL_RA + 0.5})
        prop = st("Jy/beam", {"CRVAL1": g.CRVAL_RA + 0.5, "CRVAL2": g.CRVAL_DEC - 0.1})
        p = self.c.ask(op="wcs_preview", path=str(self.cube), current=cur, proposed=prop)[0]
        self.assertTrue(p["ok"], p)
        self.assertIsNone(p["refused"])
        names = [q["name"] for q in p["before"]["points"]]
        self.assertEqual(names, ["centre", "bottom left", "bottom right", "top left", "top right"])
        fixed_before = fits.getheader(self.cube).copy()
        fixed_before["CRVAL1"] = g.CRVAL_RA + 0.5
        fixed_after = fixed_before.copy()
        fixed_after["CRVAL2"] = g.CRVAL_DEC - 0.1
        for qb, qa in zip(p["before"]["points"], p["after"]["points"]):
            wb, wa = world(fixed_before, qb["x"], qb["y"]), world(fixed_after, qa["x"], qa["y"])
            self.assertAlmostEqual(qb["lon"], wb[0], places=10)
            self.assertAlmostEqual(qa["lat"], wa[1], places=10)
        self.assertEqual([c["keyword"] for c in p["changes"]], ["CRVAL1", "CRVAL2"])
        self.assertEqual(p["changes"][1]["file"], g.CRVAL_DEC)
        self.assertEqual((p["file_bunit"], p["bunit_after"]), ("K", "Jy/beam"))
        chans = [c["channel"] for c in p["after"]["spectral"]["channels"]]
        self.assertEqual(chans, [0, 31, 63])

    # ----- an analysis carries the corrected WCS and unit as its own, and says so -----
    def test_moment_map_of_a_corrected_cube_is_stamped(self):
        out = self.dir / "m0.fits"
        state = st("Jy/beam", {"CRVAL2": g.CRVAL_DEC + 0.05})
        r = subprocess.run([sys.executable, "-P", str(HERE / "analysis.py"), "moment", "--input", str(self.cube), "--order", "0",
                            "--out", str(out), "--corrections", json.dumps({str(self.cube): state})], capture_output=True, text=True)
        res = json.loads(r.stdout)
        self.assertTrue(res["ok"], res)
        self.assertEqual(res["corrections_applied"], [state["hash"]])
        h = fits.getheader(out)
        self.assertAlmostEqual(h["CRVAL2"], g.CRVAL_DEC + 0.05, places=12)
        self.assertEqual(h["VESQWCSC"], "corrected")
        self.assertEqual(h["VESQBUNS"], "corrected")
        self.assertTrue(h["BUNIT"].startswith("Jy/beam"))
        self.assertIn("correction", res["summary"]["policy"])
        self.assertIn("The WCS of the input was the author's correction (CRVAL2)", res["summary"]["policy"])
        self.assertIn("CRVAL2", " ".join(str(v) for v in h["HISTORY"]))


if __name__ == "__main__":
    unittest.main()
