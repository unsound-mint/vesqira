package scene

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

type fixtures struct {
	Valid []struct {
		Name  string          `json:"name"`
		Scene json.RawMessage `json:"scene"`
	} `json:"valid"`
	Invalid []struct {
		Name  string          `json:"name"`
		Scene json.RawMessage `json:"scene"`
		Raw   string          `json:"raw"`
	} `json:"invalid"`
}

// packages/contracts/fixtures/scenes.json is shared with the web client, which round-trips the valid scenes; the server
// must accept every valid one unchanged and refuse every invalid one.
func load(t *testing.T) fixtures {
	t.Helper()
	raw, err := os.ReadFile("../../../../packages/contracts/fixtures/scenes.json")
	if err != nil {
		t.Fatal(err)
	}
	var f fixtures
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Valid) == 0 || len(f.Invalid) == 0 {
		t.Fatal("no fixtures")
	}
	return f
}

func canonical(t *testing.T, b []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestValidScenesAreAcceptedAndReMarshalledUnchanged(t *testing.T) {
	for _, c := range load(t).Valid {
		s, err := Parse(c.Scene)
		if err != nil {
			t.Errorf("%s: refused: %v", c.Name, err)
			continue
		}
		out, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(canonical(t, out), canonical(t, c.Scene)) {
			t.Errorf("%s: the stored form differs\n got %s\nwant %s", c.Name, out, c.Scene)
		}
	}
}

func TestInvalidScenesAreRefused(t *testing.T) {
	for _, c := range load(t).Invalid {
		raw := []byte(c.Raw)
		if c.Raw == "" {
			raw = c.Scene
		}
		if _, err := Parse(raw); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: accepted (err %v)", c.Name, err)
		}
	}
}

func TestListsAndSizeAreBounded(t *testing.T) {
	base := `{"schema":"scene/1","base":{"datasetId":1,"channel":0,"display":{"mapping":"scalar","colormap":"gray","stretch":"linear","range":"minmax"}},"overlays":[%s],"view":{"zoom":1}}`
	var ov []string
	for i := 0; i <= MaxOverlays; i++ {
		ov = append(ov, fmt.Sprintf(`{"id":"r%d","kind":"regions","datasetId":1,"visible":true,"saved":[]}`, i))
	}
	if _, err := Parse([]byte(fmt.Sprintf(base, strings.Join(ov[:MaxOverlays], ",")))); err != nil {
		t.Fatalf("%d overlays should be accepted: %v", MaxOverlays, err)
	}
	if _, err := Parse([]byte(fmt.Sprintf(base, strings.Join(ov, ",")))); !errors.Is(err, ErrInvalid) {
		t.Errorf("%d overlays accepted", MaxOverlays+1)
	}
	var saved []string
	for i := 1; i <= MaxSaved+1; i++ {
		saved = append(saved, fmt.Sprintf(`{"id":%d,"shape":{"shape":"circle","cx":1,"cy":1,"r":1}}`, i))
	}
	if _, err := Parse([]byte(fmt.Sprintf(base, `{"id":"r","kind":"regions","datasetId":1,"visible":true,"saved":[`+strings.Join(saved, ",")+`]}`))); !errors.Is(err, ErrInvalid) {
		t.Errorf("%d saved regions accepted", MaxSaved+1)
	}
	big := fmt.Sprintf(base, "") + strings.Repeat(" ", MaxBytes)
	if _, err := Parse([]byte(big)); !errors.Is(err, ErrInvalid) {
		t.Error("an oversized body was read")
	}
}
