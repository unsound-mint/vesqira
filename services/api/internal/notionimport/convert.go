package notionimport

import (
	"html"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

type outLine struct {
	text string
	code bool
}

// htmlConv reduces raw HTML in Markdown to text and collects output lines.
// Fenced code is passed through verbatim.
type htmlConv struct {
	lines     []outLine
	cur       strings.Builder
	depth     int // blockquote depth from <aside>
	skipUntil string
	hrefs     []string
}

var (
	tagRe  = regexp.MustCompile(`^<(/?)([a-zA-Z][a-zA-Z0-9-]*)((?:\s[^<>]*)?)>`)
	attrRe = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9:_-]*)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'>]+))`)
)

func attr(attrs, name string) string {
	for _, m := range attrRe.FindAllStringSubmatch(attrs, -1) {
		if strings.EqualFold(m[1], name) {
			v := m[2] + m[3] + m[4]
			return html.UnescapeString(v)
		}
	}
	return ""
}

func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

func (w *htmlConv) prefix(blank bool) string {
	if w.depth == 0 {
		return ""
	}
	p := strings.Repeat("> ", w.depth)
	if blank {
		p = strings.TrimRight(p, " ")
	}
	return p
}

func (w *htmlConv) flush() {
	t := w.cur.String()
	w.cur.Reset()
	w.lines = append(w.lines, outLine{text: w.prefix(t == "") + t})
}

func (w *htmlConv) breakLine() {
	if w.cur.Len() > 0 {
		w.flush()
	}
}

func (w *htmlConv) lastBlank() bool {
	n := len(w.lines)
	if n == 0 {
		return true
	}
	l := w.lines[n-1]
	return !l.code && strings.TrimSpace(strings.Trim(l.text, "> ")) == ""
}

func (w *htmlConv) blank() {
	w.breakLine()
	if !w.lastBlank() {
		w.flush()
	}
}

func (w *htmlConv) code(line string) {
	w.breakLine()
	w.lines = append(w.lines, outLine{text: w.prefix(false) + line, code: true})
}

func (w *htmlConv) tag(closing bool, name, attrs string) {
	switch name {
	case "aside":
		if closing {
			w.breakLine()
			if w.depth > 0 {
				w.depth--
			}
			w.blank()
		} else {
			w.blank()
			w.depth++
		}
	case "br":
		w.breakLine()
	case "script", "style":
		if !closing {
			w.skipUntil = "</" + name + ">"
		}
	case "li":
		w.breakLine()
		if !closing {
			w.cur.WriteString("- ")
		}
	case "h1", "h2", "h3", "h4", "h5", "h6":
		w.breakLine()
		if !closing {
			w.cur.WriteString(strings.Repeat("#", int(name[1]-'0')) + " ")
		}
	case "p", "div", "details", "summary", "ul", "ol", "table", "thead", "tbody", "tr", "blockquote", "figure", "figcaption", "hr", "pre":
		w.breakLine()
	case "td", "th":
		if closing {
			w.cur.WriteString(" ")
		}
	case "img":
		src := attr(attrs, "src")
		if src != "" {
			w.cur.WriteString("![" + cleanAlt(attr(attrs, "alt")) + "](<" + strings.NewReplacer("<", "%3C", ">", "%3E").Replace(src) + ">)")
		}
	case "a":
		if closing {
			if n := len(w.hrefs); n > 0 {
				h := w.hrefs[n-1]
				w.hrefs = w.hrefs[:n-1]
				if h != "" {
					w.cur.WriteString("](<" + strings.NewReplacer("<", "%3C", ">", "%3E").Replace(h) + ">)")
				}
			}
		} else {
			h := attr(attrs, "href")
			w.hrefs = append(w.hrefs, h)
			if h != "" {
				w.cur.WriteString("[")
			}
		}
	}
}

// processLine handles one non-code source line.
func (w *htmlConv) processLine(l string) {
	ll := asciiLower(l)
	wasBlank := strings.TrimSpace(l) == ""
	wrote := false
	i := 0
	for i < len(l) {
		if w.skipUntil != "" {
			idx := strings.Index(ll[i:], w.skipUntil)
			if idx < 0 {
				i = len(l)
				break
			}
			i += idx + len(w.skipUntil)
			w.skipUntil = ""
			continue
		}
		switch l[i] {
		case '`':
			n := 1
			for i+n < len(l) && l[i+n] == '`' {
				n++
			}
			run := l[i : i+n]
			end := strings.Index(l[i+n:], run)
			if end < 0 {
				w.cur.WriteString(run)
				i += n
			} else {
				w.cur.WriteString(l[i : i+n+end+n])
				i += n + end + n
			}
			wrote = true
		case '<':
			if strings.HasPrefix(l[i:], "<!--") {
				end := strings.Index(l[i+4:], "-->")
				if end < 0 {
					w.skipUntil = "-->"
					i = len(l)
				} else {
					i += 4 + end + 3
				}
				continue
			}
			if m := tagRe.FindStringSubmatch(l[i:]); m != nil {
				w.tag(m[1] == "/", asciiLower(m[2]), m[3])
				i += len(m[0])
				continue
			}
			w.cur.WriteByte('<')
			i++
			wrote = true
		default:
			j := i
			for j < len(l) && l[j] != '`' && l[j] != '<' {
				j++
			}
			w.cur.WriteString(l[i:j])
			i = j
			wrote = true
		}
	}
	switch {
	case w.cur.Len() > 0:
		w.flush()
	case wasBlank:
		if !w.lastBlank() {
			w.flush()
		}
	case wrote:
		w.flush()
	}
}

// convertPage converts a Markdown page body: HTML to text, then links and images.
func (im *importer) convertPage(p *pageInfo) {
	w := &htmlConv{}
	fence := ""
	for _, l := range strings.Split(inlineCalloutIcons(removeViewSettings(p.body)), "\n") {
		t := strings.TrimLeft(l, " ")
		if fence != "" {
			w.code(l)
			if strings.HasPrefix(t, fence) && strings.Trim(t, fence[:1]) == "" {
				fence = ""
			}
			continue
		}
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			n := 0
			for n < len(t) && t[n] == t[0] {
				n++
			}
			fence = t[:n]
			w.code(l)
			continue
		}
		w.processLine(l)
	}
	w.breakLine()
	out := make([]string, 0, len(w.lines))
	for _, l := range w.lines {
		if l.code {
			out = append(out, l.text)
		} else {
			out = append(out, im.rewriteLinks(p, l.text))
		}
	}
	p.out = strings.Trim(strings.Join(out, "\n"), "\n")
	if fm := im.frontMatter(p); fm != "" {
		if p.out != "" {
			fm += "\n\n"
		}
		p.out = fm + p.out
	}
}

// fenceOpen reports whether line opens a code fence and returns the fence marker.
func fenceOpen(line string) (string, bool) {
	t := strings.TrimLeft(line, " ")
	if !strings.HasPrefix(t, "```") && !strings.HasPrefix(t, "~~~") {
		return "", false
	}
	n := 0
	for n < len(t) && t[n] == t[0] {
		n++
	}
	return t[:n], true
}

// outsideFence marks, per line, whether it is outside fenced code.
func outsideFence(lines []string) []bool {
	out := make([]bool, len(lines))
	fence := ""
	for i, l := range lines {
		if fence != "" {
			t := strings.TrimLeft(l, " ")
			if strings.HasPrefix(t, fence) && strings.Trim(t, fence[:1]) == "" {
				fence = ""
			}
			continue
		}
		if f, ok := fenceOpen(l); ok {
			fence = f
			continue
		}
		out[i] = true
	}
	return out
}

var sortValueRe = regexp.MustCompile(`^[^:\n]+: (ascending|descending)$`)

// removeViewSettings drops the database view settings Notion writes into a page: a "sort:" line, the
// "Name: ascending" lines after it, and the blank line they leave.
func removeViewSettings(body string) string {
	if !strings.Contains(body, "sort:") {
		return body
	}
	lines := strings.Split(body, "\n")
	text := outsideFence(lines)
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		if text[i] && strings.TrimRight(lines[i], " \t") == "sort:" {
			j := i + 1
			for j < len(lines) && text[j] && sortValueRe.MatchString(strings.TrimSpace(lines[j])) {
				j++
			}
			if j > i+1 {
				if j < len(lines) && strings.TrimSpace(lines[j]) == "" {
					j++
				}
				i = j - 1
				continue
			}
		}
		out = append(out, lines[i])
	}
	return strings.Join(out, "\n")
}

// isIconLine reports whether a line is a callout icon: 1 to 8 runes with no letters or digits.
func isIconLine(l string) bool {
	l = strings.TrimSpace(l)
	n := utf8.RuneCountInString(l)
	if n < 1 || n > 8 {
		return false
	}
	return strings.IndexFunc(l, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) < 0
}

// inlineCalloutIcons moves the icon Notion writes on its own line after "<aside>" in front of the callout's first
// text line, so the blockquote starts "> 🧭 text" instead of a lone emoji. An icon with no text after it is dropped.
func inlineCalloutIcons(body string) string {
	if !strings.Contains(body, "<aside>") {
		return body
	}
	lines := strings.Split(body, "\n")
	text := outsideFence(lines)
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		out = append(out, lines[i])
		if !text[i] || strings.TrimSpace(lines[i]) != "<aside>" || i+1 >= len(lines) || !isIconLine(lines[i+1]) {
			continue
		}
		icon := strings.TrimSpace(lines[i+1])
		j := i + 2
		for j < len(lines) && strings.TrimSpace(lines[j]) == "" {
			j++
		}
		if j < len(lines) && strings.TrimSpace(lines[j]) != "</aside>" {
			out = append(out, icon+" "+strings.TrimLeft(lines[j], " "))
			i = j
		} else {
			i++ // lone icon
		}
	}
	return strings.Join(out, "\n")
}

// rewriteLinks rewrites Markdown links and images on one line, leaving inline
// code and escaped characters alone.
func (im *importer) rewriteLinks(p *pageInfo, s string) string {
	if !strings.Contains(s, "](") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		switch s[i] {
		case '`':
			n := 1
			for i+n < len(s) && s[i+n] == '`' {
				n++
			}
			run := s[i : i+n]
			end := strings.Index(s[i+n:], run)
			if end < 0 {
				b.WriteString(run)
				i += n
			} else {
				b.WriteString(s[i : i+n+end+n])
				i += n + end + n
			}
		case '\\':
			b.WriteByte('\\')
			i++
			if i < len(s) {
				b.WriteByte(s[i])
				i++
			}
		case '!':
			if i+1 < len(s) && s[i+1] == '[' {
				if text, dest, end, ok := parseLink(s, i+1); ok {
					b.WriteString(im.imageOut(p, text, dest))
					i = end
					continue
				}
			}
			b.WriteByte('!')
			i++
		case '[':
			if text, dest, end, ok := parseLink(s, i); ok {
				b.WriteString(im.linkOut(p, text, dest))
				i = end
				continue
			}
			b.WriteByte('[')
			i++
		default:
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String()
}

// parseLink parses "[text](dest "title")" starting at s[i] == '['. It returns the
// text, the destination and the index just after the closing parenthesis.
func parseLink(s string, i int) (text, dest string, end int, ok bool) {
	depth := 0
	j := i
	for ; j < len(s); j++ {
		c := s[j]
		if c == '\\' {
			j++
			continue
		}
		if c == '[' {
			depth++
		} else if c == ']' {
			depth--
			if depth == 0 {
				break
			}
		}
	}
	if j >= len(s) || j+1 >= len(s) || s[j+1] != '(' {
		return "", "", 0, false
	}
	text = s[i+1 : j]
	k := j + 2
	for k < len(s) && s[k] == ' ' {
		k++
	}
	if k < len(s) && s[k] == '<' {
		e := strings.IndexByte(s[k+1:], '>')
		if e < 0 {
			return "", "", 0, false
		}
		dest = s[k+1 : k+1+e]
		k += e + 2
	} else {
		start := k
		pd := 0
		for k < len(s) {
			c := s[k]
			if c == '\\' {
				k += 2
				continue
			}
			if c == ' ' || c == '\t' {
				break
			}
			if c == '(' {
				pd++
			} else if c == ')' {
				if pd == 0 {
					break
				}
				pd--
			}
			k++
		}
		if k > len(s) {
			k = len(s)
		}
		dest = s[start:k]
	}
	for k < len(s) && s[k] == ' ' {
		k++
	}
	if k < len(s) && (s[k] == '"' || s[k] == '\'') {
		q := s[k]
		e := strings.IndexByte(s[k+1:], q)
		if e < 0 {
			return "", "", 0, false
		}
		k += e + 2
		for k < len(s) && s[k] == ' ' {
			k++
		}
	}
	if k >= len(s) || s[k] != ')' {
		return "", "", 0, false
	}
	return text, dest, k + 1, true
}
