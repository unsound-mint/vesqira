package vgtlog

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func block(chunk, chunks int) string {
	return "[ Info: Done chunk\n│   chunk = " + strconv.Itoa(chunk) + "\n│   chunks = " + strconv.Itoa(chunks) + "\n│   y_start = 1\n│   y_end = 9\n└   elapsed_s = 12.3\n"
}

func TestParse(t *testing.T) {
	cases := []struct {
		name string
		log  string
		want Progress
		ok   bool
	}{
		{"no lines", "starting\nloading cube\n", Progress{}, false},
		{"empty", "", Progress{}, false},
		{"in order", block(1, 16) + block(2, 16) + block(3, 16), Progress{3, 16}, true},
		{"out of order is a count, not a maximum", block(5, 16) + block(2, 16), Progress{2, 16}, true},
		{"duplicates count once", block(2, 16) + "noise\n" + block(2, 16), Progress{1, 16}, true},
		{"partial last block with both fields", block(1, 4) + "[ Info: Done chunk\n│   chunk = 2\n│   chunks = 4\n", Progress{2, 4}, true},
		{"partial last block cut before chunks", block(1, 4) + "[ Info: Done chunk\n│   chunk = 2\n", Progress{1, 4}, true},
		{"only a cut block", "[ Info: Done chunk\n│   chunk = 2\n", Progress{}, false},
		{"garbage values", "[ Info: Done chunk\n│   chunk = x\n│   chunks = 9\n└   elapsed_s = 1\n[ Info: Done chunk\n│   chunk = -1\n│   chunks = 9\n", Progress{}, false},
		{"chunk beyond total is refused", block(9, 4), Progress{}, false},
		{"zero is not a chunk", block(0, 4), Progress{}, false},
		{"huge total refused", block(1, 99999999999), Progress{}, false},
		{"other info lines ignored", "[ Info: Done something\n│   chunk = 1\n│   chunks = 2\n", Progress{}, false},
		{"mixed totals: the last block's total wins", block(1, 8) + block(2, 8) + block(1, 3), Progress{1, 3}, true},
		{"windows line ends", strings.ReplaceAll(block(1, 2)+block(2, 2), "\n", "\r\n"), Progress{2, 2}, true},
		{"complete", block(1, 1), Progress{1, 1}, true},
	}
	for _, c := range cases {
		got, ok := Parse(c.log)
		if ok != c.ok || got != c.want {
			t.Errorf("%s: got %v %v, want %v %v", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestParseTailRefusesATruncatedFile(t *testing.T) {
	short := "=== ./a-1.out\nhello\n=== ./a-1.err\n" + block(1, 4) + block(2, 4)
	if p, ok := ParseTail(short); !ok || p != (Progress{2, 4}) {
		t.Fatalf("got %v %v", p, ok)
	}
	full := "=== ./a-1.err\n" + strings.Repeat("x", TailLimit-100) + block(3, 4)
	if p, ok := ParseTail(full); ok {
		t.Fatalf("a log cut at the tail limit must report no progress, got %v", p)
	}
	if _, ok := ParseTail(""); ok {
		t.Fatal("empty output has no progress")
	}
}

func TestETA(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if s, ok := ETA(Progress{4, 16}, start, start.Add(10*time.Minute)); !ok || s != 1800 {
		t.Fatalf("got %d %v", s, ok)
	}
	for _, p := range []Progress{{0, 16}, {16, 16}} {
		if _, ok := ETA(p, start, start.Add(time.Hour)); ok {
			t.Errorf("no estimate for %v", p)
		}
	}
	if _, ok := ETA(Progress{1, 2}, time.Time{}, start); ok {
		t.Error("no estimate without a start")
	}
}
