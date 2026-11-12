"""HEALPix polarization maps sampled onto a celestial grid (ADR 0067): what the header must state, the COSMO to IAU flip, the turn from
Galactic north to the grid's north, the two products, the noise and the pixel ordering.

Independence: the expected angle at each pixel is the fixture's Galactic angle plus the position angle of the North Galactic Pole seen
from that pixel in ICRS (SkyCoord.position_angle), not the helper the code uses (orientation.rotate_pa_frame, which steps along a
direction and transforms it). The Gaussian kernel's sum of squared weights is recomputed by a brute-force loop over every pixel centre
with SkyCoord.separation, not by the cone search.
"""
import json
import math
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

import numpy as np
from astropy import units as u
from astropy.coordinates import SkyCoord
from astropy.io import fits
from astropy.wcs import WCS

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
sys.path.insert(0, str(HERE.parent.parent / "fixtures"))
import generate as g  # noqa: E402

NGP = SkyCoord(l=0 * u.deg, b=90 * u.deg, frame="galactic").icrs


def grid_header(ctype=("RA---TAN", "DEC--TAN"), crval=(80.9, -69.75), cdelt=2.0, n=9) -> fits.Header:
    """A wide n x n grid (2 degree pixels around the LMC by default), so north turns appreciably across it."""
    h = fits.PrimaryHDU(np.zeros((n, n), dtype=np.float32)).header
    for i, (ct, cv, cd) in enumerate(zip(ctype, crval, (-cdelt, cdelt)), start=1):
        h[f"CTYPE{i}"], h[f"CRVAL{i}"], h[f"CDELT{i}"], h[f"CRPIX{i}"], h[f"CUNIT{i}"] = ct, cv, cd, (n + 1) / 2, "deg"
    if ctype[0].startswith("RA"):
        h["RADESYS"] = "ICRS"
    return h


def ngp_pa(h: fits.Header) -> np.ndarray:
    """Position angle (ICRS, east of north) of the North Galactic Pole at every pixel centre of `h`."""
    w = WCS(h)
    ny, nx = h["NAXIS2"], h["NAXIS1"]
    ys, xs = np.mgrid[0:ny, 0:nx]
    return w.pixel_to_world(xs, ys).position_angle(NGP).deg


def wrap(a):
    return np.mod(a, 180.0)


def axial_close(a, b, tol):
    d = np.abs(((np.asarray(a) - np.asarray(b)) + 90.0) % 180.0 - 90.0)
    return bool(np.all(d < tol)), float(np.max(d))


class Base(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        cls.dir = Path(cls.tmp.name)
        cls.grid = cls.dir / "grid.fits"
        fits.PrimaryHDU(np.zeros((9, 9), dtype=np.float32), header=grid_header()).writeto(cls.grid)

    @classmethod
    def tearDownClass(cls):
        cls.tmp.cleanup()

    def write(self, name, hdul) -> Path:
        p = self.dir / name
        hdul.writeto(p, overwrite=True)
        return p

    def run_sample(self, src, params, target=None, companions=True):
        out = self.dir / f"out-{self.id()}.fits"
        cmd = [sys.executable, "-P", str(HERE / "analysis.py"), "healpix_sample", "--input", str(src), "--reference", str(target or self.grid),
               "--out", str(out), "--params", json.dumps(params)]
        if companions:
            cmd += ["--p-out", str(out.with_suffix(".p.fits")), "--sigma-out", str(out.with_suffix(".s.fits"))]
        r = subprocess.run(cmd, capture_output=True, text=True, timeout=120)
        res = json.loads(r.stdout)
        if not res["ok"]:
            return res, None
        arrays = {"angle": fits.getdata(out).astype(float)}
        if companions:
            arrays["p"] = fits.getdata(out.with_suffix(".p.fits")).astype(float)
            arrays["sigma_p"] = fits.getdata(out.with_suffix(".s.fits")).astype(float)
        return res, arrays


class Header(Base):
    def describe(self, hdul):
        sys.path.insert(0, str(HERE))
        from fits_header import describe
        return describe(str(self.write("h.fits", hdul)))

    def test_planck_map_is_described_as_its_own_kind(self):
        d = self.describe(g.healpix_pol_map())
        self.assertEqual(d["kind"], "healpix")
        self.assertEqual((d["nside"], d["npix"], d["ordering"], d["coordsys"], d["frame"], d["polcconv"]), (16, 3072, "RING", "GALACTIC", "galactic", "COSMO"))
        self.assertTrue(d["has_qu_cov"])
        self.assertEqual(d["stokes_unit"], "K_CMB")
        self.assertNotIn("axes", d)            # nothing pretends it is an image with a WCS

    def test_refusals_name_what_is_missing(self):
        from healpix_pol import HealpixRefused
        cases = {
            "POLCCONV": g.healpix_pol_map(polcconv=None),
            "COORDSYS": g.healpix_pol_map(coordsys=None),
            "neither COSMO nor IAU": g.healpix_pol_map(polcconv="HEALPIX"),
        }
        bad_nside = g.healpix_pol_map()
        bad_nside[1].header["NSIDE"] = 12
        cases["power of two"] = bad_nside
        explicit = g.healpix_pol_map()
        explicit[1].header["INDXSCHM"] = "EXPLICIT"
        cases["explicit pixel index"] = explicit
        rows = g.healpix_pol_map()
        rows[1].header["NSIDE"] = 32
        cases["one value per pixel"] = rows
        for what, hdul in cases.items():
            with self.subTest(what=what), self.assertRaises(HealpixRefused) as e:
                self.describe(hdul)
            self.assertIn(what, str(e.exception))
            self.assertIn("not registered", str(e.exception))

    def test_without_covariance_columns_it_is_refused(self):
        from healpix_pol import HealpixRefused
        h = g.healpix_pol_map()
        cols = fits.ColDefs([c for c in h[1].columns if c.name != "UU_COV"])
        t = fits.BinTableHDU.from_columns(cols, header=h[1].header)
        with self.assertRaises(HealpixRefused) as e:
            self.describe(fits.HDUList([fits.PrimaryHDU(), t]))
        self.assertIn("UU_COV", str(e.exception))

    def test_an_ordinary_fits_table_is_not_healpix(self):
        t = fits.BinTableHDU.from_columns([fits.Column(name="RA", format="E", array=np.zeros(3, dtype=np.float32))])
        d = self.describe(fits.HDUList([fits.PrimaryHDU(), t]))
        self.assertNotEqual(d.get("kind"), "healpix")


class Angle(Base):
    def test_uniform_galactic_field_on_icrs_grid_matches_ngp_position_angle(self):
        src = self.write("cosmo.fits", g.healpix_pol_map())
        delta = ngp_pa(grid_header())
        self.assertGreater(np.ptp(delta), 15.0)           # north turns across the grid: a missing per-pixel rotation would show
        expect = wrap(g.PLANCK_PSI_GAL_DEG + delta)
        for method, extra in (("nearest", {}), ("bilinear", {}), ("gaussian", {"fwhm_arcmin": 500.0})):
            with self.subTest(method=method):
                res, a = self.run_sample(src, {"product": "dust_polarization", "method": method, **extra})
                self.assertTrue(res["ok"], res)
                ok, worst = axial_close(a["angle"], expect, 1e-3)
                self.assertTrue(ok, f"{method}: worst {worst} deg")
                self.assertEqual(res["summary"]["angle_frame"], "sky")
                self.assertTrue(res["summary"]["derivation"]["convention"]["flipped"])

    def test_galactic_grid_needs_no_rotation(self):
        h = grid_header(ctype=("GLON-TAN", "GLAT-TAN"), crval=(280.0, -33.0))
        target = self.dir / "gal.fits"
        fits.PrimaryHDU(np.zeros((9, 9), dtype=np.float32), header=h).writeto(target, overwrite=True)
        res, a = self.run_sample(self.write("cosmo.fits", g.healpix_pol_map()), {"product": "dust_polarization"}, target)
        self.assertTrue(np.allclose(a["angle"], g.PLANCK_PSI_GAL_DEG, atol=1e-4))

    def test_cosmo_to_iau_flip(self):
        # psi = 30 deg is stored in COSMO as U = -P sin 60. Read as IAU it would be -30 deg: 60 degrees away at every pixel.
        delta = ngp_pa(grid_header())
        cosmo = self.write("cosmo.fits", g.healpix_pol_map(polcconv="COSMO"))
        iau = self.write("iau.fits", g.healpix_pol_map(polcconv="IAU"))
        _, a = self.run_sample(cosmo, {"product": "dust_polarization"})
        _, b = self.run_sample(iau, {"product": "dust_polarization"})
        self.assertTrue(axial_close(a["angle"], wrap(30.0 + delta), 1e-3)[0])
        self.assertTrue(axial_close(a["angle"], b["angle"], 1e-4)[0])
        unflipped = wrap(-30.0 + delta)
        d = np.abs(((a["angle"] - unflipped) + 90.0) % 180.0 - 90.0)
        self.assertTrue(np.all(d > 59.0))

    def test_b_field_is_dust_plus_90_and_axial(self):
        # a dust angle near 135 deg on the grid: the field orientation crosses 180 and must wrap into [0, 180)
        src = self.write("p135.fits", g.healpix_pol_map(psi_gal=float(np.mod(135.0 - np.median(ngp_pa(grid_header())), 180.0))))
        _, dust = self.run_sample(src, {"product": "dust_polarization", "method": "bilinear"})
        res, b = self.run_sample(src, {"product": "b_field", "method": "bilinear"})
        for arr in (dust["angle"], b["angle"]):
            self.assertTrue(np.all((arr >= 0) & (arr < 180)))
        self.assertTrue(np.allclose(b["angle"], np.mod(dust["angle"] + 90.0, 180.0), atol=1e-3))
        self.assertTrue(np.any(dust["angle"] + 90.0 >= 180.0))     # the case actually wraps somewhere
        self.assertEqual(res["summary"]["quantity"], "b_field")
        self.assertIn("+90 degrees", res["summary"]["policy"])

    def test_ring_and_nested_give_the_same_map(self):
        # a field that changes with position: an ignored ORDERING would put values at the wrong pixels
        def psi(l, b):
            return 20.0 + 0.9 * b + 0.4 * ((l + 180.0) % 360.0 - 180.0)
        ring = self.write("ring.fits", g.healpix_pol_map(ordering="RING", psi_gal=psi))
        nested = self.write("nested.fits", g.healpix_pol_map(ordering="NESTED", psi_gal=psi))
        _, a = self.run_sample(ring, {"product": "dust_polarization"})
        _, b = self.run_sample(nested, {"product": "dust_polarization"})
        self.assertTrue(np.allclose(a["angle"], b["angle"], atol=1e-4))
        # and both equal the field at the HEALPix pixel holding each target pixel, turned into ICRS
        from astropy.coordinates import Galactic
        from astropy_healpix import HEALPix
        h = grid_header()
        w = WCS(h)
        ys, xs = np.mgrid[0:9, 0:9]
        gal = w.pixel_to_world(xs, ys).galactic
        hp = HEALPix(16, "ring", Galactic())
        cl, cb = hp.healpix_to_lonlat(hp.lonlat_to_healpix(gal.l, gal.b))
        self.assertTrue(axial_close(a["angle"], wrap(psi(cl.deg, cb.deg) + ngp_pa(h)), 1e-3)[0])
        # the same NESTED data labelled RING is a different map
        mislabelled = g.healpix_pol_map(ordering="NESTED", psi_gal=psi)
        mislabelled[1].header["ORDERING"] = "RING"
        _, c = self.run_sample(self.write("mislabelled.fits", mislabelled), {"product": "dust_polarization"})
        self.assertGreater(np.nanmax(np.abs(((c["angle"] - a["angle"]) + 90) % 180 - 90)), 5.0)

    def test_many_values_per_row_reads_the_same_map(self):
        # Planck maps may store 1024 pixels per row (TFORM 1024E); NSIDE 16 is exactly 3 such rows
        def psi(l, b):
            return 20.0 + 0.9 * b + 0.4 * ((l + 180.0) % 360.0 - 180.0)
        one = self.write("one.fits", g.healpix_pol_map(psi_gal=psi))
        many = self.write("many.fits", g.healpix_pol_map(psi_gal=psi, per_row=1024))
        self.assertEqual(fits.getheader(many, 1)["NAXIS2"], 3)
        for method in ("nearest", "bilinear"):
            _, a = self.run_sample(one, {"product": "dust_polarization", "method": method})
            _, b = self.run_sample(many, {"product": "dust_polarization", "method": method})
            self.assertTrue(np.array_equal(a["angle"], b["angle"]) and np.array_equal(a["sigma_p"], b["sigma_p"]), method)

    def test_a_gaussian_near_the_galactic_pole_is_refused(self):
        # Galactic north turns fast across a kernel near the pole: averaging Q and U against it would depolarize, so it is refused
        h = grid_header(crval=(192.86, 27.13), cdelt=1.0, n=3)
        target = self.dir / "pole.fits"
        fits.PrimaryHDU(np.zeros((3, 3), dtype=np.float32), header=h).writeto(target, overwrite=True)
        src = self.write("cosmo.fits", g.healpix_pol_map())
        res, _ = self.run_sample(src, {"product": "dust_polarization", "method": "gaussian", "fwhm_arcmin": 300.0}, target)
        self.assertFalse(res["ok"])
        self.assertIn("depolarize", res["error"])
        res, _ = self.run_sample(src, {"product": "dust_polarization", "method": "bilinear"}, target)
        self.assertTrue(res["ok"], res)

    def test_refusals_at_sampling(self):
        src = self.write("cosmo.fits", g.healpix_pol_map())
        eq = self.write("eq.fits", g.healpix_pol_map(coordsys="C"))
        for params, words, path in (({"product": "angle"}, "dust_polarization", src),
                                    ({"product": "b_field", "method": "gaussian"}, "FWHM", src),
                                    ({"product": "b_field", "fwhm_arcmin": 30}, "gaussian method only", src),
                                    ({"product": "b_field", "method": "gaussian", "fwhm_arcmin": 30.0}, "smaller than this map's pixels", src),
                                    ({"product": "b_field"}, "only Galactic maps", eq)):
            with self.subTest(params=params):
                res, _ = self.run_sample(path, params)
                self.assertFalse(res["ok"])
                self.assertIn(words, res["error"])


class Noise(Base):
    def test_nearest_sigma_p_is_the_pixel_covariance(self):
        res, a = self.run_sample(self.write("cosmo.fits", g.healpix_pol_map()), {"product": "dust_polarization"})
        q = g.PLANCK_P * math.cos(math.radians(60))
        u_cosmo = -g.PLANCK_P * math.sin(math.radians(60))
        # sigma_P does not depend on the reference direction, so the file's own (COSMO) values give it: flipping U without QU would not
        sig = math.sqrt((q * q * g.PLANCK_QQ + u_cosmo ** 2 * g.PLANCK_UU + 2 * q * u_cosmo * g.PLANCK_QU) / g.PLANCK_P ** 2)
        wrong = math.sqrt((q * q * g.PLANCK_QQ + u_cosmo ** 2 * g.PLANCK_UU - 2 * q * u_cosmo * g.PLANCK_QU) / g.PLANCK_P ** 2)
        self.assertGreater(abs(sig - wrong) / sig, 0.05)
        self.assertTrue(np.allclose(a["p"], g.PLANCK_P, rtol=1e-5))
        self.assertTrue(np.allclose(a["sigma_p"], sig, rtol=1e-4), (a["sigma_p"][0, 0], sig))
        self.assertIn("not modelled", res["summary"]["derivation"]["noise"])
        self.assertIn("not debiased", res["summary"]["policy"])

    def test_gaussian_kernel_sum_of_squared_weights(self):
        # QQ = UU = s0^2 and QU = 0, so the covariance is s0^2 I in every frame and sigma_P^2 = s0^2 sum(w^2); one pixel near the
        # target has no value, so the weights are renormalised over the others
        from astropy.coordinates import Galactic
        from astropy_healpix import HEALPix
        hp = HEALPix(16, "ring", Galactic())
        s0 = 3.0e-5
        h = grid_header(n=3, cdelt=5.0, crval=(80.9, -69.75))
        corner = WCS(h).pixel_to_world(0, 0).galactic
        missing = (int(hp.lonlat_to_healpix(corner.l, corner.b)),)
        src = self.write("g.fits", g.healpix_pol_map(qq=s0 ** 2, uu=s0 ** 2, qu=0.0, missing=missing))
        target = self.dir / "g3.fits"
        fits.PrimaryHDU(np.zeros((3, 3), dtype=np.float32), header=h).writeto(target, overwrite=True)
        fwhm = 400.0
        res, a = self.run_sample(src, {"product": "dust_polarization", "method": "gaussian", "fwhm_arcmin": fwhm}, target)
        self.assertTrue(res["ok"], res)
        centres = hp.healpix_to_skycoord(np.arange(hp.npix))
        sigma = fwhm / 60.0 / (2 * math.sqrt(2 * math.log(2)))
        w = WCS(h)
        partial = 0
        for y in range(3):
            for x in range(3):
                pos = w.pixel_to_world(x, y).galactic
                d = pos.separation(centres).deg
                keep = d <= 3 * sigma
                keep[list(missing)] = False
                partial += int(any((pos.separation(centres[m]).deg <= 3 * sigma) for m in missing))
                wt = np.exp(-0.5 * (d[keep] / sigma) ** 2)
                wt /= wt.sum()
                self.assertGreater(keep.sum(), 10)
                self.assertAlmostEqual(a["sigma_p"][y, x] / (s0 * math.sqrt(np.sum(wt ** 2))), 1.0, places=4)
        self.assertGreater(partial, 0)                                  # the missing pixel is inside some kernel
        self.assertEqual(res["summary"]["counts"]["partial_kernel"], partial)

    def test_bilinear_sum_of_squared_weights(self):
        s0 = 2.0e-5
        res, a = self.run_sample(self.write("b.fits", g.healpix_pol_map(qq=s0 ** 2, uu=s0 ** 2, qu=0.0)), {"product": "b_field", "method": "bilinear"})
        from astropy.coordinates import Galactic
        from astropy_healpix import HEALPix
        hp = HEALPix(16, "ring", Galactic())
        w = WCS(grid_header())
        ys, xs = np.mgrid[0:9, 0:9]
        gal = w.pixel_to_world(xs, ys).galactic
        _, wt = hp.bilinear_interpolation_weights(gal.l, gal.b)
        self.assertTrue(np.allclose(a["sigma_p"], s0 * np.sqrt(np.sum(np.asarray(wt) ** 2, axis=0)), rtol=1e-4))

    def test_without_qu_covariance_it_is_taken_as_zero_and_said(self):
        src = self.write("noqu.fits", g.healpix_pol_map(qu_cov=False))
        res, a = self.run_sample(src, {"product": "dust_polarization"})
        self.assertTrue(res["ok"], res)
        q, u_ = g.PLANCK_P * math.cos(math.radians(60)), g.PLANCK_P * math.sin(math.radians(60))
        sig = math.sqrt((q * q * g.PLANCK_QQ + u_ * u_ * g.PLANCK_UU) / g.PLANCK_P ** 2)
        self.assertTrue(np.allclose(a["sigma_p"], sig, rtol=1e-4))
        self.assertFalse(res["summary"]["healpix"]["has_qu_cov"])
        self.assertIn("QU taken as 0", res["summary"]["policy"])
        self.assertIn("no QU_COV", res["summary"]["derivation"]["noise"])

    def test_missing_pixel_stays_missing(self):
        from astropy.coordinates import Galactic
        from astropy_healpix import HEALPix
        hp = HEALPix(16, "ring", Galactic())
        pos = WCS(grid_header()).pixel_to_world(4, 4).galactic
        pix = int(hp.lonlat_to_healpix(pos.l, pos.b))
        res, a = self.run_sample(self.write("m.fits", g.healpix_pol_map(missing=(pix,))), {"product": "dust_polarization"})
        self.assertTrue(np.isnan(a["angle"][4, 4]) and np.isnan(a["p"][4, 4]) and np.isnan(a["sigma_p"][4, 4]))
        self.assertGreaterEqual(res["summary"]["counts"]["no_healpix_value"], 1)
        self.assertEqual(int(np.isnan(a["angle"]).sum()), res["summary"]["counts"]["no_healpix_value"])

    def test_snr_threshold_blanks_the_angle_only(self):
        src = self.write("cosmo.fits", g.healpix_pol_map())
        res, a = self.run_sample(src, {"product": "b_field", "snr_min": 3.0})
        self.assertEqual(res["summary"]["snr_filter"]["below"], 0)
        self.assertFalse(np.isnan(a["angle"]).any())
        res, a = self.run_sample(src, {"product": "b_field", "snr_min": 100.0})
        self.assertTrue(np.isnan(a["angle"]).all())
        self.assertEqual(res["summary"]["snr_filter"]["below"], 81)
        self.assertFalse(np.isnan(a["p"]).any() or np.isnan(a["sigma_p"]).any())


if __name__ == "__main__":
    unittest.main()
