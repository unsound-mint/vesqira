"""Relative orientation (ADR 0047): golden cases computed independently in the tests.

Independence: expected numbers come from astropy and plain loops written here (SkyCoord.separation per pixel, the owner's
script transcribed line by line), never from relorient's own helpers.
"""
import json
import math
import subprocess
import sys
import tempfile
import types
import unittest
from pathlib import Path

import numpy as np
from astropy import units as u
from astropy.coordinates import SkyCoord
from astropy.io import fits
from astropy.wcs import WCS

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import relorient as ro  # noqa: E402

SCRIPT = str(HERE / "analysis.py")


def header(nx, ny, scale_deg, crval=(80.0, -69.0), rot_deg=0.0, extra=None):
    """North up, east left (rotated by rot_deg), ICRS, reference pixel in the middle."""
    h = fits.Header()
    h["CTYPE1"], h["CTYPE2"] = "RA---TAN", "DEC--TAN"
    h["CRVAL1"], h["CRVAL2"] = crval
    h["CRPIX1"], h["CRPIX2"] = nx // 2 + 1, ny // 2 + 1
    h["CUNIT1"] = h["CUNIT2"] = "deg"
    h["RADESYS"] = "ICRS"
    a = math.radians(rot_deg)
    cd = np.array([[-scale_deg, 0.0], [0.0, scale_deg]]) @ np.array([[math.cos(a), -math.sin(a)], [math.sin(a), math.cos(a)]])
    for i in range(2):
        for j in range(2):
            h[f"CD{i + 1}_{j + 1}"] = float(cd[i][j])
    for k, v in (extra or {}).items():
        h[k] = v
    return h


def field(nx, ny, seed):
    """A smooth, structured orientation field in degrees [0, 180) (pixel frame)."""
    r = np.random.default_rng(seed)
    yy, xx = np.mgrid[0:ny, 0:nx]
    t = np.zeros((ny, nx))
    for _ in range(4):
        kx, ky, ph, amp = r.uniform(0.05, 0.4), r.uniform(0.05, 0.4), r.uniform(0, 6.3), r.uniform(20, 60)
        t += amp * np.sin(kx * xx + ky * yy + ph)
    return (t + 90.0) % 180.0


def write_map(path, data, h):
    fits.PrimaryHDU(np.asarray(data, dtype="<f4"), header=h).writeto(path, overwrite=True)
    return str(path)


def write_catalog(path, ra, dec, pa, sigma=None):
    with open(path, "w") as f:
        f.write("ra,dec,pa" + (",spa" if sigma is not None else "") + "\n")
        for i in range(len(ra)):
            row = [repr(float(ra[i])), repr(float(dec[i])), "" if not np.isfinite(pa[i]) else repr(float(pa[i]))]
            if sigma is not None:
                row.append(repr(float(sigma[i])))
            f.write(",".join(row) + "\n")
    return str(path)


def decl(quantity="starlight_polarization", sigma=False, frame="icrs"):
    return {"lon": "ra", "lat": "dec", "format": "degrees", "frame": frame, "equinox": None,
            "angle": {"column": "pa", "unit": "deg", "frame": "sky", "quantity": quantity, "sigmaColumn": "spa" if sigma else None}, "values": []}


def args(**kw):
    base = dict(input=None, axial_unit="deg", axial_frame="pixel", axial_quantity="b_field", weights=None, catalog=None, declaration=None,
                out=None, reference=None, ref_axial_unit="deg", ref_axial_frame="pixel", ref_axial_quantity="b_field", ref_weights=None, table_out=None)
    base.update(kw)
    return types.SimpleNamespace(**base)


def sky_pa_of_pixel_angle(w, x, y, phi_deg):
    """Independent pixel-angle -> position angle: step along the pixel direction and ask SkyCoord.position_angle."""
    a = np.radians(phi_deg)
    p0 = w.pixel_to_world(x, y)
    p1 = w.pixel_to_world(x + 1e-3 * np.cos(a), y + 1e-3 * np.sin(a))
    return np.asarray(p0.position_angle(p1).deg) % 180.0


def axial_dist(a, b):
    d = np.abs((np.asarray(a) - np.asarray(b)) % 180.0)
    return np.minimum(d, 180.0 - d)


# ---------------------------------------------------------------------------------------------------- the owner's procedure
# Transcribed from ~/Projects/VGT/scripts/vgt_phase_vs_stars.py (disk_axial_mean lines 55-66, compare lines 83-117) and
# compare_vgt_polarization.py (headless 44-45, galactic_angle_to_pixel 55-64). Angles in radians, as there.

def owner_headless(d):
    return (d + np.pi / 2) % np.pi - np.pi / 2


def owner_angle_to_pixel(wcs, l, b, theta, frame):
    step = 1.0 / 3600.0
    l2 = l + step * np.sin(theta) / np.cos(np.radians(b))
    b2 = b + step * np.cos(theta)
    x1, y1 = wcs.world_to_pixel(SkyCoord(l * u.deg, b * u.deg, frame=frame))
    x2, y2 = wcs.world_to_pixel(SkyCoord(l2 * u.deg, b2 * u.deg, frame=frame))
    return np.arctan2(y2 - y1, x2 - x1)


def owner_disk_axial_mean(psi, x, y, r):
    ny, nx = psi.shape
    xi, yi = int(round(x)), int(round(y))
    ri = int(np.ceil(r))
    if xi - ri < 0 or yi - ri < 0 or xi + ri >= nx or yi + ri >= ny:
        return np.nan
    sub = psi[yi - ri:yi + ri + 1, xi - ri:xi + ri + 1]
    yy, xx = np.mgrid[-ri:ri + 1, -ri:ri + 1]
    v = sub[(xx ** 2 + yy ** 2 <= r ** 2) & np.isfinite(sub)]
    if v.size < 5:
        return np.nan
    return 0.5 * np.arctan2(np.sin(2 * v).sum(), np.cos(2 * v).sum())


def owner_compare(psi, wcs, lon, lat, pa, r_px, nshift, rng, deg_px):
    x, y = wcs.world_to_pixel(SkyCoord(lon * u.deg, lat * u.deg, frame="icrs"))
    phi = owner_angle_to_pixel(wcs, lon, lat, pa, frame="icrs")
    v = np.array([owner_disk_axial_mean(psi, xi, yi, r_px) for xi, yi in zip(x, y)])
    d = owner_headless(v - phi)
    ok = np.isfinite(d)
    am = float(np.mean(np.cos(2 * d[ok])))
    n_real = int(ok.sum())
    n_sub = int(0.9 * n_real)
    null = []
    ny, nx = psi.shape
    for _ in range(nshift):
        while True:
            dx, dy = rng.uniform(-nx, nx), rng.uniform(-ny, ny)
            if np.hypot(dx, dy) >= deg_px:
                break
        xs, ys = (x + dx) % nx, (y + dy) % ny
        vs = np.array([owner_disk_axial_mean(psi, a, b, r_px) for a, b in zip(xs, ys)])
        ds = owner_headless(vs - phi)
        oks = np.isfinite(ds)
        if oks.sum() >= n_sub:
            pick = rng.choice(np.where(oks)[0], n_sub, replace=False)
            null.append(np.mean(np.cos(2 * ds[pick])))
    null = np.array(null)
    return am, float(np.percentile(null, 95)), n_real, len(null), float((np.sum(null >= am) + 1) / (len(null) + 1))


class Base(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.dir = Path(self._tmp.name)

    def tearDown(self):
        self._tmp.cleanup()

    def p(self, name):
        return str(self.dir / name)


class Statistics(Base):
    def test_projected_rayleigh_matches_a_hand_computed_case_and_the_paper_definition(self):
        d = np.array([0.0, 90.0, 45.0, 30.0])
        w = np.array([1.0, 1.0, 1.0, 2.0])
        # Σ w cos 2Δψ = 1 - 1 + 0 + 2 * 0.5 = 1; Σ w² = 7  ->  V = 1 / sqrt(3.5)  (Jow et al. 2018 eq. 8)
        self.assertAlmostEqual(ro.projected_rayleigh(d, w), 1.0 / math.sqrt(3.5), places=12)
        # unweighted: Σ cos 2Δψ = 0.5, n = 4  ->  0.5 / sqrt(2)  (eq. 6)
        self.assertAlmostEqual(ro.projected_rayleigh(d), 0.5 / math.sqrt(2.0), places=12)
        # eq. 6: under uniform independent angles V is asymptotically standard normal
        r = np.random.default_rng(7)
        v = np.array([ro.projected_rayleigh(r.uniform(-90, 90, 200)) for _ in range(3000)])
        self.assertLess(abs(v.mean()), 0.08)
        self.assertLess(abs(v.var() - 1.0), 0.1)

    def test_alignment_measure_one_and_179_degrees_count_as_aligned(self):
        d = ro.headless_deg(np.array([1.0]) - np.array([179.0]))
        self.assertAlmostEqual(abs(float(d[0])), 2.0, places=9)
        self.assertGreater(ro.alignment_measure(d), 0.99)
        self.assertAlmostEqual(ro.alignment_measure([0.0, 90.0]), 0.0, places=12)
        self.assertAlmostEqual(ro.alignment_measure([10.0, 20.0], [3.0, 1.0]), (3 * math.cos(math.radians(20)) + math.cos(math.radians(40))) / 4, places=12)
        self.assertIsNone(ro.alignment_measure([np.nan]))

    def test_histogram_bins_are_equal_width_and_include_ninety(self):
        h = ro.histogram_block(np.array([0.0, 9.99, 10.0, 45.0, 89.9, 90.0]), 9)
        self.assertEqual(h["counts"], [2, 1, 0, 0, 1, 0, 0, 0, 2])
        self.assertEqual(h["edges_deg"], [10.0 * i for i in range(10)])

    def test_parameters_are_checked_and_defaults_stated(self):
        p = ro.check_params({}, "catalog")
        self.assertEqual((p["n_shifts"], p["min_shift_deg"], p["keep_fraction"], p["bins"], p["min_pixels"]), (3000, 1.0, 0.9, 9, 5))
        for bad in ({"n_shifts": 2.5}, {"keep_fraction": 0}, {"bins": 1}, {"radius_arcmin": -1}, {"seed": "x"}, {"radius_arcmin": float("nan")}):
            with self.assertRaises(ro.Refusal):
                ro.check_params(bad, "catalog")


class Disk(Base):
    def test_disk_mean_matches_a_direct_loop_by_sky_distance_with_reliability_weights(self):
        n = 31
        h = header(n, n, 1.0 / 60.0, rot_deg=20.0)
        w = WCS(h)
        phi = field(n, n, 3)
        phi[10:13, 14:16] = np.nan
        rel = np.random.default_rng(4).uniform(0.1, 1.0, (n, n))
        rel[15, 15] = np.nan
        radius = 4.3 / 60.0
        disks = ro.Disks(w, phi, rel, radius, 5)
        xs, ys = np.array([15.2, 9.6, 21.4, 3.0]), np.array([14.7, 20.1, 8.8, 15.0])
        theta, npx, R, st, _ = disks.sample(xs, ys)
        yy, xx = np.mgrid[0:n, 0:n]
        allpix = w.pixel_to_world(xx.ravel(), yy.ravel())
        for i in range(3):
            c = w.pixel_to_world(round(xs[i]), round(ys[i]))
            sep = c.separation(allpix).deg.reshape(n, n)
            m = (sep <= radius) & np.isfinite(phi) & np.isfinite(rel)
            ww, t = rel[m], np.radians(2 * phi[m])
            expect = (0.5 * math.degrees(math.atan2((ww * np.sin(t)).sum(), (ww * np.cos(t)).sum()))) % 180.0
            self.assertEqual(st[i], "")
            self.assertEqual(npx[i], int(m.sum()))
            self.assertLess(axial_dist(theta[i], expect), 1e-9)
            self.assertAlmostEqual(R[i], math.hypot((ww * np.sin(t)).sum(), (ww * np.cos(t)).sum()) / ww.sum(), places=12)
        self.assertEqual(st[3], "edge")                 # its search box (ri = 5) leaves the map, as in the owner's script


class CatalogMode(Base):
    """Synthetic map plus catalog; the catalog angles are set from the map with an independent astropy conversion."""

    def build(self, star_pa, quantity_map="b_field", quantity_cat="starlight_polarization", n=61, nstar=80, seed=1, sigma=None):
        h = header(n, n, 1.0 / 60.0, rot_deg=25.0)
        w = WCS(h)
        phi = field(n, n, seed)
        m = write_map(self.p("map.fits"), phi, h)
        r = np.random.default_rng(seed + 100)
        xs, ys = r.uniform(8, n - 9, nstar), r.uniform(8, n - 9, nstar)
        sky = w.pixel_to_world(xs, ys)
        pa = star_pa(w, xs, ys, phi, r)
        c = write_catalog(self.p("cat.csv"), sky.ra.deg, sky.dec.deg, pa, sigma)
        return m, c, w, phi

    def run_op(self, m, c, quantity_map="b_field", quantity_cat="starlight_polarization", params=None, sigma=False, frame="pixel"):
        a = args(input=m, catalog=c, declaration=json.dumps(decl(quantity_cat, sigma)), out=self.p("out.csv"),
                 axial_quantity=quantity_map, axial_frame=frame)
        return ro.op_catalog(a, {"radius_arcmin": 2.0, "n_shifts": 0, **(params or {})})["summary"]

    @staticmethod
    def b_at(w, xs, ys, phi):
        """The map's B orientation at each star (nearest pixel) as a sky position angle."""
        return sky_pa_of_pixel_angle(w, xs, ys, phi[np.rint(ys).astype(int), np.rint(xs).astype(int)])

    def test_aligned_stars_give_am_near_one_and_perpendicular_near_minus_one(self):
        m, c, w, phi = self.build(lambda w, xs, ys, phi, r: self.b_at(w, xs, ys, phi))
        s = self.run_op(m, c)
        self.assertGreater(s["am"], 0.98)
        self.assertEqual(s["label"], "agreement statistic")
        m, c, w, phi = self.build(lambda w, xs, ys, phi, r: (self.b_at(w, xs, ys, phi) + 90.0) % 180.0)
        self.assertLess(self.run_op(m, c)["am"], -0.98)

    def test_random_star_angles_give_am_near_zero(self):
        m, c, w, phi = self.build(lambda w, xs, ys, phi, r: r.uniform(0, 180, xs.size), nstar=400, n=81)
        s = self.run_op(m, c)
        n = s["counts"]["used"]
        self.assertGreater(n, 300)
        self.assertLess(abs(s["am"]), 4 * math.sqrt(0.5 / n))

    def test_dust_is_rotated_ninety_degrees_and_starlight_is_not(self):
        # the map is declared as dust polarization (E-vector, perpendicular to B), so B = map + 90
        m, c, w, phi = self.build(lambda w, xs, ys, phi, r: (self.b_at(w, xs, ys, phi) + 90.0) % 180.0)
        self.assertGreater(self.run_op(m, c, quantity_map="dust_polarization")["am"], 0.98)
        # the catalog declared as dust polarization: its B is the catalog angle + 90
        self.assertGreater(self.run_op(m, c, quantity_cat="dust_polarization")["am"], 0.98)
        self.assertLess(self.run_op(m, c, quantity_cat="b_field")["am"], -0.98)

    def test_a_sky_frame_map_and_a_galactic_catalog_are_carried_into_one_frame(self):
        n = 41
        h = header(n, n, 1.0 / 60.0, rot_deg=0.0)
        w = WCS(h)
        phi = field(n, n, 9)
        sky_map = sky_pa_of_pixel_angle(w, *np.mgrid[0:n, 0:n][::-1].reshape(2, -1).astype(float), phi.ravel()).reshape(n, n)
        m = write_map(self.p("skymap.fits"), sky_map, h)
        r = np.random.default_rng(5)
        xs, ys = r.uniform(6, n - 7, 60), r.uniform(6, n - 7, 60)
        icrs = w.pixel_to_world(xs, ys)
        pa_icrs = self.b_at(w, xs, ys, phi)
        gal = icrs.galactic
        tip = icrs.directional_offset_by(pa_icrs * u.deg, 1e-4 * u.deg).galactic   # independent: carry the direction by hand here
        pa_gal = gal.position_angle(tip).deg % 180.0
        with open(self.p("gal.csv"), "w") as f:
            f.write("ra,dec,pa\n")
            for i in range(60):
                f.write(f"{float(gal.l.deg[i])!r},{float(gal.b.deg[i])!r},{float(pa_gal[i])!r}\n")
        a = args(input=m, catalog=self.p("gal.csv"), declaration=json.dumps(decl(frame="galactic")), out=self.p("o.csv"), axial_frame="sky")
        s = ro.op_catalog(a, {"radius_arcmin": 1.0, "n_shifts": 0, "min_pixels": 1})["summary"]
        self.assertGreater(s["am"], 0.995)

    def test_exclusions_are_counted_with_reasons_and_every_row_is_in_the_table(self):
        n = 41
        h = header(n, n, 1.0 / 60.0)
        w = WCS(h)
        phi = field(n, n, 2)
        m = write_map(self.p("map.fits"), phi, h)
        xs, ys = np.array([20.0, 1.0, 20.0, 100.0]), np.array([20.0, 20.0, 20.0, 20.0])
        sky = w.pixel_to_world(xs, ys)
        pa = np.array([10.0, 10.0, np.nan, 10.0])
        c = write_catalog(self.p("cat.csv"), sky.ra.deg, sky.dec.deg, pa)
        s = self.run_op(m, c)
        ex = s["counts"]["excluded"]
        self.assertEqual(s["counts"]["used"], 1)
        self.assertEqual(ex[ro.REASONS["edge"]], 1)
        self.assertEqual(ex[ro.REASONS["angle"]], 1)
        self.assertEqual(ex[ro.REASONS["outside"]], 1)
        lines = Path(self.p("out.csv")).read_text().splitlines()
        self.assertEqual(len(lines), 5)
        self.assertTrue(lines[1].split(",")[-2] == "1" and lines[2].endswith(ro.REASONS["edge"]))

    def test_sigma_weights_follow_the_owners_floor(self):
        m, c, w, phi = self.build(lambda w, xs, ys, phi, r: self.b_at(w, xs, ys, phi), sigma=np.full(80, 0.5))
        s = self.run_op(m, c, sigma=True)
        self.assertIsNotNone(s["am_weighted"])
        row = Path(self.p("out.csv")).read_text().splitlines()[1].split(",")
        self.assertAlmostEqual(float(row[15]), 1.0 / math.radians(1.0) ** 2, places=2)    # σ = 0.5° floored at 1°

    def test_refusals_name_what_is_missing(self):
        m, c, w, phi = self.build(lambda w, xs, ys, phi, r: self.b_at(w, xs, ys, phi))
        cases = [
            (dict(axial_frame=None), "frame"),
            (dict(axial_quantity="unspecified"), "unspecified"),
            (dict(axial_quantity=None), "what the angle is"),
        ]
        for kw, word in cases:
            a = args(input=m, catalog=c, declaration=json.dumps(decl()), out=self.p("x.csv"), **kw)
            with self.assertRaises(ro.Refusal) as e:
                ro.op_catalog(a, {"n_shifts": 0})
            self.assertIn(word, str(e.exception))
        for d, word in ((decl("unspecified"), "unspecified"), (decl(None), "not declared"), ({**decl(), "angle": None}, "angle")):
            a = args(input=m, catalog=c, declaration=json.dumps(d), out=self.p("x.csv"))
            with self.assertRaises(ro.Refusal) as e:
                ro.op_catalog(a, {"n_shifts": 0})
            self.assertIn(word, str(e.exception))
        a = args(input=m, catalog=c, declaration=json.dumps(decl()), out=self.p("x.csv"))
        with self.assertRaises(ro.Refusal):
            ro.op_catalog(a, {"n_shifts": 10, "min_shift_deg": 5.0, "radius_arcmin": 2.0})      # longer than the map's diagonal


class OwnerNull(Base):
    def test_the_shifted_pattern_null_reproduces_the_owners_script_with_the_same_seed(self):
        # A rotated grid (north turns across the map), a structured field with holes, stars inside and outside the map.
        # r = 2.5 pixels: no pixel centre lies near the disk edge (squared offsets 5 and 8 bracket 6.25), so the owner's
        # pixel-distance disk around the rounded pixel and the sky-distance disk select the same pixels.
        nx, ny, scale = 40, 30, 3.0 / 60.0
        h = header(nx, ny, scale, rot_deg=30.0)
        w = WCS(h)
        psi_deg = field(nx, ny, 11)
        psi_deg[3:7, 20:26] = np.nan
        m = write_map(self.p("map.fits"), psi_deg, h)
        r = np.random.default_rng(12)
        xs = np.concatenate([r.uniform(2, nx - 3, 22), [-5.0, nx + 4.0]])
        ys = np.concatenate([r.uniform(2, ny - 3, 22), [10.0, 12.0]])
        sky = w.pixel_to_world(xs, ys)
        pa = r.uniform(0, 180, xs.size)
        c = write_catalog(self.p("cat.csv"), sky.ra.deg, sky.dec.deg, pa)
        seed, nshift, min_deg = 2024, 150, 0.5
        a = args(input=m, catalog=c, declaration=json.dumps(decl("b_field")), out=self.p("o.csv"))
        s = ro.op_catalog(a, {"radius_arcmin": 2.5 * scale * 60.0, "n_shifts": nshift, "min_shift_deg": min_deg, "seed": seed})["summary"]
        from astropy.wcs.utils import proj_plane_pixel_scales
        deg_px = min_deg / float(np.mean(proj_plane_pixel_scales(w)))
        am, chance, n_real, n_null, pval = owner_compare(np.radians(psi_deg), w, sky.ra.deg, sky.dec.deg, np.radians(pa), 2.5, nshift,
                                                         np.random.default_rng(seed), deg_px)
        self.assertEqual(s["counts"]["used"], n_real)
        self.assertAlmostEqual(s["am"], am, places=6)
        self.assertEqual(s["null"]["kept"], n_null)
        self.assertGreater(n_null, 20)
        # The star angle reaches the pixel frame by a 1-arcsec flat-sky step there and by astropy directional_offset_by here; the
        # two differ by about 1e-4 degrees at this declination, which moves AM values in the sixth decimal.
        self.assertAlmostEqual(s["null"]["chance95"], chance, places=4)
        self.assertEqual(s["null"]["p_one_sided"], pval)
        self.assertEqual(s["null"]["n_sub"], int(0.9 * n_real))

    def test_null_p_is_roughly_uniform_when_the_catalog_is_unrelated_to_the_map(self):
        nx = ny = 50
        h = header(nx, ny, 2.0 / 60.0)
        w = WCS(h)
        m = write_map(self.p("map.fits"), field(nx, ny, 21), h)
        ps = []
        for k in range(30):
            r = np.random.default_rng(1000 + k)
            xs, ys = r.uniform(4, nx - 5, 40), r.uniform(4, ny - 5, 40)
            sky = w.pixel_to_world(xs, ys)
            c = write_catalog(self.p(f"c{k}.csv"), sky.ra.deg, sky.dec.deg, r.uniform(0, 180, 40))
            a = args(input=m, catalog=c, declaration=json.dumps(decl()), out=self.p(f"o{k}.csv"))
            s = ro.op_catalog(a, {"radius_arcmin": 6.0, "n_shifts": 199, "min_shift_deg": 0.3, "seed": k})["summary"]
            ps.append(s["null"]["p_one_sided"])
        ps = np.array(ps)
        # 30 draws of a uniform variable: mean 0.5 with sd 0.053; allow 3.5 sd, and few small p
        self.assertLess(abs(ps.mean() - 0.5), 0.19)
        self.assertLessEqual(int((ps < 0.05).sum()), 5)

    def test_same_seed_same_numbers_different_seed_different_null(self):
        nx = ny = 40
        h = header(nx, ny, 2.0 / 60.0)
        w = WCS(h)
        m = write_map(self.p("map.fits"), field(nx, ny, 5), h)
        r = np.random.default_rng(3)
        xs, ys = r.uniform(4, nx - 5, 30), r.uniform(4, ny - 5, 30)
        sky = w.pixel_to_world(xs, ys)
        c = write_catalog(self.p("c.csv"), sky.ra.deg, sky.dec.deg, r.uniform(0, 180, 30))

        def go(seed):
            a = args(input=m, catalog=c, declaration=json.dumps(decl()), out=self.p("o.csv"))
            return ro.op_catalog(a, {"radius_arcmin": 6.0, "n_shifts": 100, "min_shift_deg": 0.3, "seed": seed})["summary"]
        s1, s2, s3 = go(7), go(7), go(8)
        self.assertEqual(json.dumps(s1, sort_keys=True), json.dumps(s2, sort_keys=True))
        self.assertNotEqual(s1["null"]["mean"], s3["null"]["mean"])
        self.assertEqual(s1["am"], s3["am"])


class MapMode(Base):
    def maps(self, b_of_a, n=48, rel=None, beam=True):
        extra = {"BMAJ": 3.0 / 60.0, "BMIN": 2.0 / 60.0, "BPA": 0.0} if beam else {}
        h = header(n, n, 1.0 / 60.0, extra=extra)
        a = field(n, n, 31)
        a[0:3, 0:5] = np.nan
        b = b_of_a(a)
        pa, pb = write_map(self.p("a.fits"), a, h), write_map(self.p("b.fits"), b, h)
        rp = write_map(self.p("rel.fits"), rel, header(n, n, 1.0 / 60.0)) if rel is not None else None
        return pa, pb, rp, a, b

    def run_op(self, pa, pb, params=None, rp=None, **kw):
        a = args(input=pa, reference=pb, out=self.p("d.fits"), table_out=self.p("t.csv"), weights=rp, **kw)
        return ro.op_maps(a, {"n_shifts": 0, **(params or {})})["summary"]

    def test_per_pixel_statistics_match_a_direct_computation(self):
        rel = np.random.default_rng(2).uniform(0.2, 1.0, (48, 48))
        noise = np.random.default_rng(9).normal(0, 20, (48, 48))
        pa, pb, rp, a, b = self.maps(lambda a: (a + noise) % 180.0, rel=rel)
        s = self.run_op(pa, pb, rp=rp)
        d = (a - b + 90.0) % 180.0 - 90.0
        ok = np.isfinite(d)
        c = np.cos(np.radians(2 * d[ok]))
        self.assertAlmostEqual(s["am"], c.mean(), places=9)
        ww = rel[ok]
        self.assertAlmostEqual(s["am_weighted"], (ww * c).sum() / ww.sum(), places=7)   # the reliability map is stored as float32
        self.assertAlmostEqual(s["prs"]["V"], c.sum() / math.sqrt(ok.sum() / 2.0), places=6)
        self.assertAlmostEqual(s["prs"]["V_weighted"], (ww * c).sum() / math.sqrt((ww ** 2).sum() / 2.0), places=6)
        self.assertEqual(s["counts"]["used"], int(ok.sum()))
        self.assertEqual(sum(s["histogram"]["counts"]), int(ok.sum()))
        out = fits.getdata(self.p("d.fits")).astype(float)
        np.testing.assert_allclose(out[ok], np.abs(d[ok]), atol=1e-4)
        self.assertTrue(np.isnan(out[~ok]).all())
        # beams: n × pixel area / (π/(4 ln 2) × BMAJ × BMIN), the owner's 1.1331 FWHM² approximation
        expect = ok.sum() * (1.0 / 60.0) ** 2 / (1.1331 * (3.0 / 60.0) * (2.0 / 60.0))
        self.assertAlmostEqual(s["independent_samples"]["value"], expect, delta=expect * 1e-3)
        self.assertEqual(len(Path(self.p("t.csv")).read_text().splitlines()), ok.sum() + 1)

    def test_aligned_maps_beat_the_shifted_null_and_unrelated_maps_do_not(self):
        noise = np.random.default_rng(1).normal(0, 10, (48, 48))
        pa, pb, _, _, _ = self.maps(lambda a: (a + noise) % 180.0)
        s = self.run_op(pa, pb, {"n_shifts": 99, "min_shift_deg": 0.1})
        self.assertGreater(s["am"], 0.9)
        self.assertEqual(s["null"]["p_one_sided"], 0.01)
        self.assertLess(s["null"]["chance95"], s["am"])
        other = field(48, 48, 77)
        pa, pb, _, _, _ = self.maps(lambda a: other)
        s = self.run_op(pa, pb, {"n_shifts": 99, "min_shift_deg": 0.1})
        self.assertGreater(s["null"]["p_one_sided"], 0.01)

    def test_one_and_179_and_the_dust_rotation(self):
        pa, pb, _, a, _ = self.maps(lambda a: (a + 178.0) % 180.0)
        self.assertGreater(self.run_op(pa, pb)["am"], 0.99)                    # 2° apart, not 178
        pa, pb, _, _, _ = self.maps(lambda a: (a + 90.0) % 180.0)
        self.assertLess(self.run_op(pa, pb)["am"], -0.99)
        self.assertGreater(self.run_op(pa, pb, ref_axial_quantity="dust_polarization")["am"], 0.99)

    def test_no_beam_no_count_and_different_grids_are_refused(self):
        pa, pb, _, _, _ = self.maps(lambda a: a, beam=False)
        s = self.run_op(pa, pb)
        self.assertIsNone(s["independent_samples"]["value"])
        self.assertIn("BMAJ", s["independent_samples"]["reason"])
        small = write_map(self.p("small.fits"), np.zeros((10, 12)), header(12, 10, 1.0 / 60.0))
        with self.assertRaises(ro.Refusal) as e:
            self.run_op(pa, small)
        self.assertIn("align", str(e.exception))
        with self.assertRaises(ro.Refusal) as e:
            self.run_op(pa, pb, ref_axial_frame=None)
        self.assertIn("second map", str(e.exception))

    def test_the_map_null_is_deterministic(self):
        noise = np.random.default_rng(1).normal(0, 40, (48, 48))
        pa, pb, _, _, _ = self.maps(lambda a: (a + noise) % 180.0)
        s1 = self.run_op(pa, pb, {"n_shifts": 50, "min_shift_deg": 0.1, "seed": 3})
        s2 = self.run_op(pa, pb, {"n_shifts": 50, "min_shift_deg": 0.1, "seed": 3})
        self.assertEqual(s1["null"], s2["null"])


class Program(Base):
    def test_the_command_line_writes_the_table_and_refuses_cleanly(self):
        n = 41
        h = header(n, n, 1.0 / 60.0)
        w = WCS(h)
        m = write_map(self.p("map.fits"), field(n, n, 1), h)
        sky = w.pixel_to_world(np.array([20.0, 25.0]), np.array([20.0, 15.0]))
        c = write_catalog(self.p("cat.csv"), sky.ra.deg, sky.dec.deg, np.array([10.0, 20.0]))
        base = [sys.executable, "-P", SCRIPT, "relorient_catalog", "--input", m, "--axial-unit", "deg", "--axial-frame", "pixel",
                "--catalog", c, "--declaration", json.dumps(decl()), "--params", json.dumps({"n_shifts": 0, "radius_arcmin": 2}), "--out", self.p("o.csv")]
        p = subprocess.run(base + ["--axial-quantity", "b_field"], capture_output=True, text=True, timeout=120)
        r = json.loads(p.stdout)
        self.assertEqual(p.returncode, 0, r)
        self.assertEqual(r["summary"]["counts"]["used"], 2)
        self.assertIn("astropy", r["software"])
        p = subprocess.run(base, capture_output=True, text=True, timeout=120)
        self.assertEqual(p.returncode, 2)
        self.assertIn("what the angle is", json.loads(p.stdout)["error"])


if __name__ == "__main__":
    unittest.main()
