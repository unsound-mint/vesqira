package reports

import (
	"regexp"
	"strconv"
	"strings"
)

// Reference forms in a report body (ADR 0030):
//
//	![[figure:<64 hex>]]   a pinned figure revision, shown as the picture with its provenance
//	[[run:<id>]]           a run, shown as its reviewer-safe summary
//	[[Title]] [[Title|label]]  another report by title (case ignored)
//	![alt](attachment:<sha256>)  an uploaded image
//
// A figure or run reference alone on its line becomes a block of its own; elsewhere it is an inline mention.
var (
	figureRe     = regexp.MustCompile(`!\[\[figure:([0-9a-f]{64})\]\]`)
	runRe        = regexp.MustCompile(`\[\[run:([0-9]{1,18})\]\]`)
	wikiRe       = regexp.MustCompile(`\[\[([^\[\]|#]+?)(\|[^\[\]]*)?\]\]`)
	attachmentRe = regexp.MustCompile(`\]\(attachment:([0-9a-f]{64})\)`)
	figureLine   = regexp.MustCompile(`^\s*!\[\[figure:([0-9a-f]{64})\]\]\s*$`)
	runLine      = regexp.MustCompile(`^\s*\[\[run:([0-9]{1,18})\]\]\s*$`)
	fenceRe      = regexp.MustCompile("^\\s{0,3}(```+|~~~+)")
)

// Link is one outgoing reference: kind report (target is the lower-cased title), figure (revision), run (id) or
// attachment (sha256).
type Link struct{ Kind, Target string }

// Links lists the distinct references in a body, outside fenced code.
func Links(body string) []Link {
	seen := map[Link]bool{}
	var out []Link
	add := func(l Link) {
		if !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	for _, b := range Blocks(body) {
		switch b.Kind {
		case "figure":
			add(Link{"figure", b.Figure})
			continue
		case "run":
			add(Link{"run", strconv.FormatInt(b.Run, 10)})
			continue
		case "code":
			continue
		}
		for _, m := range figureRe.FindAllStringSubmatch(b.Text, -1) {
			add(Link{"figure", m[1]})
		}
		for _, m := range runRe.FindAllStringSubmatch(b.Text, -1) {
			add(Link{"run", m[1]})
		}
		for _, m := range wikiRe.FindAllStringSubmatch(b.Text, -1) {
			if isRef(m[1]) {
				continue
			}
			add(Link{"report", strings.ToLower(strings.Join(strings.Fields(m[1]), " "))})
		}
		for _, m := range attachmentRe.FindAllStringSubmatch(b.Text, -1) {
			add(Link{"attachment", m[1]})
		}
	}
	return out
}

// Block is a unit of a report a comment can be anchored to: a paragraph, heading, list, table, quote, code or math
// block, or a figure or run reference on its own line.
type Block struct {
	Kind   string `json:"kind"`             // text | code | figure | run
	Text   string `json:"text,omitempty"`   // the Markdown of the block (text and code)
	Figure string `json:"figure,omitempty"` // figure revision
	Run    int64  `json:"run,omitempty"`    // run id
}

// Blocks splits a body into blocks. Blank lines separate blocks, except inside fenced code and $$ math; a heading is
// always its own block; a figure or run reference alone on a line is its own block.
func Blocks(body string) []Block {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	var out []Block
	var cur []string
	flush := func() {
		if len(cur) > 0 {
			t := strings.Trim(strings.Join(cur, "\n"), "\n")
			if strings.TrimSpace(t) != "" {
				out = append(out, Block{Kind: "text", Text: t})
			}
			cur = nil
		}
	}
	for i := 0; i < len(lines); i++ {
		ln := lines[i]
		if m := fenceRe.FindStringSubmatch(ln); m != nil {
			flush()
			marker := strings.TrimSpace(m[1])
			block := []string{ln}
			for i++; i < len(lines); i++ {
				block = append(block, lines[i])
				if strings.HasPrefix(strings.TrimSpace(lines[i]), marker[:3]) && strings.Trim(strings.TrimSpace(lines[i]), string(marker[0])) == "" {
					break
				}
			}
			out = append(out, Block{Kind: "code", Text: strings.Join(block, "\n")})
			continue
		}
		if t := strings.TrimSpace(ln); strings.HasPrefix(t, "$$") && !strings.HasSuffix(t[2:], "$$") { // display math over several lines
			flush()
			block := []string{ln}
			for i++; i < len(lines); i++ {
				block = append(block, lines[i])
				if strings.HasSuffix(strings.TrimSpace(lines[i]), "$$") {
					break
				}
			}
			out = append(out, Block{Kind: "text", Text: strings.Join(block, "\n")})
			continue
		}
		if m := figureLine.FindStringSubmatch(ln); m != nil {
			flush()
			out = append(out, Block{Kind: "figure", Figure: m[1]})
			continue
		}
		if m := runLine.FindStringSubmatch(ln); m != nil {
			flush()
			id, _ := strconv.ParseInt(m[1], 10, 64)
			out = append(out, Block{Kind: "run", Run: id})
			continue
		}
		if strings.TrimSpace(ln) == "" {
			flush()
			continue
		}
		if strings.HasPrefix(strings.TrimLeft(ln, " "), "#") && isHeading(strings.TrimLeft(ln, " ")) {
			flush()
			out = append(out, Block{Kind: "text", Text: ln})
			continue
		}
		cur = append(cur, ln)
	}
	flush()
	return out
}

// isRef is true for the typed reference forms ([[figure:…]], [[run:…]], [[report:…]]), which are not report titles.
func isRef(inner string) bool {
	inner = strings.TrimSpace(inner)
	return strings.HasPrefix(inner, "figure:") || strings.HasPrefix(inner, "run:") || strings.HasPrefix(inner, "report:")
}

func isHeading(s string) bool {
	n := 0
	for n < len(s) && s[n] == '#' {
		n++
	}
	return n >= 1 && n <= 6 && (n == len(s) || s[n] == ' ')
}

// ReportTitles returns the distinct report titles a text links to, as written (first spelling wins).
func ReportTitles(text string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range wikiRe.FindAllStringSubmatch(text, -1) {
		if isRef(m[1]) {
			continue
		}
		t := strings.Join(strings.Fields(m[1]), " ")
		if k := strings.ToLower(t); !seen[k] {
			seen[k] = true
			out = append(out, t)
		}
	}
	return out
}

// RewriteWikiLinks replaces each [[Title]] / [[Title|label]] with what f returns for the title and label (label is
// "" when absent). Fenced code is the caller's concern: call it on text blocks only.
func RewriteWikiLinks(text string, f func(title, label string) string) string {
	return wikiRe.ReplaceAllStringFunc(text, func(m string) string {
		sm := wikiRe.FindStringSubmatch(m)
		if isRef(sm[1]) {
			return m
		}
		label := strings.TrimPrefix(sm[2], "|")
		return f(strings.Join(strings.Fields(sm[1]), " "), strings.TrimSpace(label))
	})
}

// RewriteInline replaces inline figure and run mentions (not on their own line) with what the callbacks return.
func RewriteInline(text string, fig func(rev string) string, run func(id int64) string) string {
	text = figureRe.ReplaceAllStringFunc(text, func(m string) string { return fig(figureRe.FindStringSubmatch(m)[1]) })
	return runRe.ReplaceAllStringFunc(text, func(m string) string {
		id, _ := strconv.ParseInt(runRe.FindStringSubmatch(m)[1], 10, 64)
		return run(id)
	})
}

// RewriteAttachments replaces attachment:<sha> image targets with what f returns for the sha.
func RewriteAttachments(text string, f func(sha string) string) string {
	return attachmentRe.ReplaceAllStringFunc(text, func(m string) string { return "](" + f(attachmentRe.FindStringSubmatch(m)[1]) + ")" })
}
