package notionimport

import (
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/gif"  // register GIF for image.DecodeConfig
	_ "image/jpeg" // register JPEG
	_ "image/png"  // register PNG
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type pageInfo struct {
	source   string
	stem     string // source without extension (and without "_all" for CSV)
	csv      bool
	id       string
	rawTitle string
	title    string // final, unique
	body     string // raw Markdown without the title heading
	parent   string
	out      string
	rows     [][]string
	props    [][2]string // page properties (name, value) for the front matter
	viewCSV  string      // Source of the database table this page is only a view of
}

type imgRes struct {
	sha string
	ok  bool
}

func truncRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}

func cleanTitle(s string) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.Map(func(r rune) rune {
		switch r {
		case '[':
			return '('
		case ']':
			return ')'
		case '|':
			return '-'
		case '\n', '\r', '\t':
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(truncRunes(strings.TrimSpace(s), maxTitleRunes))
	if s == "" {
		return "Untitled"
	}
	return s
}

func normalizeText(b []byte) string {
	s := strings.ToValidUTF8(string(b), "")
	s = strings.TrimPrefix(s, "\ufeff")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// build indexes the files, assigns titles and parents, and converts every page.
func (im *importer) build() error {
	for _, name := range im.order {
		f := im.files[name]
		if f.kind != kindPage && f.kind != kindCSV {
			continue
		}
		p := &pageInfo{source: name, csv: f.kind == kindCSV}
		stem := strings.TrimSuffix(name, path.Ext(name))
		if p.csv {
			stem = strings.TrimSuffix(stem, "_all")
		}
		p.stem = stem
		base := path.Base(stem)
		if m := idRe.FindStringSubmatch(base); m != nil {
			p.id = m[1]
			base = strings.TrimSpace(strings.TrimSuffix(base, m[0]))
		}
		p.rawTitle = cleanTitle(base)
		if !p.csv {
			text := normalizeText(f.data)
			lines := strings.Split(text, "\n")
			i := 0
			for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
				i++
			}
			if i < len(lines) && strings.HasPrefix(lines[i], "# ") {
				p.rawTitle = cleanTitle(strings.TrimPrefix(lines[i], "# "))
				i++
				if i < len(lines) && strings.TrimSpace(lines[i]) == "" {
					i++
				}
			} else {
				i = 0
			}
			p.props, p.body = splitProperties(lines[i:])
		}
		im.pages[name] = p
	}

	// A database exported both as "X.csv" and "X_all.csv": keep the complete one.
	for src, p := range im.pages {
		if !p.csv || strings.HasSuffix(strings.TrimSuffix(src, ".csv"), "_all") {
			continue
		}
		all := p.stem + "_all.csv"
		if _, ok := im.pages[all]; ok {
			im.warn("%s: the unfiltered export %s is used instead", src, all)
			im.alias[src] = all
			delete(im.pages, src)
		}
	}

	// A page that is only a link to a CSV of its own database (a database view) and that CSV are one page.
	for _, s := range im.sortedSources() {
		p := im.pages[s]
		if p == nil || p.csv {
			continue
		}
		m := viewLinkRe.FindStringSubmatch(strings.TrimSpace(removeViewSettings(p.body)))
		if m == nil {
			continue
		}
		k, ok := im.lookup(path.Dir(p.source), m[1])
		if q, isPage := im.pages[k]; ok && isPage && q.csv {
			p.viewCSV = k
			im.alias[k] = p.source
			delete(im.pages, k)
		}
	}

	srcs := im.sortedSources()
	byStem := map[string]*pageInfo{}
	// Notion also names a page's folder without the id suffix; byLoose finds the page for such a folder.
	byLoose := map[string]*pageInfo{}
	looseDup := map[string]bool{}
	for _, s := range srcs { // markdown pages win over a CSV with the same stem
		p := im.pages[s]
		if cur, ok := byStem[p.stem]; !ok || (cur.csv && !p.csv) {
			byStem[p.stem] = p
		}
		if p.id != "" {
			lk := path.Join(path.Dir(p.stem), p.rawTitle)
			if cur, ok := byLoose[lk]; ok && cur.csv == p.csv {
				looseDup[lk] = true
			} else if !ok || (cur.csv && !p.csv) {
				byLoose[lk] = p
			}
			if _, dup := im.byID[p.id]; !dup {
				im.byID[p.id] = p
			}
		}
		b := path.Base(s)
		im.byBase[b] = append(im.byBase[b], p)
	}
	im.childTitles = map[string][]*pageInfo{}
	for _, s := range srcs {
		p := im.pages[s]
		dir := path.Dir(p.source)
		for dir != "." && dir != "/" {
			par, ok := byStem[dir]
			if !ok && !looseDup[dir] {
				par, ok = byLoose[dir]
			}
			if ok && par != p {
				p.parent = par.source
				break
			}
			dir = path.Dir(dir)
		}
		if p.parent != "" {
			im.childTitles[p.parent] = append(im.childTitles[p.parent], p)
		}
	}

	// Unique titles in Source order.
	seen := map[string]bool{}
	for _, s := range srcs {
		p := im.pages[s]
		cand := p.rawTitle
		for n := 2; seen[strings.ToLower(cand)]; n++ {
			suffix := fmt.Sprintf(" (%d)", n)
			cand = truncRunes(p.rawTitle, maxTitleRunes-len(suffix)) + suffix
		}
		seen[strings.ToLower(cand)] = true
		p.title = cand
	}

	for _, s := range srcs {
		p := im.pages[s]
		if p.csv {
			im.convertCSV(p)
		} else if p.viewCSV != "" {
			p.out = im.csvTable(p, im.files[p.viewCSV])
		} else {
			im.convertPage(p)
		}
	}
	return nil
}

func (im *importer) sortedSources() []string {
	srcs := make([]string, 0, len(im.pages))
	for s := range im.pages {
		srcs = append(srcs, s)
	}
	sort.Strings(srcs)
	return srcs
}

// ---- link resolution -------------------------------------------------------

var (
	schemeRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*:`)
	hexIDRe  = regexp.MustCompile(`([0-9a-f]{32})(?:[^0-9a-f]|$)`)
)

// lookup finds the export entry a relative link points at.
func (im *importer) lookup(fromDir, dest string) (string, bool) {
	p := dest
	if i := strings.IndexAny(p, "#?"); i >= 0 {
		p = p[:i]
	}
	if p == "" {
		return "", false
	}
	if u, err := url.PathUnescape(p); err == nil {
		p = u
	}
	p = strings.ReplaceAll(p, `\`, "/")
	var cands []string
	if strings.HasPrefix(p, "/") {
		cands = append(cands, path.Clean(strings.TrimLeft(p, "/")))
	} else {
		cands = append(cands, path.Join(fromDir, p), path.Clean(p))
	}
	for _, c := range cands {
		if c == "." || c == ".." || strings.HasPrefix(c, "../") {
			continue
		}
		for _, suf := range []string{"", ".md", ".csv", "_all.csv"} {
			k := c + suf
			if a, ok := im.alias[k]; ok {
				k = a
			}
			if _, ok := im.files[k]; ok {
				return k, true
			}
		}
	}
	base := path.Base(path.Clean(p))
	if m := hexIDRe.FindStringSubmatch(base); m != nil {
		if pg, ok := im.byID[m[1]]; ok {
			return pg.source, true
		}
	}
	if list := im.byBase[base]; len(list) == 1 {
		return list[0].source, true
	}
	return "", false
}

func sameText(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

func (im *importer) wikilink(target *pageInfo, text string) string {
	if strings.TrimSpace(text) == "" || sameText(text, target.rawTitle) || sameText(text, target.title) {
		return "[[" + target.title + "]]"
	}
	return "[[" + target.title + "]] (" + strings.TrimSpace(text) + ")"
}

// linkText drops Notion's placeholder label for untitled inline databases so the link shows the page title.
func linkText(text string) string {
	if sameText(text, "Untitled") {
		return ""
	}
	return text
}

func cleanAlt(s string) string {
	s = strings.NewReplacer("[", "", "]", "", "\n", " ").Replace(s)
	return strings.TrimSpace(s)
}

func linkDest(d string) string {
	if strings.ContainsAny(d, " ()<>") {
		return "<" + strings.NewReplacer("<", "%3C", ">", "%3E").Replace(d) + ">"
	}
	return d
}

func (im *importer) linkOut(from *pageInfo, text, dest string) string {
	dir := path.Dir(from.source)
	if schemeRe.MatchString(dest) {
		lower := strings.ToLower(dest)
		switch {
		case strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://"):
			if u, err := url.Parse(dest); err == nil && (u.Host == "notion.so" || strings.HasSuffix(u.Host, ".notion.so")) {
				if m := hexIDRe.FindStringSubmatch(path.Base(u.Path)); m != nil {
					if pg, ok := im.byID[m[1]]; ok {
						return im.wikilink(pg, linkText(text))
					}
				}
			}
			return "[" + text + "](" + linkDest(dest) + ")"
		case strings.HasPrefix(lower, "mailto:"):
			return "[" + text + "](" + linkDest(dest) + ")"
		}
		im.warn("%s: link with unsupported address kept as text (%s)", from.source, truncRunes(text, 60))
		return text
	}
	if k, ok := im.lookup(dir, dest); ok {
		if pg, isPage := im.pages[k]; isPage {
			return im.wikilink(pg, linkText(text))
		}
	}
	if strings.TrimSpace(text) == "" {
		text = path.Base(dest)
	}
	im.warn("%s: link to %q is not an exported page; kept as text", from.source, truncRunes(dest, 120))
	return text
}

func (im *importer) imageOut(from *pageInfo, alt, dest string) string {
	alt = cleanAlt(alt)
	if schemeRe.MatchString(dest) {
		label := alt
		if label == "" {
			label = "image"
		}
		im.warn("%s: external image not imported, kept as a link (%s)", from.source, truncRunes(dest, 120))
		lower := strings.ToLower(dest)
		if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
			return "[" + label + "](" + linkDest(dest) + ")"
		}
		return label
	}
	omitted := func() string {
		l := alt
		if l == "" {
			l = path.Base(dest)
		}
		return "(image omitted: " + l + ")"
	}
	k, ok := im.lookup(path.Dir(from.source), dest)
	if !ok {
		im.warn("%s: image %q is not in the export; omitted", from.source, truncRunes(dest, 120))
		return omitted()
	}
	res, seen := im.images[k]
	if !seen {
		res = im.processImage(from, im.files[k])
		im.images[k] = res
	}
	if !res.ok {
		return omitted()
	}
	return "![" + alt + "](attachment:" + res.sha + ")"
}

func (im *importer) processImage(from *pageInfo, f *file) *imgRes {
	bad := &imgRes{}
	if f.kind != kindImage {
		im.warn("%s: %s is not a PNG, JPEG or GIF image; omitted", from.source, f.name)
		return bad
	}
	if f.tooLarge {
		im.warn("%s: image %s is larger than %d bytes; omitted", from.source, f.name, im.opt.MaxImageBytes)
		return bad
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(f.data))
	if err != nil {
		im.warn("%s: %s is not a valid image (%v); omitted", from.source, f.name, err)
		return bad
	}
	if cfg.Width > maxImageSide || cfg.Height > maxImageSide || cfg.Width <= 0 || cfg.Height <= 0 {
		im.warn("%s: image %s is %d x %d, over the %d x %d limit; omitted", from.source, f.name, cfg.Width, cfg.Height, maxImageSide, maxImageSide)
		return bad
	}
	if _, _, err := image.Decode(bytes.NewReader(f.data)); err != nil {
		im.warn("%s: %s does not decode (%v); omitted", from.source, f.name, err)
		return bad
	}
	var mime string
	switch format {
	case "png":
		mime = "image/png"
	case "jpeg":
		mime = "image/jpeg"
	case "gif":
		mime = "image/gif"
	default:
		im.warn("%s: %s is a %s image, not PNG, JPEG or GIF; omitted", from.source, f.name, format)
		return bad
	}
	sum := sha256.Sum256(f.data)
	sha := hex.EncodeToString(sum[:])
	if !im.imageBySHA[sha] {
		im.imageBySHA[sha] = true
		im.imageList = append(im.imageList, Image{SHA256: sha, Mime: mime, Data: f.data, Name: path.Base(f.name)})
	}
	return &imgRes{sha: sha, ok: true}
}

// ---- CSV -------------------------------------------------------------------

var (
	cellLinkRe = regexp.MustCompile(`([^,]*?)\s*\(([^()]*?\.md)\)`)
	tagStripRe = regexp.MustCompile(`</?[a-zA-Z][a-zA-Z0-9-]*(?:\s[^<>]*)?/?>`)
)

func (im *importer) convertCSV(p *pageInfo) { p.out = im.csvTable(p, im.files[p.source]) }

// csvTable renders the CSV file f as a Markdown table for page p; rows that have their own page become [[links]].
func (im *importer) csvTable(p *pageInfo, f *file) string {
	r := csv.NewReader(strings.NewReader(normalizeText(f.data)))
	r.LazyQuotes = true
	r.FieldsPerRecord = -1
	records, err := r.ReadAll()
	if err != nil {
		im.warn("%s: the table could not be fully parsed (%v)", f.name, err)
	}
	if len(records) == 0 {
		return ""
	}
	header := records[0]
	rows := records[1:]
	if len(rows) > maxCSVRows {
		im.warn("%s: the table has %d rows; only the first %d are imported", f.name, len(rows), maxCSVRows)
		rows = rows[:maxCSVRows]
	}
	children := map[string][]*pageInfo{}
	for _, c := range im.childTitles[p.source] {
		children[c.rawTitle] = append(children[c.rawTitle], c)
	}
	dir := path.Dir(f.name)
	cols := len(header)
	cell := func(s string, first bool) string {
		linked := false
		s = cellLinkRe.ReplaceAllStringFunc(s, func(m string) string {
			sm := cellLinkRe.FindStringSubmatch(m)
			if k, ok := im.lookup(dir, sm[2]); ok {
				if pg, isPage := im.pages[k]; isPage {
					linked = true
					return "[[" + pg.title + "]]"
				}
			}
			im.warn("%s: a cell links to %q which is not an exported page; kept as text", p.source, truncRunes(sm[2], 120))
			return sm[1]
		})
		s = tagStripRe.ReplaceAllString(s, "")
		s = strings.NewReplacer("\n", " ", "\t", " ").Replace(strings.TrimSpace(s))
		if first && !linked && s != "" {
			key := cleanTitle(s)
			if list := children[key]; len(list) > 0 {
				s = "[[" + list[0].title + "]]"
				children[key] = list[1:] // each row page is linked once
			}
		}
		return strings.ReplaceAll(s, "|", `\|`)
	}
	var b strings.Builder
	writeRow := func(rec []string, isHeader bool) {
		b.WriteString("|")
		for i := 0; i < cols; i++ {
			v := ""
			if i < len(rec) {
				v = rec[i]
			}
			if isHeader {
				v = strings.ReplaceAll(strings.NewReplacer("\n", " ").Replace(tagStripRe.ReplaceAllString(strings.TrimSpace(v), "")), "|", `\|`)
			} else {
				v = cell(v, i == 0)
			}
			b.WriteString(" " + v + " |")
		}
		b.WriteString("\n")
	}
	writeRow(header, true)
	b.WriteString("|" + strings.Repeat(" --- |", cols) + "\n")
	for _, rec := range rows {
		writeRow(rec, false)
	}
	return strings.TrimRight(b.String(), "\n")
}

// propertyLine is a Notion page property as the export writes it under the title: "Status: Draft".
var propertyLine = regexp.MustCompile(`^([A-Z][A-Za-z0-9 /&();'.-]{0,40}): (\S.*)$`)

// viewLinkRe matches a body that is only a link to a CSV: a database view page.
var viewLinkRe = regexp.MustCompile(`^\[[^\]]*\]\(([^()\s]+\.csv)\)$`)

// splitProperties separates the block of "Key: value" lines Notion writes under a page title from the rest of the
// page. Only a block at the very top counts; a single line counts only when it stands alone before a blank line.
// The properties are returned in order as (name, value) pairs for frontMatter; without a block the lines are
// returned unchanged.
func splitProperties(lines []string) (props [][2]string, body string) {
	n := 0
	for n < len(lines) && propertyLine.MatchString(lines[n]) {
		n++
	}
	if n < 2 && !(n == 1 && (len(lines) == 1 || strings.TrimSpace(lines[1]) == "")) {
		return nil, strings.Join(lines, "\n")
	}
	for _, l := range lines[:n] {
		m := propertyLine.FindStringSubmatch(l)
		props = append(props, [2]string{m[1], m[2]})
	}
	rest := lines[n:]
	for len(rest) > 0 && strings.TrimSpace(rest[0]) == "" {
		rest = rest[1:]
	}
	return props, strings.Join(rest, "\n")
}

var nonKeyRe = regexp.MustCompile(`[^a-z0-9]+`)

// propKey normalises a property name to a front matter key: lowercase, runs of other characters become "-".
func propKey(name string) string {
	return strings.Trim(nonKeyRe.ReplaceAllString(strings.ToLower(name), "-"), "-")
}

var notionDateLayouts = []string{"January 2, 2006", "January 2, 2006 3:04 PM", "2006/01/02", "2006-01-02"}

// parseNotionDate reads a Notion date ("November 10, 2026", optionally with a time, or a range "A → B", whose
// start is used) and returns it as YYYY-MM-DD.
func parseNotionDate(v string) (string, bool) {
	v = strings.TrimSpace(v)
	if i := strings.Index(v, " → "); i >= 0 {
		v = strings.TrimSpace(v[:i])
	}
	for _, layout := range notionDateLayouts {
		if t, err := time.Parse(layout, v); err == nil {
			return t.Format("2006-01-02"), true
		}
	}
	return "", false
}

// frontMatter renders the page properties as the Obsidian-style block the app parses: "---", one "key: value" line
// per property, "---". Keys are normalised by propKey (empty keys skipped, first occurrence wins), a "Date" property
// becomes "date" (or "date-notion" with the raw text when it does not parse), and the first of Headline, Summary,
// TL;DR and Answer becomes "summary". Values are one line and never empty. It returns "" when nothing is left.
func (im *importer) frontMatter(p *pageInfo) string {
	var b strings.Builder
	seen := map[string]bool{}
	haveSummary := false
	for _, kv := range p.props {
		name := strings.ToLower(strings.TrimSpace(kv[0]))
		raw := strings.TrimSpace(kv[1])
		key := propKey(kv[0])
		val := raw
		switch {
		case name == "date":
			if d, ok := parseNotionDate(raw); ok {
				key, val = "date", d
			} else {
				key = "date-notion"
			}
		case !haveSummary && (name == "headline" || name == "summary" || name == "tl;dr" || name == "answer"):
			haveSummary = true
			key = "summary"
		}
		if key == "" || seen[key] {
			continue
		}
		if key != "date" {
			val = tagStripRe.ReplaceAllString(im.rewriteLinks(p, val), "")
		}
		val = strings.Join(strings.Fields(val), " ")
		if val == "" {
			continue
		}
		seen[key] = true
		b.WriteString(key + ": " + val + "\n")
	}
	if b.Len() == 0 {
		return ""
	}
	return "---\n" + b.String() + "---"
}
