package notionimport

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/color"
	"image/png"
	"io/fs"
	"strings"
	"testing"
)

type ze struct {
	name string
	data []byte
	mode fs.FileMode
}

func s(name, data string) ze { return ze{name: name, data: []byte(data)} }

func buildZip(t *testing.T, entries ...ze) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.mode != 0 {
			h.SetMode(e.mode)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(e.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func run(t *testing.T, z []byte, opt Options) (Result, error) {
	t.Helper()
	return Import(bytes.NewReader(z), int64(len(z)), opt)
}

func mustRun(t *testing.T, z []byte, opt Options) Result {
	t.Helper()
	r, err := run(t, z, opt)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func pngBytes(t *testing.T, w, h int, shade uint8) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		img.SetGray(x, 0, color.Gray{Y: shade})
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func id(c byte) string { return strings.Repeat(string(c), 31) + "1" }

func page(res Result, title string) *Page {
	for i := range res.Pages {
		if res.Pages[i].Title == title {
			return &res.Pages[i]
		}
	}
	return nil
}

func hasWarning(res Result, sub string) bool {
	for _, w := range res.Warnings {
		if strings.Contains(w, sub) {
			return true
		}
	}
	return false
}

func realisticExport(t *testing.T) []byte {
	img := pngBytes(t, 4, 4, 200)
	home := "Home " + id('a')
	weekly := "Weekly notes " + id('b')
	sub := "Sub page " + id('c')
	db := "Experiments " + id('d')
	row := "Run one " + id('e')
	return buildZip(t,
		s(home+".md", "# Home\n\nSee [Weekly notes]("+strings.ReplaceAll(weekly, " ", "%20")+".md) and [the sub page]("+
			strings.ReplaceAll(home, " ", "%20")+"/"+strings.ReplaceAll(sub, " ", "%20")+".md).\n\n"+
			"<aside>\nRemember **this** <b>now</b>\n</aside>\n\n"+
			"![plot]("+strings.ReplaceAll(home, " ", "%20")+"/plot.png)\n\n"+
			"![again]("+strings.ReplaceAll(home, " ", "%20")+"/plot.png)\n\n"+
			"[Gone](Nope%20"+id('f')+".md) and [site](https://example.org/x)\n\n```\n<aside>[x](y.md)</aside>\n```\n"),
		s(weekly+".md", "# Weekly notes\n\nBack to [Home]("+strings.ReplaceAll(home, " ", "%20")+".md)\n"),
		s(home+"/"+sub+".md", "# Sub page\n\nbody\n"),
		ze{name: home + "/plot.png", data: img},
		s(db+".csv", "Name,Owner,Note\nRun one,Ann,a|b\nOther,\"Run one ("+strings.ReplaceAll(db, " ", "%20")+"/"+strings.ReplaceAll(row, " ", "%20")+".md)\",x\n"),
		s(db+"/"+row+".md", "# Run one\n\nrow body\n"),
		s(".DS_Store", "junk"),
	)
}

func TestRealisticExport(t *testing.T) {
	res := mustRun(t, realisticExport(t), Options{})
	if len(res.Pages) != 5 {
		t.Fatalf("pages = %d: %+v", len(res.Pages), res.Pages)
	}
	home := page(res, "Home")
	if home == nil {
		t.Fatal("no Home page")
	}
	if home.Parent != "" {
		t.Errorf("home parent %q", home.Parent)
	}
	for _, want := range []string{
		"[[Weekly notes]]",
		"[[Sub page]] (the sub page)",
		"> Remember **this** now",
		"[site](https://example.org/x)",
		"Gone",
		"```\n<aside>[x](y.md)</aside>\n```",
	} {
		if !strings.Contains(home.Markdown, want) {
			t.Errorf("home missing %q in:\n%s", want, home.Markdown)
		}
	}
	if strings.Contains(home.Markdown, "<b>") || strings.Contains(home.Markdown, "](Nope") || strings.HasPrefix(home.Markdown, "# ") {
		t.Errorf("home not converted:\n%s", home.Markdown)
	}
	sum := sha256.Sum256(pngBytes(t, 4, 4, 200))
	sha := hex.EncodeToString(sum[:])
	if strings.Count(home.Markdown, "![plot](attachment:"+sha+")") != 1 || !strings.Contains(home.Markdown, "![again](attachment:"+sha+")") {
		t.Errorf("image refs wrong:\n%s", home.Markdown)
	}
	if len(res.Images) != 1 || res.Images[0].SHA256 != sha || res.Images[0].Mime != "image/png" || res.Images[0].Name != "plot.png" {
		t.Errorf("images = %+v", res.Images)
	}
	if !hasWarning(res, "Nope") {
		t.Errorf("no warning for missing page: %v", res.Warnings)
	}
	if w := page(res, "Weekly notes"); w == nil || !strings.Contains(w.Markdown, "[[Home]]") {
		t.Errorf("weekly: %+v", w)
	}
	sub := page(res, "Sub page")
	if sub == nil || sub.Parent != home.Source {
		t.Errorf("sub parent = %+v", sub)
	}
	dbp := page(res, "Experiments")
	if dbp == nil {
		t.Fatal("no database page")
	}
	for _, want := range []string{"| Name | Owner | Note |", "| --- | --- | --- |", "| [[Run one]] | Ann | a\\|b |", "[[Run one]] |"} {
		if !strings.Contains(dbp.Markdown, want) {
			t.Errorf("table missing %q:\n%s", want, dbp.Markdown)
		}
	}
	row := page(res, "Run one")
	if row == nil || row.Parent != dbp.Source {
		t.Errorf("row = %+v", row)
	}
}

func TestNestedPartZip(t *testing.T) {
	inner := buildZip(t, s("A "+id('a')+".md", "# A\n\nto [B](B%20"+id('b')+".md)\n"), s("B "+id('b')+".md", "# B\n"))
	outer := buildZip(t, ze{name: "Export-1234-Part-1.zip", data: inner})
	res := mustRun(t, outer, Options{})
	if len(res.Pages) != 2 || page(res, "A") == nil || !strings.Contains(page(res, "A").Markdown, "[[B]]") {
		t.Fatalf("%+v", res)
	}
}

func TestNestedTwoDeepIgnored(t *testing.T) {
	deep := buildZip(t, s("Z.md", "# Z\n"))
	inner := buildZip(t, ze{name: "deep.zip", data: deep}, s("A.md", "# A\n"))
	outer := buildZip(t, ze{name: "Part-1.zip", data: inner})
	res := mustRun(t, outer, Options{})
	if len(res.Pages) != 1 || !hasWarning(res, "nested archive") {
		t.Fatalf("%+v", res)
	}
}

func TestUnsafeNames(t *testing.T) {
	for _, name := range []string{"../x.md", "/etc/x.md", "a/../../x.md", "C:/x.md", `a\b.md`, "a\x00b.md"} {
		_, err := run(t, buildZip(t, s(name, "# x")), Options{})
		if err == nil {
			t.Errorf("%q accepted", name)
		}
	}
}

func TestSymlinkIgnored(t *testing.T) {
	z := buildZip(t, s("Page.md", "# Page\n"), ze{name: "link.md", data: []byte("/etc/passwd"), mode: fs.ModeSymlink | 0o777})
	res := mustRun(t, z, Options{})
	if len(res.Pages) != 1 || !hasWarning(res, "symbolic link") {
		t.Fatalf("%+v", res)
	}
}

func TestTooManyEntries(t *testing.T) {
	var es []ze
	for i := 0; i < 5; i++ {
		es = append(es, s("p"+string(rune('a'+i))+".md", "# p"))
	}
	_, err := run(t, buildZip(t, es...), Options{MaxEntries: 4})
	if err == nil || !strings.Contains(err.Error(), "more than 4 entries") {
		t.Fatalf("err = %v", err)
	}
}

func TestDecompressionBomb(t *testing.T) {
	zeros := make([]byte, 8<<20)
	for _, name := range []string{"bomb.md", "bomb.png"} {
		_, err := run(t, buildZip(t, ze{name: name, data: zeros}), Options{MaxTotalBytes: 1 << 20, MaxFileBytes: 32 << 20, MaxImageBytes: 32 << 20})
		if err == nil || !strings.Contains(err.Error(), "uncompressed") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	// A file that is never imported (a video, a PDF) is not decompressed, so it cannot abort the import.
	if _, err := run(t, buildZip(t, ze{name: "talk.mp4", data: zeros}, s("a.md", "# A")), Options{MaxTotalBytes: 1 << 20, MaxFileBytes: 1 << 20}); err != nil {
		t.Errorf("unimported large file: %v", err)
	}
	_, err := run(t, buildZip(t, ze{name: "big.md", data: zeros}), Options{MaxFileBytes: 1 << 20})
	if err == nil || !strings.Contains(err.Error(), "MaxFileBytes") {
		t.Errorf("file limit: %v", err)
	}
}

func TestOversizedImageSkipped(t *testing.T) {
	big := pngBytes(t, 4, 4, 9)
	z := buildZip(t, s("P.md", "# P\n\n![big](big.png)\n"), ze{name: "big.png", data: big})
	res := mustRun(t, z, Options{MaxImageBytes: int64(len(big)) - 1})
	if len(res.Images) != 0 || !hasWarning(res, "larger than") || !strings.Contains(res.Pages[0].Markdown, "(image omitted: big)") {
		t.Fatalf("%+v", res)
	}
	if strings.Contains(res.Pages[0].Markdown, "attachment:") {
		t.Error("attachment emitted for a skipped image")
	}
}

func TestNonImageAsImage(t *testing.T) {
	z := buildZip(t, s("P.md", "# P\n\n![a](fake.png)\n![b](doc.pdf)\n![c](gone.png)\n![d](https://x.example/i.png)\n"),
		s("fake.png", "this is not a png"), s("doc.pdf", "%PDF"))
	res := mustRun(t, z, Options{})
	md := res.Pages[0].Markdown
	if len(res.Images) != 0 || strings.Contains(md, "attachment:") || strings.Contains(md, "![") {
		t.Fatalf("md:\n%s\n%+v", md, res)
	}
	for _, w := range []string{"fake.png", "doc.pdf", "gone.png", "external image"} {
		if !hasWarning(res, w) {
			t.Errorf("no warning for %s: %v", w, res.Warnings)
		}
	}
}

func TestOversizedDimensionsSkipped(t *testing.T) {
	z := buildZip(t, s("P.md", "# P\n\n![w](wide.png)\n"), ze{name: "wide.png", data: pngBytes(t, 5000, 2, 1)})
	res := mustRun(t, z, Options{})
	if len(res.Images) != 0 || !hasWarning(res, "limit") {
		t.Fatalf("%+v", res)
	}
}

func TestDuplicateTitles(t *testing.T) {
	z := buildZip(t,
		s("a/Notes "+id('a')+".md", "# Notes\n\nfirst\n"),
		s("b/Notes "+id('b')+".md", "# Notes\n\nsecond, see [first](../a/Notes%20"+id('a')+".md)\n"),
		s("c/Notes "+id('c')+".md", "# Notes\n\nthird, see [second](../b/Notes%20"+id('b')+".md)\n"),
	)
	res := mustRun(t, z, Options{})
	got := map[string]string{}
	for _, p := range res.Pages {
		got[p.Source] = p.Title
	}
	if got["a/Notes "+id('a')+".md"] != "Notes" || got["b/Notes "+id('b')+".md"] != "Notes (2)" || got["c/Notes "+id('c')+".md"] != "Notes (3)" {
		t.Fatalf("titles = %v", got)
	}
	if !strings.Contains(res.Pages[1].Markdown, "[[Notes]] (first)") || !strings.Contains(res.Pages[2].Markdown, "[[Notes (2)]] (second)") {
		t.Errorf("links wrong: %q / %q", res.Pages[1].Markdown, res.Pages[2].Markdown)
	}
}

func TestTitleRules(t *testing.T) {
	long := strings.Repeat("é", 300)
	z := buildZip(t,
		s("Plain "+id('a')+".md", "no heading, just text\n"),
		s("Blank "+id('b')+".md", "#  \n\nbody\n"),
		s("Long "+id('c')+".md", "# "+long+"\n"),
		s("Br "+id('d')+".md", "# A [b] | c\n"),
	)
	res := mustRun(t, z, Options{})
	if p := page(res, "Plain"); p == nil || p.Markdown != "no heading, just text" {
		t.Errorf("plain: %+v", p)
	}
	if page(res, "Untitled") == nil {
		t.Errorf("empty heading not Untitled: %+v", res.Pages)
	}
	if page(res, "A (b) - c") == nil {
		t.Errorf("title not sanitised: %+v", res.Pages)
	}
	for _, p := range res.Pages {
		if n := len([]rune(p.Title)); n > 200 {
			t.Errorf("title %d runes", n)
		}
	}
}

func TestHTMLNeverKept(t *testing.T) {
	z := buildZip(t, s("P.md", "# P\n\n<details><summary>More</summary>\nhidden <i>text</i>\n</details>\n\n<script>alert(1)</script>after\n\n<!-- c -->x <a href=\"https://e.org\">e</a> <img src=\"https://e.org/i.png\" alt=\"i\">\n<aside>\n\none\n\n</aside>\n"))
	md := mustRun(t, z, Options{}).Pages[0].Markdown
	for _, bad := range []string{"<details", "<summary", "<script", "alert", "<i>", "<!--", "<aside"} {
		if strings.Contains(md, bad) {
			t.Errorf("%q kept:\n%s", bad, md)
		}
	}
	for _, want := range []string{"More", "hidden text", "after", "[e](https://e.org)", "> one"} {
		if !strings.Contains(md, want) {
			t.Errorf("missing %q:\n%s", want, md)
		}
	}
}

func TestCSVCapsAndAllVariant(t *testing.T) {
	var b strings.Builder
	b.WriteString("Name\n")
	for i := 0; i < 2005; i++ {
		b.WriteString("r\n")
	}
	z := buildZip(t, s("Db "+id('a')+".csv", "Name\nfiltered\n"), s("Db "+id('a')+"_all.csv", b.String()))
	res := mustRun(t, z, Options{})
	if len(res.Pages) != 1 || !hasWarning(res, "first 2000") {
		t.Fatalf("%+v", res.Warnings)
	}
	if rows := strings.Count(res.Pages[0].Markdown, "\n| r |"); rows != 2000 {
		t.Errorf("rows = %d", rows)
	}
}

func TestNotAZip(t *testing.T) {
	if _, err := run(t, []byte("hello"), Options{}); err == nil {
		t.Fatal("accepted")
	}
}

func TestPagePropertiesBecomeAList(t *testing.T) {
	got := propertiesAsList(strings.Split("Date: November 10, 2026\nPresenter: Denis\nStatus: Draft\n\nBody text: not a property.", "\n"))
	want := "- **Date:** November 10, 2026\n- **Presenter:** Denis\n- **Status:** Draft\n\nBody text: not a property."
	if got != want {
		t.Fatalf("got %q", got)
	}
	if got := propertiesAsList([]string{"Note: a sentence that happens to start like a property", "and continues."}); !strings.HasPrefix(got, "Note:") {
		t.Fatalf("a single line followed by text is prose, not a property: %q", got)
	}
}
