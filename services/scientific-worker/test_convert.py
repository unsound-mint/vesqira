"""Golden tests for readout conversions (ADR 0040). Oracles are closed-form formulas and astropy used directly in the
test, not the worker's code path: a conversion is right only if it agrees with them."""
import math
import tempfile
import unittest
from pathlib import Path

from astropy import constants as const
from astropy import units as u
from astropy.coordinates import FK4, FK5, ICRS, Galactic, SkyCoord

from test_plane_worker import Client, g

C = 299792458.0
F0 = g.REST_HI_HZ
CIRCLE = math.pi / (4 * math.log(2))      # solid angle of a Gaussian beam = CIRCLE * FWHM_maj * FWHM_min
RESTS = ("RESTFRQ", "RESTFREQ", "RESTWAV")


def radio(f):
    return C * (1 - f / F0)


def optical(f):
    return C * (F0 / f - 1)


def relativistic(f):
    return C * (F0**2 - f**2) / (F0**2 + f**2)


def jy_per_beam_to_kelvin(s_jy, f_hz, fwhm_arcsec):
    omega = CIRCLE * (fwhm_arcsec * math.pi / 180 / 3600) ** 2
    return s_jy * 1e-26 * C**2 / (2 * const.k_B.value * f_hz**2 * omega)


class Convert(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        cls.dir = Path(cls.tmp.name)
        cls.f = g.write_all(cls.dir)
        cls.c = Client()

    @classmethod
    def tearDownClass(cls):
        cls.c.close()
        cls.tmp.cleanup()

    def variant(self, name, edit, base=None):
        hdul = (base or g.cube_frequency_axis)()
        edit(hdul[0].header)
        path = self.dir / name
        hdul.writeto(path, overwrite=True)
        return path

    def conv(self, path, x=3, y=3, channel=0, **kw):
        head, _ = self.c.ask(op="convert", path=str(path), x=x, y=y, channel=channel, **kw)
        return head

    def val(self, path, to, value=None, channel=0, **kw):
        extra = {} if value is None else {"value": value}
        return self.conv(path, channel=channel, to=to, **extra, **kw)["value"]

    # ----- spectral -----
    def test_hi_rest_frequency_is_zero_radio_velocity_both_ways(self):
        # VRAD cube, channel index 32 is exactly 0 km/s; the file's RESTFRQ is the H I line
        r = self.conv(self.f["hi_cube.fits"], channel=32, spectral="MHz")
        self.assertTrue(r["ok"])
        s = r["spectral"]
        self.assertAlmostEqual(s["native"]["value"], 0.0, places=6)
        self.assertEqual(s["native"]["unit"], "m/s")
        self.assertAlmostEqual(s["converted"]["value"], 1420.405751768, places=8)
        self.assertEqual((s["convention"], s["convention_source"], s["rest"]["source"]), ("radio", "file", "RESTFRQ"))
        self.assertIn("doppler_radio", s["basis"])
        # a frequency axis whose first channel is the rest frequency reads 0 km/s in the radio convention
        p = self.variant("at_rest.fits", lambda h: h.__setitem__("CRVAL3", F0))
        v = self.conv(p, spectral="km/s", convention="radio")["spectral"]
        self.assertAlmostEqual(v["converted"]["value"], 0.0, places=9)

    def test_frequency_to_velocity_radio_optical_relativistic_match_closed_forms(self):
        f = 1420.0e6
        for conv, oracle in (("radio", radio), ("optical", optical), ("relativistic", relativistic)):
            s = self.conv(self.f["hi_cube_freq.fits"], spectral="km/s", convention=conv)["spectral"]
            self.assertAlmostEqual(s["converted"]["value"], oracle(f) / 1000, places=6, msg=conv)
            self.assertEqual((s["convention"], s["convention_source"]), (conv, "user"))
            self.assertIn("chosen by you", s["basis"])
        self.assertGreater(abs(radio(f) - optical(f)), 10.0)       # the conventions really differ here (about 24 m/s)
        s = self.conv(self.f["hi_cube_freq.fits"], channel=3, spectral="km/s", convention="radio")["spectral"]
        self.assertAlmostEqual(s["native"]["value"], 1420.3e6, delta=1e-3)
        self.assertAlmostEqual(s["converted"]["value"], radio(1420.3e6) / 1000, places=6)

    def test_frequency_and_wavelength_units_need_no_rest_frequency(self):
        p = self.variant("no_rest.fits", lambda h: [h.remove(k, ignore_missing=True) for k in RESTS])
        for unit, scale in (("MHz", 1e-6), ("GHz", 1e-9), ("kHz", 1e-3)):
            self.assertAlmostEqual(self.conv(p, spectral=unit)["spectral"]["converted"]["value"], 1.42e9 * scale, places=6)
        self.assertAlmostEqual(self.conv(p, spectral="cm")["spectral"]["converted"]["value"], C / 1.42e9 * 100, places=9)   # lambda = c / nu
        self.assertAlmostEqual(self.conv(p, spectral="mm")["spectral"]["converted"]["value"], C / 1.42e9 * 1000, places=8)

    def test_velocity_needs_a_rest_frequency_from_the_file(self):
        p = self.variant("no_rest2.fits", lambda h: [h.remove(k, ignore_missing=True) for k in RESTS])
        s = self.conv(p, spectral="km/s", convention="radio")["spectral"]
        self.assertIsNone(s["converted"])
        self.assertIn("rest frequency", s["refused"])
        offers, _ = self.c.ask(op="conversions", path=str(p))
        t = {x["unit"]: x for x in offers["spectral"]["targets"]}
        self.assertFalse(t["km/s"]["enabled"])
        self.assertIn("rest frequency", t["km/s"]["reason"])
        self.assertTrue(t["GHz"]["enabled"])
        # and a velocity axis cannot become a frequency without one
        q = self.variant("vrad_no_rest.fits", lambda h: h.remove("RESTFRQ"), base=g.hi_cube)
        self.assertIn("rest frequency", self.conv(q, spectral="GHz")["spectral"]["refused"])

    def test_restwav_and_older_restfreq_card_are_read(self):
        def edit(h):
            h.remove("RESTFRQ")
            h["RESTWAV"] = C / F0
        s = self.conv(self.variant("restwav.fits", edit), spectral="km/s", convention="radio")["spectral"]
        self.assertEqual(s["rest"]["source"], "RESTWAV")
        self.assertAlmostEqual(s["converted"]["value"], radio(1420.0e6) / 1000, places=5)

        def edit2(h):
            h.remove("RESTFRQ")
            h["RESTFREQ"] = F0
        s = self.conv(self.variant("restfreq.fits", edit2), spectral="km/s", convention="radio")["spectral"]
        self.assertEqual(s["rest"]["source"], "RESTFREQ")

    def test_frequency_axis_velocity_requires_a_chosen_convention(self):
        s = self.conv(self.f["hi_cube_freq.fits"], spectral="km/s")["spectral"]
        self.assertIsNone(s["converted"])
        self.assertIn("choose radio, optical or relativistic", s["refused"])

    def test_a_velocity_axis_keeps_the_files_convention(self):
        s = self.conv(self.f["hi_cube.fits"], channel=0, spectral="km/s", convention="optical")["spectral"]
        self.assertIsNone(s["converted"])
        self.assertIn("radio convention", s["refused"])
        s = self.conv(self.f["hi_cube.fits"], channel=0, spectral="km/s")["spectral"]
        self.assertAlmostEqual(s["converted"]["value"], -32.0, places=9)
        self.assertEqual((s["convention"], s["convention_source"]), ("radio", "file"))

    def test_the_reference_frame_is_never_changed(self):
        s = self.conv(self.f["hi_cube_freq.fits"], spectral="GHz")["spectral"]
        self.assertEqual(s["specsys"], "LSRK")
        self.assertIn("not changed", s["reference_frame_note"])
        self.assertIn("TOPOCENT to LSRK", s["reference_frame_note"])

    def test_air_wavelength_and_legacy_velocity_axes_are_not_converted(self):
        def edit(h):
            h["CTYPE3"], h["CUNIT3"], h["CRVAL3"], h["CDELT3"] = "AWAV", "m", 0.21, 1e-5
        s = self.conv(self.variant("awav.fits", edit), spectral="GHz")["spectral"]
        self.assertIsNone(s["converted"])
        self.assertIn("air wavelength", s["refused"])
        legacy = self.variant("legacy.fits", lambda h: h.__setitem__("CTYPE3", "VELO-LSR"), base=g.hi_cube)
        s = self.conv(legacy, spectral="GHz")["spectral"]
        self.assertIsNone(s["converted"])
        self.assertIn("legacy", s["refused"])
        # a velocity unit change needs no convention: VELO-LSR in m/s is offered in km/s, nothing assumed
        native = self.conv(legacy, spectral="m/s")["spectral"]
        s = self.conv(legacy, spectral="km/s")["spectral"]
        self.assertIsNone(s["refused"])
        self.assertAlmostEqual(s["converted"]["value"], (native["native"]["value"] * u.Unit(native["native"]["unit"])).to_value(u.km / u.s), places=9)
        self.assertIn("none is assumed", s["basis"])
        s = self.conv(legacy, spectral="km/s", convention="radio")["spectral"]
        self.assertIsNone(s["converted"])

    def test_no_spectral_axis_and_bad_requests(self):
        s = self.conv(self.f["galactic_centre.fits"], spectral="GHz")["spectral"]
        self.assertIsNone(s["converted"])
        self.assertIn("no spectral axis", s["refused"])
        for bad in ({"spectral": "parsec"}, {"spectral": "GHz", "convention": "sideways"}, {"frame": "ecliptic"}, {"to": 5}, {"spectral": 3}):
            r = self.conv(self.f["hi_cube_freq.fits"], **bad)
            self.assertFalse(r["ok"], bad)

    # ----- brightness -----
    def test_unit_change_within_one_dimension(self):
        p = self.f["hi_cube_freq.fits"]
        r = self.val(p, "mJy/beam", 0.25)
        self.assertAlmostEqual(r["converted"]["value"], 250.0, places=9)
        self.assertEqual((r["native"]["unit"], r["native"]["unit_status"]), ("Jy/beam", "declared"))
        k = self.val(self.f["hi_cube.fits"], "mK", 12.5)
        self.assertAlmostEqual(k["converted"]["value"], 12500.0, places=9)
        self.assertIn("one dimension", k["basis"])

    def test_jy_per_beam_to_kelvin_matches_the_closed_form_and_round_trips(self):
        p = self.f["hi_cube_freq.fits"]
        r = self.val(p, "K", 1.0)
        want = jy_per_beam_to_kelvin(1.0, 1.42e9, 60.0)
        self.assertAlmostEqual(r["converted"]["value"] / want, 1.0, places=9)
        self.assertIn("brightness_temperature", r["basis"])
        self.assertIn("60 x 60 arcsec", r["basis"])
        self.assertIn("1420000000 Hz", r["basis"])
        # independent astropy reference with the beam solid angle from its own Gaussian area (sigma = FWHM / 2.3548...)
        sigma = (60 * u.arcsec).to(u.rad).value / (2 * math.sqrt(2 * math.log(2)))
        ref = (1.0 * u.Jy / u.beam).to(u.K, u.brightness_temperature(1.42e9 * u.Hz, beam_area=2 * math.pi * sigma**2 * u.sr))
        self.assertAlmostEqual(r["converted"]["value"] / ref.value, 1.0, places=9)
        # another channel changes the frequency and so the factor (T goes as 1/nu^2)
        r7 = self.val(p, "K", 1.0, channel=7)
        self.assertAlmostEqual(r7["converted"]["value"] / jy_per_beam_to_kelvin(1.0, 1.4207e9, 60.0), 1.0, places=9)
        self.assertNotAlmostEqual(r7["converted"]["value"], r["converted"]["value"], places=6)
        # K back to Jy/beam through a K file with the same beam and axis
        q = self.variant("k_back.fits", lambda h: h.__setitem__("BUNIT", "K"))
        self.assertAlmostEqual(self.val(q, "Jy/beam", want)["converted"]["value"], 1.0, places=9)
        self.assertAlmostEqual(self.val(p, "mK", 1.0)["converted"]["value"] / (want * 1000), 1.0, places=9)

    def test_velocity_axis_frequency_for_the_beam_conversion_comes_from_the_rest_frequency_and_convention(self):
        # hi_cube is K with a 0.03 x 0.02 deg beam; channel 32 is 0 km/s radio, so the plane frequency is the rest frequency
        r = self.val(self.f["hi_cube.fits"], "Jy/beam", 1.0, channel=32)
        omega = CIRCLE * (0.03 * math.pi / 180) * (0.02 * math.pi / 180)
        t_per_jy = 1e-26 * C**2 / (2 * const.k_B.value * F0**2 * omega)
        self.assertAlmostEqual(r["converted"]["value"] * t_per_jy, 1.0, places=9)

    def test_refusals_for_brightness(self):
        p = self.f["hi_cube_freq.fits"]
        no_beam = self.variant("nobeam.fits", lambda h: [h.remove(k) for k in ("BMAJ", "BMIN")])
        r = self.val(no_beam, "K", 1.0)
        self.assertIsNone(r["converted"])
        self.assertIn("beam", r["refused"])
        self.assertEqual(r["native"]["value"], 1.0)
        half = self.variant("halfbeam.fits", lambda h: h.remove("BMIN"))
        self.assertIn("beam", self.val(half, "K", 1.0)["refused"])
        # no frequency for the plane: a 2D map with a beam and no spectral axis
        two_d = self.variant("flat.fits", lambda h: h.update({"BMAJ": 0.01, "BMIN": 0.01}), base=g.cube_galactic_centre)
        r = self.val(two_d, "Jy/beam", 1.0)
        self.assertIsNone(r["converted"])
        self.assertIn("frequency", r["refused"])
        # incompatible dimension
        r = self.val(self.f["hi_cube.fits"], "km/s", 1.0)
        self.assertIsNone(r["converted"])
        self.assertIn("do not measure the same thing", r["refused"])
        self.assertIn("not a unit astropy recognises", self.val(p, "florps", 1.0)["refused"])
        # a pixel with no value is not zero
        r = self.val(p, "mJy/beam")
        self.assertIsNone(r["converted"])
        self.assertIn("no value", r["refused"])

    def test_unknown_unit_offers_nothing_and_a_declared_unit_converts_and_stays_declared(self):
        nounit = self.variant("nounit.fits", lambda h: h.remove("BUNIT"))
        offers, _ = self.c.ask(op="conversions", path=str(nounit))
        self.assertEqual(offers["value"]["targets"], [])
        self.assertIn("no conversion is offered", offers["value"]["reason"])
        r = self.val(nounit, "mJy/beam", 1.0)
        self.assertIsNone(r["converted"])
        self.assertIn("unit is unknown", r["refused"])
        # declared by the person (ADR 0038): converts, and the label survives the conversion
        r = self.val(nounit, "mJy/beam", 1.0, declared_bunit="Jy/beam")
        self.assertAlmostEqual(r["converted"]["value"], 1000.0, places=9)
        self.assertEqual((r["native"]["unit_status"], r["converted"]["unit_status"]), ("user", "user"))
        r = self.val(nounit, "K", 1.0, declared_bunit="Jy/beam")
        self.assertAlmostEqual(r["converted"]["value"] / jy_per_beam_to_kelvin(1.0, 1.42e9, 60.0), 1.0, places=9)
        self.assertEqual(r["converted"]["unit_status"], "user")
        offers, _ = self.c.ask(op="conversions", path=str(nounit), declared_bunit="Jy/beam")
        self.assertEqual(offers["value"]["unit_status"], "user")
        self.assertTrue(offers["value"]["targets"])
        # an unrecognised unit in the file is not guessed at
        weird = self.variant("weird.fits", lambda h: h.__setitem__("BUNIT", "ADU/florp"))
        offers, _ = self.c.ask(op="conversions", path=str(weird))
        self.assertEqual(offers["value"]["targets"], [])
        self.assertIn("not a unit astropy recognises", offers["value"]["reason"])

    def test_offers_say_why_a_cross_conversion_is_disabled(self):
        offers, _ = self.c.ask(op="conversions", path=str(self.f["hi_cube_freq.fits"]))
        t = {x["unit"]: x for x in offers["value"]["targets"]}
        self.assertTrue(t["mJy/beam"]["enabled"] and t["K"]["enabled"] and t["K"]["cross"])
        no_beam = self.variant("nobeam2.fits", lambda h: [h.remove(k) for k in ("BMAJ", "BMIN")])
        offers, _ = self.c.ask(op="conversions", path=str(no_beam))
        t = {x["unit"]: x for x in offers["value"]["targets"]}
        self.assertTrue(t["mJy/beam"]["enabled"])
        self.assertFalse(t["K"]["enabled"])
        self.assertIn("beam", t["K"]["reason"])

    # ----- sky -----
    def test_galactic_centre_in_icrs_is_l_b_zero(self):
        s = self.conv(self.f["galactic_centre.fits"], x=int(g.CRPIX[0]) - 1, y=int(g.CRPIX[1]) - 1, frame="galactic")["sky"]
        l, b = s["converted"]["lon"], s["converted"]["lat"]
        self.assertLess(min(l, 360 - l), 1e-3)
        self.assertLess(abs(b), 1e-3)
        self.assertEqual((s["native"]["frame"], s["native"]["frame_status"]), ("ICRS", "declared"))
        self.assertAlmostEqual(s["native"]["lon"], 266.405, places=9)
        want = SkyCoord(266.405 * u.deg, -28.936 * u.deg, frame=ICRS()).galactic
        self.assertAlmostEqual(l, want.l.deg, places=9)
        self.assertEqual((s["converted"]["lon_name"], s["converted"]["lat_name"]), ("l", "b"))

    def test_fk5_file_to_icrs_and_galactic_matches_astropy(self):
        r = self.conv(self.f["hi_cube_freq.fits"], x=20, y=9, frame="icrs")["sky"]
        native = SkyCoord(r["native"]["lon"] * u.deg, r["native"]["lat"] * u.deg, frame=FK5(equinox="J2000"))
        self.assertEqual(r["native"]["frame"], "FK5 (J2000)")
        self.assertAlmostEqual(r["converted"]["lon"], native.icrs.ra.deg, places=10)
        self.assertAlmostEqual(r["converted"]["lat"], native.icrs.dec.deg, places=10)
        self.assertNotEqual(r["converted"]["lon"], r["native"]["lon"])      # a real (tens of mas) offset, not a copy
        self.assertLess(abs(r["converted"]["lon"] - r["native"]["lon"]), 1e-4)
        gal = self.conv(self.f["hi_cube_freq.fits"], x=20, y=9, frame="galactic")["sky"]["converted"]
        self.assertAlmostEqual(gal["lon"], native.galactic.l.deg, places=10)
        same = self.conv(self.f["hi_cube_freq.fits"], x=20, y=9, frame="fk5")["sky"]
        self.assertEqual((same["converted"]["lon"], same["converted"]["lat"]), (same["native"]["lon"], same["native"]["lat"]))
        self.assertIn("no transformation", same["basis"])

    def test_an_undeclared_frame_is_said_to_be_assumed_by_the_fits_default(self):
        bare = self.variant("noframe.fits", lambda h: [h.remove(k) for k in ("RADESYS", "EQUINOX")])
        s = self.conv(bare, x=20, y=9, frame="galactic")["sky"]
        self.assertEqual((s["native"]["frame"], s["native"]["frame_status"]), ("ICRS", "assumed"))
        self.assertIn("assumed", s["frame_note"])
        self.assertIn("FITS standard", s["frame_note"])
        self.assertIsNone(self.conv(self.f["galactic_centre.fits"], frame="galactic")["sky"]["frame_note"])
        offers, _ = self.c.ask(op="conversions", path=str(bare))
        self.assertEqual(offers["sky"]["frame_status"], "assumed")

    def test_an_equinox_without_radesys_takes_the_fits_default_frame(self):
        def edit(h):
            h.remove("RADESYS")
            h["EQUINOX"] = 1950.0
        s = self.conv(self.variant("fk4.fits", edit), x=20, y=9, frame="icrs")["sky"]
        self.assertEqual((s["native"]["frame"], s["native"]["frame_status"]), ("FK4 (B1950)", "inferred"))
        native = SkyCoord(s["native"]["lon"] * u.deg, s["native"]["lat"] * u.deg, frame=FK4(equinox="B1950"))
        self.assertAlmostEqual(s["converted"]["lon"], native.icrs.ra.deg, places=9)
        self.assertGreater(abs(s["converted"]["lon"] - s["native"]["lon"]), 0.1)   # B1950 to ICRS moves positions by tenths of a degree

    def test_galactic_file_and_files_without_celestial_wcs(self):
        s = self.conv(self.f["hi_cube_galactic.fits"], x=3, y=3, frame="icrs")["sky"]
        self.assertEqual(s["native"]["frame"], "Galactic")
        native = SkyCoord(s["native"]["lon"] * u.deg, s["native"]["lat"] * u.deg, frame=Galactic())
        self.assertAlmostEqual(s["converted"]["lon"], native.icrs.ra.deg, places=9)
        s = self.conv(self.f["hi_cube_no_celestial_wcs.fits"], frame="icrs")["sky"]
        self.assertIsNone(s["converted"])
        self.assertIn("no complete celestial WCS", s["refused"])
        offers, _ = self.c.ask(op="conversions", path=str(self.f["hi_cube_no_celestial_wcs.fits"]))
        self.assertIsNone(offers["sky"])

    def test_nothing_requested_computes_nothing_and_the_file_is_not_changed(self):
        p = self.f["hi_cube_freq.fits"]
        before = p.read_bytes()
        r = self.conv(p)
        self.assertEqual((r["spectral"], r["sky"], r["value"]), (None, None, None))
        self.conv(p, spectral="km/s", convention="radio", frame="galactic", to="K", value=1.0)
        self.assertEqual(p.read_bytes(), before)


if __name__ == "__main__":
    unittest.main()
