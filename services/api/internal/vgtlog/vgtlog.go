// Package vgtlog reads progress out of a VGT job's log. The Julia pipeline logs one block per finished chunk:
//
//	[ Info: Done chunk
//	│   chunk = 3
//	│   chunks = 16
//	│   y_start = …
//	│   y_end = …
//	└   elapsed_s = 12.3
//
// Chunks run on threads and may finish out of order, so progress is the number of distinct chunk numbers seen, never the
// highest one. A log without such blocks says nothing about progress, and nothing is guessed.
package vgtlog

import (
	"strconv"
	"strings"
	"time"
)

// Progress is how many of the chunks have been logged as done.
type Progress struct {
	Done  int `json:"done"`
	Total int `json:"total"`
}

const maxChunks = 1_000_000

// Parse returns the progress recorded in the log text, or false when it holds no usable "Done chunk" block. A block cut
// off before its "chunks" line is ignored. If blocks disagree on the total (another run's lines mixed in), the total
// of the last block wins and only chunks logged with that total are counted.
func Parse(log string) (Progress, bool) {
	seen := map[int]map[int]bool{} // total -> distinct chunk numbers
	lastTotal := 0
	inBlock := false
	chunk, total := 0, 0
	flush := func() {
		if chunk >= 1 && total >= 1 && chunk <= total {
			if seen[total] == nil {
				seen[total] = map[int]bool{}
			}
			seen[total][chunk] = true
			lastTotal = total
		}
		chunk, total = 0, 0
	}
	for _, line := range strings.Split(log, "\n") {
		line = strings.TrimRight(line, "\r ")
		switch {
		case line == "[ Info: Done chunk":
			if inBlock {
				flush()
			}
			inBlock = true
		case inBlock && (strings.HasPrefix(line, "│") || strings.HasPrefix(line, "└")):
			if k, v, ok := field(line); ok {
				switch k {
				case "chunk":
					chunk = v
				case "chunks":
					total = v
				}
			}
		default:
			if inBlock {
				flush()
			}
			inBlock = false
		}
	}
	if inBlock {
		flush()
	}
	if lastTotal == 0 {
		return Progress{}, false
	}
	return Progress{Done: len(seen[lastTotal]), Total: lastTotal}, true
}

// field reads "│   name = 123" as (name, 123). Non-integer values and absurd sizes are refused.
func field(line string) (string, int, bool) {
	line = strings.TrimLeft(strings.TrimLeft(line, "│└"), " \t")
	k, v, ok := strings.Cut(line, "=")
	if !ok {
		return "", 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 0 || n > maxChunks {
		return "", 0, false
	}
	return strings.TrimSpace(k), n, true
}

// TailLimit is how many bytes of each log file the job log command returns. A file that fills it has lost its earlier
// lines, so chunks logged before them cannot be counted.
const TailLimit = 12000

// ParseTail is Parse for the output of the job log command: sections of tailed files, each introduced by a line
// "=== <file>". When a section is as long as the tail allows, the log was cut and the count could be too low, so no
// progress is reported rather than a wrong one.
func ParseTail(out string) (Progress, bool) {
	var all strings.Builder
	for i, sec := range strings.Split("\n"+out, "\n=== ") {
		if i == 0 {
			all.WriteString(sec)
			continue
		}
		_, body, _ := strings.Cut(sec, "\n")
		if len(body) >= TailLimit-100 && strings.Contains(body, "Done chunk") {
			return Progress{}, false
		}
		all.WriteString("\n" + body)
	}
	return Parse(all.String())
}

// ETA estimates the seconds left at the rate so far: elapsed × (total−done)/done. It is only an estimate; false when
// nothing is done yet, the job has no known start, or it is already complete.
func ETA(p Progress, started, now time.Time) (int64, bool) {
	if p.Done <= 0 || p.Done >= p.Total || started.IsZero() || !now.After(started) {
		return 0, false
	}
	return int64(now.Sub(started).Seconds() * float64(p.Total-p.Done) / float64(p.Done)), true
}
