"""Point catalogs (ADR 0043): every format, sexagesimal hours versus degrees, a Galactic table, unreadable rows reported with their
line, caps that refuse, a hostile VOTable, and frame transforms checked against astropy built the long way round in the tests."""
import gzip
import json
import sys
import tempfile
import unittest
from pathlib import Path

import numpy as np
from astropy import units as u
from astropy.coordinates import FK5, Galactic, SkyCoord
from astropy.io import fits
from astropy.table import Table

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import catalog_table as ct  # noqa: E402
from test_plane_worker import Client  # noqa: E402

STARLIGHT = (
    "#Star\tRA(J2000)\tDEC(J2000)\tP(%)\tsP(%)\tPA(deg)\tsPA(deg)\n"
    "67-2\t4 47 07.65\t-67 06 23.31\t1.17\t0.08\t25\t4\t\t\t\n"
    "69-7\t4 49 15.83\t-69 27 27.92\t1.30\t0.09\t38\t7\n"
    "67-5\t4 50 17.23\t-67 39 29.93\t0.79\t0.16\t61\t6\t\n"
    "69-16\t4 52 03.57\t-69 26 33.76\t1.12\t0.08\t41\t\n"
)
SEXA_DECL = {"lon": "RA(J2000)", "lat": "DEC(J2000)", "format": "sexagesimal_hours_degrees", "frame": "fk5", "equinox": "J2000",
             "angle": {"column": "PA(deg)", "unit": "deg", "quantity": "starlight_polarization", "sigmaColumn": "sPA(deg)"}}


def write(d: Path, name: str, content) -> str:
    p = d / name
    p.write_bytes(content if isinstance(content, bytes) else content.encode())
    return str(p)


def votable(rows="<TR><TD>1</TD><TD>10.5</TD></TR>", extra_attr="", head="", field_extra=""):
    return f"""<?xml version="1.0"?>
{head}<VOTABLE version="1.4" xmlns="http://www.ivoa.net/xml/VOTable/v1.3"><RESOURCE><TABLE {extra_attr}>
<FIELD name="id" datatype="int"/><FIELD name="ra" datatype="double" unit="deg" {field_extra}/>
<DATA><TABLEDATA>{rows}</TABLEDATA></DATA></TABLE></RESOURCE></VOTABLE>"""


class Reading(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        cls.d = Path(cls.tmp.name)

    @classmethod
    def tearDownClass(cls):
        cls.tmp.cleanup()

    def desc(self, name, content):
        return ct.op_describe(write(self.d, name, content), {})

    def test_starlight_table_commented_header_trailing_tabs_and_a_missing_last_value(self):
        doc = self.desc("sl.dat", STARLIGHT)
        self.assertEqual(doc["format"], "tsv")
        self.assertEqual(doc["rows"], 4)
        self.assertEqual([c["name"] for c in doc["columns"]], ["Star", "RA(J2000)", "DEC(J2000)", "P(%)", "sP(%)", "PA(deg)", "sPA(deg)"])
        self.assertEqual(doc["read"]["skippedTotal"], 0)
        # the last row's sPA is empty: missing, not 0
        self.assertIsNone(doc["preview"]["rows"][3][6])

    def test_csv_tsv_text_ecsv_votable_fits_and_ipac(self):
        want = [1.5, 2.5]
        cases = {
            "a.csv": "id,ra\n1,1.5\n2,2.5\n",
            "b.tsv": "id\tra\n1\t1.5\n2\t2.5\n",
            "c.dat": "id ra\n1 1.5\n2 2.5\n",
            "d.ecsv": "# %ECSV 1.0\n# ---\n# datatype:\n# - {name: id, datatype: int64}\n# - {name: ra, datatype: float64, unit: deg}\nid ra\n1 1.5\n2 2.5\n",
            "e.xml": votable("<TR><TD>1</TD><TD>1.5</TD></TR><TR><TD>2</TD><TD>2.5</TD></TR>"),
            "f.tbl": "|id  |ra   |\n|int |real |\n|    |deg  |\n|null|null |\n 1    1.5\n 2    2.5\n",
        }
        for name, text in cases.items():
            with self.subTest(name):
                p = write(self.d, name, text)
                L = ct.load(p)
                self.assertEqual(len(L.table), 2)
                self.assertEqual(list(np.asarray(L.table["ra"], dtype=float)), want)
        t = Table({"id": [1, 2], "ra": [1.5, 2.5]})
        t["ra"].unit = "deg"
        p = str(self.d / "g.fits")
        fits.HDUList([fits.PrimaryHDU(), fits.table_to_hdu(t)]).writeto(p, overwrite=True)
        L = ct.load(p)
        self.assertEqual((L.fmt, len(L.table)), ("fits", 2))

    def test_units_are_reported_only_when_the_file_declares_them(self):
        e = ct.op_describe(write(self.d, "u.ecsv", "# %ECSV 1.0\n# ---\n# datatype:\n# - {name: ra, datatype: float64, unit: deg}\n# - {name: x, datatype: float64}\nra x\n1 2\n"), {})
        self.assertEqual([c["unit"] for c in e["columns"]], ["deg", None])
        v = ct.op_describe(write(self.d, "u.vot", votable()), {})
        self.assertEqual([c["unit"] for c in v["columns"]], [None, "deg"])
        c = self.desc("nou.csv", "a,b\n1,2\n")
        self.assertEqual([x["unit"] for x in c["columns"]], [None, None])

    def test_a_headerless_file_gets_numbered_columns_and_a_note_and_no_proposal(self):
        doc = self.desc("nohead.txt", "1 69.1968 -66.4503 0.04\n2 73.1145 -69.4266 0.6\n")
        self.assertTrue(doc["read"]["headerless"])
        self.assertEqual([c["name"] for c in doc["columns"]], ["col1", "col2", "col3", "col4"])
        self.assertIn("no header", doc["read"]["notes"][0])
        self.assertIsNone(doc["proposal"]["lon"])

    def test_malformed_lines_are_reported_with_their_line_number_and_the_rest_is_read(self):
        text = "# a comment\nname\tra\tdec\nx\t1.0\t2.0\ny\t3.0\nz\t5.0\t6.0\tjunk\nw\t7.0\t8.0\n"
        doc = self.desc("bad.tsv", text)
        self.assertEqual(doc["rows"], 2)
        self.assertEqual(doc["read"]["skippedTotal"], 2)
        self.assertEqual([s["line"] for s in doc["read"]["skipped"]], [4, 5])
        self.assertIn("fields", doc["read"]["skipped"][0]["reason"])
        L = ct.load(str(self.d / "bad.tsv"))
        self.assertEqual(list(L.linenos), [3, 6])

    def test_a_text_table_row_with_the_wrong_field_count_is_reported(self):
        doc = self.desc("ws.dat", "a b c\n1 2 3\n4 5\n6 7 8\n")
        self.assertEqual((doc["rows"], [s["line"] for s in doc["read"]["skipped"]]), (2, [3]))


class Positions(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        cls.d = Path(cls.tmp.name)
        cls.star = write(cls.d, "sl.dat", STARLIGHT)

    @classmethod
    def tearDownClass(cls):
        cls.tmp.cleanup()

    def test_sexagesimal_hours_are_not_read_as_degrees(self):
        p = ct.op_positions(self.star, {"declaration": SEXA_DECL})
        want = SkyCoord(["4 47 07.65", "4 49 15.83", "4 50 17.23", "4 52 03.57"], ["-67 06 23.31", "-69 27 27.92", "-67 39 29.93", "-69 26 33.76"], unit=(u.hourangle, u.deg), frame=FK5(equinox="J2000"))
        np.testing.assert_allclose(p["lon"], want.ra.deg, atol=1e-9)
        np.testing.assert_allclose(p["lat"], want.dec.deg, atol=1e-9)
        self.assertAlmostEqual(p["lon"][0], (4 + 47 / 60 + 7.65 / 3600) * 15, places=9)
        self.assertEqual((p["frame"], p["equinox"]), ("fk5", "J2000"))
        self.assertEqual(p["report"]["placed"], 4)

    def test_decimal_hours_and_degrees_differ_by_fifteen(self):
        f = write(self.d, "dec.csv", "ra,dec\n4.5,-30\n10,20.5\n")
        base = {"lon": "ra", "lat": "dec", "frame": "icrs"}
        deg = ct.op_positions(f, {"declaration": {**base, "format": "degrees"}})
        hrs = ct.op_positions(f, {"declaration": {**base, "format": "hours_degrees"}})
        self.assertEqual(deg["lon"], [4.5, 10.0])
        self.assertEqual(hrs["lon"], [67.5, 150.0])
        self.assertEqual(hrs["lat"], deg["lat"])

    def test_angles_are_kept_in_degrees_axial_ones_in_0_180_and_missing_ones_are_null(self):
        f = write(self.d, "ang.csv", "lon,lat,pa,spa\n10,20,-59.0,2\n11,21,,3\n12,22,181.0,-1\n13,23,0.5,x\n")
        decl = {"lon": "lon", "lat": "lat", "format": "degrees", "frame": "galactic",
                "angle": {"column": "pa", "unit": "deg", "quantity": "b_field", "sigmaColumn": "spa"}}
        p = ct.op_positions(f, {"declaration": decl})
        self.assertAlmostEqual(p["angle"][0], 121.0)
        self.assertIsNone(p["angle"][1])                      # empty is missing, not 0
        self.assertAlmostEqual(p["angle"][2], 1.0)
        self.assertTrue(p["angleAxial"])
        self.assertEqual(p["sigma"][:2], [2.0, 3.0])
        self.assertIsNone(p["sigma"][2])                       # a negative uncertainty is unreadable
        self.assertIsNone(p["sigma"][3])
        self.assertEqual(p["report"]["angle"]["missing"], 1)
        rad = dict(decl, angle={"column": "pa", "unit": "rad", "quantity": "unspecified", "sigmaColumn": None})
        f2 = write(self.d, "rad.csv", "lon,lat,pa\n1,2,1.5707963267948966\n")
        r = ct.op_positions(f2, {"declaration": rad})
        self.assertAlmostEqual(r["angle"][0], 90.0)
        self.assertFalse(r["angleAxial"])

    def test_a_galactic_table_transforms_to_fk5_like_astropy(self):
        f = write(self.d, "gal.tbl", "|num |glon    |glat     |PA       |\n|real|real    |real     |real     |\n|null|null    |null     |null     |\n 1.0  277.985  -37.96    -59.0306\n 2.0  280.894  -35.5962  -78.1392\n 3.0  300.0    10.0      20.0\n")
        decl = {"lon": "glon", "lat": "glat", "format": "degrees", "frame": "galactic",
                "angle": {"column": "PA", "unit": "deg", "quantity": "starlight_polarization", "sigmaColumn": None}}
        out = ct.op_positions(f, {"declaration": decl}, to=ct.check_target({"frame": "fk5", "equinox": "J2000"}))
        g = SkyCoord(Galactic(l=[277.985, 280.894, 300] * u.deg, b=[-37.96, -35.5962, 10] * u.deg))
        want = g.transform_to(FK5(equinox="J2000"))
        np.testing.assert_allclose(out["lon"], want.ra.deg, atol=1e-9)
        np.testing.assert_allclose(out["lat"], want.dec.deg, atol=1e-9)
        # the angle: walk 1 arcmin along the Galactic position angle, move both points to FK5, measure the angle there
        pa = np.array([-59.0306, -78.1392, 20.0])
        tip = g.directional_offset_by(pa * u.deg, 1 * u.arcmin)
        expect = np.mod(want.position_angle(tip.transform_to(FK5(equinox="J2000"))).deg, 180.0)
        diff = np.abs((np.array(out["angle"]) - expect + 90) % 180 - 90)
        self.assertLess(diff.max(), 0.01)                      # degrees; the probe step differs, the curvature does not matter
        self.assertEqual(out["frame"], "fk5")
        # to the declared frame itself leaves the position unchanged
        same = ct.op_positions(f, {"declaration": decl}, to=ct.check_target({"frame": "galactic"}))
        np.testing.assert_allclose(same["lon"], [277.985, 280.894, 300], atol=1e-9)

    def test_unreadable_values_are_counted_and_reported_with_line_and_column(self):
        rows = ["name\tra\tdec"] + [f"s{i}\t{4 + i % 10} 00 00\t-30 00 00" for i in range(14)]  # lines 2-15
        rows += ["s14\t25:61:00\t-30 00 00", "s15\t4 00 00\t-30 00 00", "s16\t\t-30 00 00", "s17\t30 00 00\t95 00 00", "s18\tfoo\t-30 00 00"]  # lines 16-20
        f = write(self.d, "bad.tsv", "\n".join(rows) + "\n")
        decl = {"lon": "ra", "lat": "dec", "format": "sexagesimal_hours_degrees", "frame": "icrs"}
        rep = ct.op_report(f, {"declaration": decl})
        self.assertEqual(rep["rows"], 19)
        self.assertEqual(rep["placed"], 15)                    # 14 + the valid row at line 17
        self.assertEqual(rep["unreadable"], 3)                 # 25:61:00, 30 hours, foo, and the latitude 95: counted by row
        self.assertEqual(rep["missing"], 1)
        by_line = {s["line"]: s for s in rep["samples"]}
        self.assertEqual(set(by_line), {16, 19, 20})
        self.assertEqual((by_line[16]["column"], by_line[16]["value"]), ("ra", "25:61:00"))
        self.assertIn("not a valid angle", by_line[16]["reason"])
        self.assertIn("outside the range", by_line[19]["reason"])
        # unreadable rows have no position: null, never 0
        p = ct.op_positions(f, {"declaration": decl})
        self.assertIsNone(p["lon"][14])
        self.assertIsNone(p["lat"][14])
        self.assertEqual(p["row"][14], 15)

    def test_the_declaration_is_validated_and_nothing_is_assumed(self):
        L = ct.load(self.star)
        bad = [
            ({**SEXA_DECL, "frame": None}, "frame"),
            ({**SEXA_DECL, "equinox": None}, "equinox"),
            ({**SEXA_DECL, "frame": "icrs"}, "no equinox"),
            ({**SEXA_DECL, "lon": "nope"}, "no column"),
            ({**SEXA_DECL, "lat": "RA(J2000)"}, "different columns"),
            ({**SEXA_DECL, "frame": "galactic", "equinox": None}, "not written in hours"),
            ({**SEXA_DECL, "format": "furlongs"}, "format"),
            ({**SEXA_DECL, "angle": {**SEXA_DECL["angle"], "frame": "pixel"}}, "no pixel grid"),
            ({**SEXA_DECL, "angle": {**SEXA_DECL["angle"], "unit": None}}, "unit"),
            ({**SEXA_DECL, "angle": {**SEXA_DECL["angle"], "quantity": "vibes"}}, "quantity"),
            ({**SEXA_DECL, "values": ["P(%)"] * 2}, "twice"),
        ]
        for decl, word in bad:
            with self.subTest(word):
                with self.assertRaises(ct.Bad) as cm:
                    ct.validate_declaration(decl, L)
                self.assertIn(word, str(cm.exception))
        ok = ct.validate_declaration(SEXA_DECL, L)
        self.assertEqual(ok["angle"]["frame"], "sky")

    def test_proposals_come_from_names_and_say_so(self):
        doc = ct.op_describe(self.star, {})
        pr = doc["proposal"]
        self.assertEqual((pr["lon"], pr["lat"], pr["frame"], pr["equinox"]), ("RA(J2000)", "DEC(J2000)", "fk5", "J2000"))
        self.assertEqual(pr["basis"], "proposed from the column names")
        self.assertIn("J2000", pr["frame_basis"])
        self.assertEqual((pr["angle"]["column"], pr["angle"]["unit"], pr["angle"]["quantity"], pr["sigma"]), ("PA(deg)", "deg", None, "sPA(deg)"))
        plain = ct.op_describe(write(self.d, "plain.csv", "lon,lat\n10,20\n11,21\n"), {})["proposal"]
        self.assertEqual((plain["lon"], plain["frame"]), ("lon", None))     # lon/lat do not name a frame
        self.assertTrue(plain["notes"])
        ra = ct.op_describe(write(self.d, "ra.csv", "RA,DEC\n10.5,20\n11.5,21\n"), {})["proposal"]
        self.assertIsNone(ra["frame"])
        self.assertTrue(any("every longitude is 24 or less" in n for n in ra["notes"]))


class Caps(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.d = Path(self.tmp.name)

    def tearDown(self):
        self.tmp.cleanup()

    def test_an_oversize_file_is_refused_before_it_is_read(self):
        p = write(self.d, "big.csv", "a,b\n" + "1,2\n" * 100)
        old = ct.MAX_FILE_BYTES
        ct.MAX_FILE_BYTES = 100
        try:
            with self.assertRaises(ct.Bad) as cm:
                ct.load(p)
            self.assertIn("not read", str(cm.exception))
        finally:
            ct.MAX_FILE_BYTES = old

    def test_too_many_rows_and_columns_are_refused(self):
        old = (ct.MAX_ROWS, ct.MAX_COLUMNS)
        try:
            ct.MAX_ROWS = 10
            with self.assertRaises(ct.Bad) as cm:
                ct.parse(("a,b\n" + "1,2\n" * 50).encode(), "r.csv")
            self.assertIn("rows", str(cm.exception))
            ct.MAX_ROWS = old[0]
            ct.MAX_COLUMNS = 3
            with self.assertRaises(ct.Bad) as cm:
                ct.parse(b"a,b,c,d\n1,2,3,4\n", "c.csv")
            self.assertIn("columns", str(cm.exception))
        finally:
            ct.MAX_ROWS, ct.MAX_COLUMNS = old

    def test_a_votable_over_the_row_cap_is_refused_from_its_own_claim(self):
        with self.assertRaises(ct.Bad):
            ct.parse(votable(extra_attr='nrows="2000000000"').encode(), "n.vot")

    def test_long_names_and_cells_are_cut(self):
        L = ct.parse(("n" * 500 + ",v\n" + "x" * 5000 + ",1\n").encode(), "long.csv")
        self.assertEqual(len(L.table.colnames[0]), ct.MAX_NAME)
        self.assertEqual(len(ct.rows_slice(L, 0, 1)["rows"][0][0]), ct.MAX_CELL)

    def test_compressed_and_binary_files_are_refused(self):
        with self.assertRaises(ct.Bad):
            ct.parse(gzip.compress(b"a,b\n1,2\n"), "x.csv")
        with self.assertRaises(ct.Bad):
            ct.parse(b"a,b\n1,\x00\x01\x02\n", "x.csv")

    def test_a_pages_and_positions_request_is_bounded(self):
        p = write(self.d, "p.csv", "a,b\n1,2\n")
        with self.assertRaises(ct.Bad):
            ct.op_rows(p, {"limit": ct.PAGE_ROWS + 1})
        with self.assertRaises(ct.Bad):
            ct.op_rows(p, {"offset": -1})


class HostileVotable(unittest.TestCase):
    def refused(self, text, word):
        with self.assertRaises(ct.Bad) as cm:
            ct.parse(text.encode(), "h.vot")
        self.assertIn(word, str(cm.exception))

    def test_entity_expansion_is_refused_before_the_parser_sees_it(self):
        lol = '<!DOCTYPE lolz [<!ENTITY a "aaaaaaaaaa"><!ENTITY b "&a;&a;&a;&a;&a;&a;&a;&a;&a;&a;"><!ENTITY c "&b;&b;&b;&b;&b;&b;&b;&b;&b;&b;">]>\n'
        self.refused(votable(rows="<TR><TD>1</TD><TD>&c;</TD></TR>", head=lol), "entity")

    def test_an_external_entity_is_refused(self):
        self.refused(votable(head='<!DOCTYPE x [<!ENTITY e SYSTEM "file:///etc/passwd">]>\n'), "entity")

    def test_huge_declared_arrays_are_refused(self):
        self.refused(votable(field_extra='arraysize="999999999"'), "array")

    def test_a_stream_fetched_from_a_url_is_refused(self):
        t = """<?xml version="1.0"?><VOTABLE version="1.4"><RESOURCE><TABLE><FIELD name="a" datatype="double"/>
<DATA><BINARY><STREAM href="http://127.0.0.1:9/x.bin"/></BINARY></DATA></TABLE></RESOURCE></VOTABLE>"""
        self.refused(t, "URL")

    def test_a_gzip_stream_is_refused(self):
        t = """<?xml version="1.0"?><VOTABLE version="1.4"><RESOURCE><TABLE><FIELD name="a" datatype="double"/>
<DATA><BINARY><STREAM encoding="gzip">H4sIAAAA</STREAM></BINARY></DATA></TABLE></RESOURCE></VOTABLE>"""
        self.refused(t, "compressed")

    def test_a_malformed_votable_is_a_controlled_refusal(self):
        self.refused("<?xml version='1.0'?><VOTABLE><RESOURCE><TABLE><FIELD name='a' datatype='double'/><DATA><TABLEDATA><TR><TD>x", "VOTable")


class Protocol(unittest.TestCase):
    """Through the line protocol, as the API uses it: the head is small and the document is the payload."""

    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        cls.d = Path(cls.tmp.name)
        cls.star = write(cls.d, "sl.dat", STARLIGHT)
        cls.c = Client(roots=[cls.d])

    @classmethod
    def tearDownClass(cls):
        cls.c.close()
        cls.tmp.cleanup()

    def test_describe_rows_report_positions_and_transform(self):
        head, payload = self.c.ask(op="catalog_describe", path=self.star)
        self.assertTrue(head["ok"])
        self.assertEqual(head["nbytes"], len(payload))
        self.assertEqual(json.loads(payload)["rows"], 4)
        head, payload = self.c.ask(op="catalog_rows", path=self.star, offset=1, limit=2)
        doc = json.loads(payload)
        self.assertEqual((doc["offset"], len(doc["rows"]), doc["lines"]), (1, 2, [3, 4]))
        head, payload = self.c.ask(op="catalog_report", path=self.star, declaration=SEXA_DECL)
        self.assertEqual(json.loads(payload)["placed"], 4)
        head, payload = self.c.ask(op="catalog_positions", path=self.star, declaration=SEXA_DECL, limit=2)
        self.assertEqual(len(json.loads(payload)["lon"]), 2)
        head, payload = self.c.ask(op="catalog_transform", path=self.star, declaration=SEXA_DECL, to={"frame": "galactic"}, limit=2)
        self.assertEqual(json.loads(payload)["frame"], "galactic")

    def test_a_refusal_is_a_short_message_and_the_worker_keeps_serving(self):
        bad = write(self.d, "x.csv", b"a,b\n1,\x00\x01\n")
        head, _ = self.c.ask(op="catalog_describe", path=bad)
        self.assertFalse(head["ok"])
        self.assertIn("binary", head["error"])
        head, _ = self.c.ask(op="catalog_positions", path=self.star, declaration={**SEXA_DECL, "frame": None})
        self.assertFalse(head["ok"])
        self.assertIn("frame", head["error"])
        head, _ = self.c.ask(op="catalog_describe", path=self.star)
        self.assertTrue(head["ok"])

    def test_a_file_outside_the_roots_is_refused(self):
        with tempfile.TemporaryDirectory() as other:
            p = write(Path(other), "o.csv", "a,b\n1,2\n")
            head, _ = self.c.ask(op="catalog_describe", path=p)
            self.assertFalse(head["ok"])
            self.assertEqual(head["code"], "forbidden")

    def test_a_changed_file_is_read_again(self):
        p = write(self.d, "chg.csv", "a,b\n1,2\n")
        self.assertEqual(json.loads(self.c.ask(op="catalog_describe", path=p)[1])["rows"], 1)
        write(self.d, "chg.csv", "a,b\n1,2\n3,4\n")
        self.assertEqual(json.loads(self.c.ask(op="catalog_describe", path=p)[1])["rows"], 2)


if __name__ == "__main__":
    unittest.main()
