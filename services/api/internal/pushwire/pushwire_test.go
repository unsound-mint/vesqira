package pushwire

import (
	"encoding/json"
	"strings"
	"testing"
)

// The wire contract: the hash ignores nothing but itself, the origin key is opaque and per instance, and the scan refuses paths.
func TestPushwireHashOriginAndScan(t *testing.T) {
	it := Item{Kind: "run", Origin: strings.Repeat("a", 32), Title: "x", Doc: json.RawMessage(`{"name":"x"}`), Assets: []string{}, References: []Reference{}}
	a, _ := ContentHash(it)
	it.Hash = "anything"
	b, _ := ContentHash(it)
	it.Title = "y"
	c, _ := ContentHash(it)
	if a != b || a == c {
		t.Error("the hash must ignore its own field and nothing else")
	}
	k1 := OriginKey([]byte("secret-one"), "figure", 42)
	if k1 != OriginKey([]byte("secret-one"), "figure", 42) || k1 == OriginKey([]byte("secret-two"), "figure", 42) ||
		k1 == OriginKey([]byte("secret-one"), "board", 42) || strings.Contains(k1, "42") && len(k1) != 32 {
		t.Error("origin keys are stable per instance and object and differ otherwise")
	}
	for _, bad := range []string{`{"path":"x"}`, `{"a":{"sourceDir":"x"}}`, `{"s":"/home/me/data/x.fits"}`, `{"s":"see ~/data"}`, `{"s":"C:\\data"}`, `{"token":"x"}`, `{"datasetId":3}`} {
		if Scan(json.RawMessage(bad)) == nil {
			t.Errorf("the scan let %s through", bad)
		}
	}
	for _, ok := range []string{`{"unit":"km/s"}`, `{"bunit":"Jy/beam"}`, `{"key":"block_size","value":"32"}`, `{"statements":["a / b"]}`} {
		if err := Scan(json.RawMessage(ok)); err != nil {
			t.Errorf("the scan refused %s: %v", ok, err)
		}
	}
}
