package notionimport

import (
	"html"
	"regexp"
	"strings"
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
	for _, l := range strings.Split(p.body, "\n") {
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
