"""Plain unit conversion of a run's declared numbers (ADR 0062). Oracles are closed-form factors, not the worker's code path.
The point of the operation is what it refuses: no equivalency may leak in, because a run's number has no beam, frame or
spectral axis."""
import math
import unittest

from test_plane_worker import Client


def item(v, frm, e=None):
    return {"value": v, "uncertainty": e, "from": frm}


class UnitValues(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.c = Client()

    @classmethod
    def tearDownClass(cls):
        cls.c.close()

    def conv(self, to, *items):
        head, _ = self.c.ask(op="convert_values", to=to, values=list(items))
        self.assertTrue(head["ok"], head)
        return head

    def test_angle_degrees_radians_with_uncertainty(self):
        h = self.conv("deg", item(math.pi / 2, "rad", 0.01), item(30, "deg", 0.5), item(3600, "arcsec"))
        self.assertFalse(h["refused"])
        r = h["results"]
        self.assertAlmostEqual(r[0]["value"], 90.0, places=12)
        self.assertAlmostEqual(r[0]["uncertainty"], 0.01 * 180 / math.pi, places=12)    # scaled by the same factor
        self.assertEqual(r[1], {"value": 30.0, "uncertainty": 0.5})
        self.assertAlmostEqual(r[2]["value"], 1.0, places=12)
        self.assertIsNone(r[2]["uncertainty"])                                          # no uncertainty is not zero
        self.assertAlmostEqual(h["factors"]["rad"], 180 / math.pi, places=12)

    def test_negative_value_and_zero_are_scaled_not_dropped(self):
        h = self.conv("rad", item(-180, "deg"), item(0, "deg", 0))
        self.assertAlmostEqual(h["results"][0]["value"], -math.pi, places=12)
        self.assertEqual(h["results"][1], {"value": 0.0, "uncertainty": 0.0})

    def test_prefixed_units(self):
        h = self.conv("K", item(2500, "mK", 5))
        self.assertAlmostEqual(h["results"][0]["value"], 2.5)
        self.assertAlmostEqual(h["results"][0]["uncertainty"], 0.005)

    def assertRefused(self, to, frm, words):
        h = self.conv(to, item(1, frm))
        self.assertTrue(h["refused"], h)
        self.assertIn(words, h["reason"])
        self.assertNotIn("results", h)

    def test_no_equivalency_leaks(self):
        self.assertRefused("Jy/beam", "K", "measure different things")          # brightness_temperature needs beam and frequency
        self.assertRefused("K", "Jy/beam", "measure different things")
        self.assertRefused("Hz", "km/s", "measure different things")            # spectral / doppler need a rest frequency
        self.assertRefused("m", "Hz", "measure different things")
        self.assertRefused("rad", "1", "measure different things")              # dimensionless_angles is never applied
        self.assertRefused("1", "rad", "measure different things")
        self.assertRefused("K", "deg_C", "measure different things")            # temperature() equivalency is not applied

    def test_unreadable_and_function_units_are_refused(self):
        self.assertRefused("deg", "furlongs-per-fortnight", "not a unit")
        for to, frm in (("deg", "mag"), ("mag", "mag"), ("dex", "dB"), ("dB", "dB"), ("mag/s", "mag/s")):   # logarithmic units are never a plain scaling
            self.assertRefused(to, frm, "not a unit")
        h = self.conv("notaunit", item(1, "deg"))
        self.assertTrue(h["refused"])
        self.assertIn("target unit", h["reason"])

    def test_one_refusal_refuses_the_whole_request(self):
        h = self.conv("deg", item(1, "rad"), item(1, "K"))
        self.assertTrue(h["refused"])
        self.assertNotIn("results", h)

    def test_malformed_requests(self):
        for vals in ([], [{"value": "x", "from": "deg"}], [{"value": 1}], [{"value": 1, "from": "deg", "uncertainty": -1}]):
            head, _ = self.c.ask(op="convert_values", to="deg", values=vals)
            self.assertFalse(head["ok"], vals)
            self.assertEqual(head["code"], "bad_request")

    def probe(self, *pairs):
        head, _ = self.c.ask(op="convert_values", pairs=[{"from": f, "to": t} for f, t in pairs])
        self.assertTrue(head["ok"], head)
        self.assertNotIn("results", head)
        return head["pairs"]

    def test_probe_answers_each_pair_alone_and_agrees_with_a_real_conversion(self):
        pairs = (("deg", "rad"), ("K", "Jy/beam"), ("mK", "K"), ("furlongs", "K"), ("mag", "mag"), ("rad", "notaunit"))
        got = self.probe(*pairs)
        self.assertEqual([(g["from"], g["to"]) for g in got], list(pairs))        # one answer per pair, in order; a refusal does not hide the next
        self.assertEqual([g["refused"] for g in got], [False, True, False, True, True, True])
        self.assertAlmostEqual(got[0]["factor"], math.pi / 180, places=12)
        self.assertAlmostEqual(got[2]["factor"], 1e-3, places=12)
        self.assertIn("measure different things", got[1]["reason"])
        self.assertIn("not a unit", got[3]["reason"])
        self.assertIn("target unit", got[5]["reason"])
        for g in got:                                                             # the probe never promises what a conversion refuses
            self.assertEqual("factor" in g, "reason" not in g)
            real = self.conv(g["to"], item(1, g["from"]))
            self.assertEqual(real["refused"], g["refused"], g)
            if g["refused"]:
                self.assertEqual(real["reason"], g["reason"])

    def test_malformed_probe(self):
        for pairs in ([], [{"from": "deg"}], [{"from": 1, "to": "deg"}], [{"from": "deg", "to": "rad"}] * 201):
            head, _ = self.c.ask(op="convert_values", pairs=pairs)
            self.assertFalse(head["ok"], pairs)
            self.assertEqual(head["code"], "bad_request")


if __name__ == "__main__":
    unittest.main()
