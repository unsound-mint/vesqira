package regions

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Import statuses. A region is exact when the whole-pixel shape selects precisely the pixels the DS9 shape selects by
// the pixel-centre rule; approximated when it had to be rounded (the note says how); skipped when it cannot be read.
const (
	Exact       = "exact"
	Approximate = "approximated"
	Skipped     = "skipped"
)

type ImportItem struct {
	Line   int    `json:"line"`
	Text   string `json:"text"`
	Status string `json:"status"`
	Name   string `json:"name,omitempty"`
	Shape  *Shape `json:"shape,omitempty"`
	Note   string `json:"note,omitempty"`
}

const (
	maxImportLines = 5000
	eps            = 1e-9
)

// ParseDS9 reads a DS9 region file in image or physical coordinates: boxes with no rotation and circles. DS9 counts
// pixels from 1 with a pixel's centre at its integer number. Anything else (sky coordinates, other shapes, rotated
// boxes, excluded regions) is reported as skipped with the reason, never dropped silently.
func ParseDS9(text string) []ImportItem {
	var out []ImportItem
	system := "physical" // DS9's default when no system line precedes a shape
	n := 0
	for i, raw := range strings.Split(text, "\n") {
		if i >= maxImportLines {
			out = append(out, ImportItem{Line: i + 1, Status: Skipped, Note: fmt.Sprintf("only the first %d lines are read", maxImportLines)})
			break
		}
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "global") {
			continue
		}
		it := ImportItem{Line: i + 1, Text: clip(line, 120)}
		body, comment := line, ""
		if k := strings.Index(line, "#"); k >= 0 {
			body, comment = strings.TrimSpace(line[:k]), line[k+1:]
		}
		low := strings.ToLower(body)
		switch low {
		case "image", "physical", "linear":
			system = low
			continue
		case "fk5", "icrs", "fk4", "galactic", "ecliptic", "j2000", "b1950", "wcs":
			system = "sky"
			continue
		}
		if strings.Contains(body, ";") { // "fk5; circle(...)" on one line
			parts := strings.SplitN(body, ";", 2)
			head := strings.ToLower(strings.TrimSpace(parts[0]))
			if head == "image" || head == "physical" || head == "linear" {
				system, body = head, strings.TrimSpace(parts[1])
			} else {
				system, body = "sky", strings.TrimSpace(parts[1])
			}
		}
		n++
		it.Name = labelOf(comment)
		if it.Name == "" {
			it.Name = fmt.Sprintf("Imported region %d", n)
		}
		if strings.HasPrefix(body, "-") {
			it.Status, it.Note = Skipped, "an excluded region (a hole) has no equivalent here"
			out = append(out, it)
			continue
		}
		open, close := strings.Index(body, "("), strings.LastIndex(body, ")")
		if open < 0 || close < open {
			it.Status, it.Note = Skipped, "not a shape this reader understands"
			out = append(out, it)
			continue
		}
		shape := strings.ToLower(strings.TrimSpace(body[:open]))
		args, err := numbers(body[open+1 : close])
		switch {
		case system == "sky":
			it.Status, it.Note = Skipped, "sky coordinates need the dataset's WCS; export from DS9 in image coordinates"
		case err != nil:
			it.Status, it.Note = Skipped, "its numbers are not plain image coordinates ("+err.Error()+")"
		case shape == "box" && len(args) >= 4:
			if len(args) >= 5 && math.Abs(math.Mod(args[4], 360)) > eps {
				it.Status, it.Note = Skipped, "a rotated box is not a whole-pixel rectangle"
				break
			}
			xc, yc, w, h := args[0], args[1], args[2], args[3]
			x0, x1 := int(math.Ceil(xc-w/2-eps))-1, int(math.Floor(xc+w/2+eps))-1
			y0, y1 := int(math.Ceil(yc-h/2-eps))-1, int(math.Floor(yc+h/2+eps))-1
			if x0 < 0 || y0 < 0 || x1 < x0 || y1 < y0 {
				it.Status, it.Note = Skipped, "it selects no pixel inside the image grid"
				break
			}
			it.Shape, it.Status = &Shape{Shape: "rect", X0: x0, Y0: y0, X1: x1, Y1: y1}, Exact
			it.Note = "selects the pixels whose centres lie inside the box"
		case shape == "circle" && len(args) >= 3:
			cx, cy, r := args[0]-1, args[1]-1, args[2]
			if cx < -eps || cy < -eps || r < 0 {
				it.Status, it.Note = Skipped, "its centre or radius is outside the image grid"
				break
			}
			rcx, rcy, rr := int(math.Round(cx)), int(math.Round(cy)), int(math.Round(r))
			it.Shape = &Shape{Shape: "circle", CX: rcx, CY: rcy, R: rr}
			if math.Abs(cx-float64(rcx)) < eps && math.Abs(cy-float64(rcy)) < eps && math.Floor(r*r+eps) == float64(rr*rr) {
				it.Status, it.Note = Exact, "selects the pixels whose centres lie within the radius"
			} else {
				it.Status = Approximate
				it.Note = fmt.Sprintf("saved regions use a whole-pixel centre and radius: centre (%.2f, %.2f) became (%d, %d) and radius %.2f became %d, so it may select a few other pixels", cx+1, cy+1, rcx+1, rcy+1, r, rr)
			}
		default:
			it.Status, it.Note = Skipped, "the shape "+clip(shape, 20)+" is not supported (boxes and circles are)"
		}
		out = append(out, it)
	}
	return out
}

func numbers(s string) ([]float64, error) {
	var out []float64
	for _, f := range strings.Split(s, ",") {
		f = strings.TrimSpace(f)
		v, err := strconv.ParseFloat(f, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, fmt.Errorf("%q", clip(f, 20))
		}
		out = append(out, v)
	}
	return out, nil
}

// labelOf returns the text={...} label from a DS9 trailing comment.
func labelOf(comment string) string {
	if i := strings.Index(comment, "text={"); i >= 0 {
		rest := comment[i+len("text={"):]
		if j := strings.Index(rest, "}"); j >= 0 {
			if n, err := cleanName(rest[:j]); err == nil {
				return n
			}
		}
	}
	return ""
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
