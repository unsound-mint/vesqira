import os
import sys
import tempfile
import unittest
from pathlib import Path

import numpy as np
from astropy import constants as const
from astropy import units as u
from astropy.io import fits
from astropy.wcs import WCS

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parent.parent / "fixtures"))
import generate as g  # noqa: E402
import rohsa  # noqa: E402
from test_plane_worker import Client  # noqa: E402


class Index(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.dir = Path(self.tmp.name)
        self.dat = g.write_rohsa_run(self.dir / "run")
        self.cache = str(self.dir / "cache")
        self.args = (g.RX, g.RY, g.RN)

    def tearDown(self):
        self.tmp.cleanup()

    def build(self, **kw):
        return rohsa.index_step(str(self.dat), self.cache, *self.args, **kw)

    def test_every_pixel_reads_back_its_own_row_with_x_and_y_not_swapped(self):
        self.assertEqual(self.build()["state"], "ready")
        t = g.rohsa_truth()
        for y in range(g.RY):
            for x in range(g.RX):
                amp, mean, sig = rohsa.read_pixel(str(self.dat), self.cache, *self.args, x, y)
                np.testing.assert_array_equal(amp, t["amp"][y, x])
                np.testing.assert_array_equal(mean, t["mean_file"][y, x])    # the raw, 1-based value is returned untouched
                np.testing.assert_array_equal(sig, t["sigma"][y, x])

    def test_build_is_chunked_resumable_and_reused(self):
        old = rohsa.CHUNK_BYTES
        rohsa.CHUNK_BYTES = 700                       # a few rows per chunk: the header, fast and slow paths all meet
        try:
            r = self.build(budget_s=1e-9)             # one chunk then out of time
            self.assertEqual(r["state"], "building")
            self.assertGreater(r["done_bytes"], 0)
            self.assertLess(r["done_bytes"], r["size_bytes"])
            while r["state"] == "building":
                r = self.build(budget_s=1e-9)
            self.assertEqual(r["state"], "ready")
        finally:
            rohsa.CHUNK_BYTES = old
        offs = Path(self.cache).glob("*.offs")
        (offs_file,) = list(offs)
        self.assertEqual(offs_file.stat().st_size, g.RX * g.RY * 8)           # 8 bytes per pixel, nothing else
        before = offs_file.stat().st_mtime_ns
        self.assertEqual(self.build()["state"], "ready")                        # reused: not rewritten
        self.assertEqual(offs_file.stat().st_mtime_ns, before)
        self.assertFalse(any(p.parent == self.dat.parent for p in Path(self.cache).iterdir()))   # never next to the data
        amp, _, _ = rohsa.read_pixel(str(self.dat), self.cache, *self.args, 5, 3)
        np.testing.assert_array_equal(amp, g.rohsa_truth()["amp"][3, 5])

    def test_a_changed_file_is_indexed_again(self):
        self.build()
        text = self.dat.read_text()
        self.dat.write_text(text.replace("# i, j, A, mean, sigma", "# i, j, A, mean, sigma  ", 1))
        os.utime(self.dat, ns=(1, 1))
        with self.assertRaises(rohsa.Bad) as e:
            rohsa.read_pixel(str(self.dat), self.cache, *self.args, 0, 0)
        self.assertEqual(e.exception.code, "index_not_ready")
        self.assertEqual(self.build()["state"], "ready")

    def refused(self, edit):
        lines = self.dat.read_text().splitlines()
        edit(lines)
        self.dat.write_text("\n".join(lines) + "\n")
        r = self.build()
        self.assertEqual(r["state"], "refused")
        return r

    def test_malformed_rows_are_reported_with_their_line(self):
        r = self.refused(lambda ls: ls.__setitem__(10, "  1 2 3"))
        self.assertEqual(r["malformed"], 1)
        self.assertEqual(r["examples"][0]["line"], 11)
        self.assertIn("columns", r["examples"][0]["problem"])
        self.tearDown()
        self.setUp()
        r = self.refused(lambda ls: ls.__setitem__(12, ls[12].replace(ls[12].split()[3], "nan")))
        self.assertIn("finite", r["examples"][0]["problem"])

    def test_rows_out_of_order_or_missing_are_refused(self):
        r = self.refused(lambda ls: ls.__setitem__(slice(5, 7), [ls[6], ls[5]]))
        self.assertGreaterEqual(r["malformed"], 1)
        self.assertIn("should be", r["examples"][0]["problem"])
        self.tearDown()
        self.setUp()
        r = self.refused(lambda ls: ls.pop())
        self.assertIn("needs", r["reason"])

    def test_wrong_gaussian_count_is_refused_not_misread(self):
        r = rohsa.index_step(str(self.dat), self.cache, g.RX, g.RY, g.RN + 1)
        self.assertEqual(r["state"], "refused")

    def test_read_before_index_and_out_of_range_are_refused(self):
        with self.assertRaises(rohsa.Bad) as e:
            rohsa.read_pixel(str(self.dat), self.cache, *self.args, 0, 0)
        self.assertEqual(e.exception.code, "index_not_ready")
        self.build()
        with self.assertRaises(rohsa.Bad):
            rohsa.read_pixel(str(self.dat), self.cache, *self.args, g.RX, 0)


class Curves(unittest.TestCase):
    def test_raw_mean_eleven_sits_at_channel_index_ten(self):
        comps, curves, model = rohsa.components_at(np.array([2.0]), np.array([11.0]), np.array([1.5]), 30, None, None)
        self.assertEqual(int(np.argmax(model)), 10)
        self.assertEqual(comps[0]["mean_channel"], 10.0)
        self.assertEqual(comps[0]["mean_channel_file"], 11.0)
        self.assertEqual(model[10], 2.0)                      # amp at the centre, exactly
        self.assertAlmostEqual(model[9], model[11])

    def test_invalid_sigma_is_flagged_and_not_drawn_and_zero_amp_is_kept(self):
        comps, curves, model = rohsa.components_at(np.array([1.0, 0.0, 3.0]), np.array([5.0, 8.0, 9.0]), np.array([0.0, 2.0, -1.0]), 20, None, None)
        self.assertEqual([c["valid"] for c in comps], [False, True, False])
        self.assertTrue(np.all(model == 0.0))                 # nothing valid carries amplitude
        self.assertEqual(comps[1]["amp"], 0.0)                # kept as a value
        self.assertIsNone(comps[0]["sigma"])

    def test_doppler_constant_is_hydrogen_thermal_width(self):
        k = (const.m_p + const.m_e) * (1 * u.km / u.s) ** 2 / const.k_B
        self.assertAlmostEqual(k.to_value(u.K) / rohsa.DOPPLER_K_PER_KMS2, 1.0, delta=2e-4)
        self.assertEqual(round(float(np.sqrt(500.0 / rohsa.DOPPLER_K_PER_KMS2)), 2), 2.03)   # a 500 K cut is still 2.03 km/s to two decimals


class WorkerOp(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        cls.dir = Path(cls.tmp.name)
        cls.dat = g.write_rohsa_run(cls.dir / "run")
        cls.crop = cls.dat.parent / f"{g.R_REGION}.fits"
        cls.cache = str(cls.dir / "cache")
        cls.c = Client()

    @classmethod
    def tearDownClass(cls):
        cls.c.close()
        cls.tmp.cleanup()

    def req(self, op, **kw):
        return self.c.ask(op=op, path=str(self.crop), dat_path=str(self.dat), cache_dir=self.cache, n_gauss=g.RN, **kw)

    def test_index_then_pixel_matches_independent_evaluation(self):
        head, _ = self.req("rohsa_pixel", x=2, y=1)
        self.assertFalse(head["ok"])
        self.assertEqual(head["code"], "index_not_ready")
        head, _ = self.req("rohsa_index")
        self.assertEqual(head["state"], "ready")
        head, payload = self.req("rohsa_pixel", x=2, y=1)
        self.assertTrue(head["ok"], head)
        nc = head["channels"]
        arr = np.frombuffer(payload, dtype="<f8")
        axis, model, comps = arr[:nc], arr[nc:2 * nc], arr[2 * nc:].reshape(g.RN, nc)
        t = g.rohsa_truth()
        # channel -> km/s through the WCS equals the closed form of the FITS linear axis (channel c is pixel c + 1)
        np.testing.assert_allclose(axis, g.rohsa_velocity_ms(np.arange(nc)) / 1000.0, rtol=1e-12)
        w = WCS(fits.getheader(self.crop), naxis=3)
        np.testing.assert_allclose(axis, w.all_pix2world(np.column_stack([np.zeros(nc), np.zeros(nc), np.arange(nc)]), 0)[:, 2] / 1000.0, rtol=1e-12)
        # the model is the sum of the components, and equals a direct NumPy evaluation of the file's parameters (mean - 1)
        np.testing.assert_allclose(model, comps.sum(axis=0), rtol=1e-12)
        np.testing.assert_allclose(model, g.rohsa_model()[:, 1, 2], rtol=1e-12)
        for k, c in enumerate(head["components"]):
            m0 = t["mean_file"][1, 2, k] - 1.0
            self.assertAlmostEqual(c["mean_channel"], m0)
            self.assertAlmostEqual(c["centre"], g.rohsa_velocity_ms(m0) / 1000.0, places=9)
            self.assertAlmostEqual(c["sigma"], t["sigma"][1, 2, k] * g.R_CDELT / 1000.0, places=9)
            self.assertAlmostEqual(c["fwhm"], 2 * np.sqrt(2 * np.log(2)) * c["sigma"], places=9)
            self.assertAlmostEqual(c["t_doppler_k"], 121.2 * c["sigma"] ** 2, places=9)
        self.assertEqual(head["axis"]["kind"], "velocity")
        self.assertEqual(head["axis"]["unit"], "km/s")
        self.assertEqual(head["axis"]["unit_status"], "inferred")     # the crop has no CUNIT3: m/s is the FITS default, said so
        self.assertEqual(head["bunit"], "K")

    def test_zero_amplitude_component_is_listed_with_its_width(self):
        self.req("rohsa_index")
        head, _ = self.req("rohsa_pixel", x=0, y=0)
        c = head["components"][2]
        self.assertEqual(c["amp"], 0.0)
        self.assertTrue(c["valid"])
        self.assertIsNotNone(c["sigma"])

    def test_pixel_outside_the_crop_and_bad_requests_are_refused(self):
        self.req("rohsa_index")
        head, _ = self.req("rohsa_pixel", x=g.RX, y=0)
        self.assertEqual(head["code"], "bad_request")
        head, _ = self.c.ask(op="rohsa_pixel", path=str(self.crop), dat_path="relative.dat", cache_dir=self.cache, n_gauss=g.RN, x=0, y=0)
        self.assertEqual(head["code"], "bad_request")


class RemoteWorkerOp(unittest.TestCase):
    """A worker that enforces roots (a host) keeps the ROHSA index in the cache folder its launcher names, never one a request
    names, never next to the data, and never through a link."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.dir = Path(self.tmp.name).resolve()
        self.dat = g.write_rohsa_run(self.dir / "data" / "run")
        self.crop = self.dat.parent / f"{g.R_REGION}.fits"
        self.cache = self.dir / "home" / ".cache" / "vesqira" / "rohsa"
        self.clients = []

    def tearDown(self):
        for c in self.clients:
            c.close()
        self.tmp.cleanup()

    def client(self, cache="default", roots=None):
        env = {} if cache is None else {"VESQIRA_CACHE": str(self.cache if cache == "default" else cache)}
        c = Client(roots=roots or [self.dir / "data"], env_extra=env)
        self.clients.append(c)
        return c

    def ask(self, c, op, **kw):
        return c.ask(op=op, path=str(self.crop), dat_path=str(self.dat), n_gauss=g.RN, **kw)

    def test_the_index_goes_to_the_launchers_cache_and_a_requested_folder_is_ignored(self):
        c = self.client()
        evil = self.dir / "evil"
        head, _ = self.ask(c, "rohsa_index", cache_dir=str(evil))
        self.assertTrue(head["ok"], head)
        self.assertEqual(head["state"], "ready")
        self.assertTrue(head["created"] and head["settled"])
        self.assertFalse(evil.exists())
        names = sorted(p.name for p in self.cache.iterdir())
        self.assertEqual([n.rsplit(".", 1)[1] for n in names], ["json", "offs"])
        self.assertEqual(self.cache.stat().st_mode & 0o777, 0o700)
        self.assertEqual(head["cache_file"], str(self.cache / names[1]))
        self.assertEqual(sorted(p.name for p in self.dat.parent.iterdir() if p.suffix in (".offs", ".tmp")), [])   # never next to the data
        again, _ = self.ask(c, "rohsa_index")
        self.assertFalse(again["created"] or again["settled"])                                                    # nothing to audit twice
        head, payload = self.ask(c, "rohsa_pixel", x=2, y=1)
        self.assertTrue(head["ok"], head)
        np.testing.assert_allclose(np.frombuffer(payload, dtype="<f8")[head["channels"]:2 * head["channels"]], g.rohsa_model()[:, 1, 2], rtol=1e-12)

    def test_a_host_without_a_cache_folder_or_with_a_link_or_the_data_folder_refuses(self):
        for cache, why in ((None, "no cache folder"),):
            head, _ = self.ask(self.client(cache=cache), "rohsa_index")
            self.assertEqual((head["ok"], head["code"]), (False, "unsupported"), why)
        link = self.dir / "link"
        (self.dir / "real").mkdir()
        link.symlink_to(self.dir / "real")
        head, _ = self.ask(self.client(cache=link), "rohsa_index")
        self.assertEqual(head["code"], "unsupported")
        self.assertEqual(list((self.dir / "real").iterdir()), [])
        head, _ = self.ask(self.client(cache=self.dat.parent / "cache"), "rohsa_index")        # inside the data's folder
        self.assertEqual(head["code"], "unsupported")
        self.assertFalse((self.dat.parent / "cache").exists())

    def test_a_link_in_the_cache_folder_is_never_written_through(self):
        c = self.client()
        head, _ = self.ask(c, "rohsa_index")
        victim = self.dir / "victim"
        victim.write_text("do not touch")
        offs = self.cache / Path(head["cache_file"]).name
        offs.unlink()
        offs.symlink_to(victim)
        head, _ = self.ask(c, "rohsa_index")
        self.assertEqual((head["ok"], head["code"]), (False, "unsupported"))
        self.assertEqual(victim.read_text(), "do not touch")

    def test_build_in_a_host_cache_is_chunked_and_resumable(self):
        old = rohsa.CHUNK_BYTES
        rohsa.CHUNK_BYTES = 700
        try:
            r = rohsa.index_step(str(self.dat), str(self.cache), g.RX, g.RY, g.RN, budget_s=1e-9)
            self.assertEqual((r["state"], r["created"], r["settled"]), ("building", True, False))
            steps = 0
            while r["state"] == "building":
                r = rohsa.index_step(str(self.dat), str(self.cache), g.RX, g.RY, g.RN, budget_s=1e-9)
                self.assertFalse(r["created"])
                steps += 1
        finally:
            rohsa.CHUNK_BYTES = old
        self.assertGreater(steps, 1)
        self.assertEqual((r["state"], r["settled"]), ("ready", True))


if __name__ == "__main__":
    unittest.main()
