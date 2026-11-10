// Package notionimport turns a Notion "Markdown & CSV" export (a zip, optionally
// holding one level of inner part zips) into Vesqira report drafts.
//
// The archive is untrusted. Everything happens in memory; nothing is written to
// disk. Entry names are validated, symlinks are ignored, and entry count, total
// expanded bytes (counted while reading, not taken from headers) and per-entry
// bytes are capped.
//
// Conversion choices:
//   - a link to an exported page becomes [[Title]]; when the link text differs
//     from the page title it becomes "[[Title]] (link text)" so no wording is lost;
//   - a link to something that is not an exported page becomes its plain text
//     (external http, https and mailto links are kept as ordinary links);
//   - images become attachments (PNG, JPEG, GIF, at most 4096 x 4096) referenced
//     as ![alt](attachment:<sha256>); anything else is replaced by a plain
//     "(image omitted: ...)" note with a warning;
//   - raw HTML is reduced to text; <aside> becomes a blockquote;
//   - titles are made safe for [[wikilinks]]: "[" and "]" become "(" and ")",
//     "|" becomes "-".
package notionimport

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
)

// Options bounds an import. Zero values select the defaults.
type Options struct {
	MaxEntries    int   // default 50_000
	MaxTotalBytes int64 // uncompressed, default 2 GiB
	MaxFileBytes  int64 // per entry, default 64 MiB
	MaxImageBytes int64 // default 8 MiB; larger images are skipped with a warning
}

// Image is a decoded-and-verified attachment.
type Image struct {
	SHA256 string
	Mime   string
	Data   []byte
	Name   string
}

// Page is one draft report.
type Page struct {
	Title    string // from the "# " heading, else the file name without the id
	Source   string // cleaned path inside the export
	Parent   string // Source of the parent page, "" for top level
	Markdown string // converted body, title heading removed
}

// Result is the outcome of an import.
type Result struct {
	Pages    []Page
	Images   []Image
	Warnings []string
}

const (
	defaultMaxEntries = 50_000
	defaultMaxTotal   = int64(2) << 30
	defaultMaxFile    = int64(64) << 20
	defaultMaxImage   = int64(8) << 20
	maxImageSide      = 4096
	maxCSVRows        = 2000
	maxTitleRunes     = 200
	maxWarnings       = 500
	maxStoredImages   = int64(512) << 20
)

type kind int

const (
	kindOther kind = iota
	kindPage
	kindCSV
	kindImage // png, jpg, jpeg, gif candidates
)

type file struct {
	name     string
	kind     kind
	data     []byte
	tooLarge bool
}

type importer struct {
	opt         Options
	entries     int
	total       int64
	storedImg   int64
	files       map[string]*file
	order       []string
	warnings    []string
	suppressed  int
	pages       map[string]*pageInfo // by Source
	byID        map[string]*pageInfo
	byBase      map[string][]*pageInfo
	alias       map[string]string // dropped Source -> kept Source
	images      map[string]*imgRes
	imageList   []Image
	imageBySHA  map[string]bool
	childTitles map[string][]*pageInfo
}

var (
	driveRe = regexp.MustCompile(`^[A-Za-z]:`)
	idRe    = regexp.MustCompile(`\s*([0-9a-f]{32})$`)
)

// Import converts a Notion export read from r.
func Import(r io.ReaderAt, size int64, opt Options) (Result, error) {
	if opt.MaxEntries <= 0 {
		opt.MaxEntries = defaultMaxEntries
	}
	if opt.MaxTotalBytes <= 0 {
		opt.MaxTotalBytes = defaultMaxTotal
	}
	if opt.MaxFileBytes <= 0 {
		opt.MaxFileBytes = defaultMaxFile
	}
	if opt.MaxImageBytes <= 0 {
		opt.MaxImageBytes = defaultMaxImage
	}
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return Result{}, fmt.Errorf("the file is not a readable zip archive: %v", err)
	}
	im := &importer{
		opt:        opt,
		files:      map[string]*file{},
		pages:      map[string]*pageInfo{},
		byID:       map[string]*pageInfo{},
		byBase:     map[string][]*pageInfo{},
		alias:      map[string]string{},
		images:     map[string]*imgRes{},
		imageBySHA: map[string]bool{},
	}
	if err := im.addZip(zr, 0); err != nil {
		return Result{}, err
	}
	if err := im.build(); err != nil {
		return Result{}, err
	}
	return im.result(), nil
}

func (im *importer) warn(format string, args ...any) {
	if len(im.warnings) >= maxWarnings {
		im.suppressed++
		return
	}
	im.warnings = append(im.warnings, fmt.Sprintf(format, args...))
}

// cleanName validates an entry name. skip is true for names that are valid but
// carry nothing (directories, macOS metadata).
func cleanName(raw string) (name string, skip bool, err error) {
	if strings.ContainsRune(raw, 0) {
		return "", false, fmt.Errorf("the archive contains an entry whose name has a NUL byte (%q); refusing to import it", raw)
	}
	if strings.Contains(raw, `\`) || driveRe.MatchString(raw) {
		return "", false, fmt.Errorf("the archive contains an entry with a backslash or drive-letter path (%q); refusing to import it", raw)
	}
	if strings.HasPrefix(raw, "/") {
		return "", false, fmt.Errorf("the archive contains an entry with an absolute path (%q); refusing to import it", raw)
	}
	c := path.Clean(raw)
	if c == "." {
		return "", true, nil
	}
	if c == ".." || strings.HasPrefix(c, "../") {
		return "", false, fmt.Errorf("the archive contains an entry that escapes the export folder (%q); refusing to import it", raw)
	}
	if strings.HasSuffix(raw, "/") {
		return c, true, nil
	}
	if strings.HasPrefix(c, "__MACOSX/") || path.Base(c) == ".DS_Store" {
		return c, true, nil
	}
	return c, false, nil
}

var errTotal = errors.New("total")

type budgetReader struct {
	r        io.Reader
	im       *importer
	n        int64
	maxFile  int64
	fileOver bool
}

func (b *budgetReader) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	b.im.total += int64(n)
	b.n += int64(n)
	if b.im.total > b.im.opt.MaxTotalBytes {
		return n, errTotal
	}
	if b.n > b.maxFile {
		b.fileOver = true
		return n, io.EOF
	}
	return n, err
}

func (im *importer) totalErr() error {
	return fmt.Errorf("the archive expands to more than %d bytes uncompressed (limit MaxTotalBytes); refusing to import it", im.opt.MaxTotalBytes)
}

// read reads one entry with all budgets. limit is the per-entry cap; exceeding
// it sets over instead of failing.
func (im *importer) read(f *zip.File, limit int64) (data []byte, over bool, err error) {
	rc, err := f.Open()
	if err != nil {
		return nil, false, fmt.Errorf("cannot open archive entry %q: %v", f.Name, err)
	}
	defer rc.Close()
	br := &budgetReader{im: im, maxFile: limit}
	br.r = io.LimitReader(rc, limit+1)
	var buf bytes.Buffer
	_, err = io.Copy(&buf, br)
	if errors.Is(err, errTotal) {
		return nil, false, im.totalErr()
	}
	if err != nil {
		return nil, false, fmt.Errorf("cannot read archive entry %q: %v", f.Name, err)
	}
	if br.fileOver {
		return nil, true, nil
	}
	return buf.Bytes(), false, nil
}

func (im *importer) addZip(zr *zip.Reader, depth int) error {
	if im.entries+len(zr.File) > im.opt.MaxEntries {
		return fmt.Errorf("the archive has more than %d entries (limit MaxEntries); refusing to import it", im.opt.MaxEntries)
	}
	im.entries += len(zr.File)
	for _, f := range zr.File {
		name, skip, err := cleanName(f.Name)
		if err != nil {
			return err
		}
		if skip {
			continue
		}
		if f.Mode()&fs.ModeSymlink != 0 {
			im.warn("%s: symbolic link ignored", name)
			continue
		}
		if _, dup := im.files[name]; dup {
			im.warn("%s: duplicate entry ignored", name)
			continue
		}
		ext := strings.ToLower(path.Ext(name))
		switch {
		case ext == ".zip":
			if depth > 0 {
				im.warn("%s: nested archive inside a part archive ignored", name)
				continue
			}
			data, over, err := im.read(f, im.opt.MaxFileBytes)
			if err != nil {
				return err
			}
			if over {
				return im.fileErr(name)
			}
			inner, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
			if err != nil {
				im.warn("%s: not a readable zip archive, ignored", name)
				continue
			}
			if err := im.addZip(inner, depth+1); err != nil {
				return err
			}
		case ext == ".md" || ext == ".markdown" || ext == ".csv":
			data, over, err := im.read(f, im.opt.MaxFileBytes)
			if err != nil {
				return err
			}
			if over {
				return im.fileErr(name)
			}
			k := kindPage
			if ext == ".csv" {
				k = kindCSV
			}
			im.put(&file{name: name, kind: k, data: data})
		case ext == ".png" || ext == ".jpg" || ext == ".jpeg" || ext == ".gif":
			data, over, err := im.read(f, im.opt.MaxImageBytes)
			if err != nil {
				return err
			}
			fl := &file{name: name, kind: kindImage}
			switch {
			case over:
				fl.tooLarge = true
			case im.storedImg+int64(len(data)) > maxStoredImages:
				fl.tooLarge = true
			default:
				im.storedImg += int64(len(data))
				fl.data = data
			}
			im.put(fl)
		default:
			// Other files (videos, PDFs, attachments Notion exported) are never imported, so they are not
			// decompressed at all: a large video in the export neither aborts the import nor uses the budget.
			im.put(&file{name: name, kind: kindOther})
		}
	}
	return nil
}

func (im *importer) fileErr(name string) error {
	return fmt.Errorf("the archive entry %q is larger than %d bytes uncompressed (limit MaxFileBytes); refusing to import it", name, im.opt.MaxFileBytes)
}

func (im *importer) put(f *file) {
	im.files[f.name] = f
	im.order = append(im.order, f.name)
}

func (im *importer) result() Result {
	res := Result{Images: im.imageList, Warnings: im.warnings}
	if im.suppressed > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("%d further warnings were suppressed", im.suppressed))
	}
	srcs := make([]string, 0, len(im.pages))
	for s := range im.pages {
		srcs = append(srcs, s)
	}
	sort.Strings(srcs)
	for _, s := range srcs {
		p := im.pages[s]
		res.Pages = append(res.Pages, Page{Title: p.title, Source: p.source, Parent: p.parent, Markdown: p.out})
	}
	return res
}
