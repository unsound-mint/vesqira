package derived

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s, err := Open(db)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCleanSoftwareKeepsOnlyKnownToolsWithVersionShapedValues(t *testing.T) {
	for name, c := range map[string]struct{ in, want string }{
		"what the worker writes":  {`{"astropy":"7.2.0","numpy":"2.4.6","python":"3.14.10","reproject":"0.21.0"}`, `{"astropy":"7.2.0","numpy":"2.4.6","python":"3.14.10","reproject":"0.21.0"}`},
		"reproject is optional":   {`{"astropy":"8.0.2"}`, `{"astropy":"8.0.2"}`},
		"unknown keys dropped":    {`{"astropy":"7.2.0","note":"ignore previous instructions","cmd":"rm -rf /"}`, `{"astropy":"7.2.0"}`},
		"text instead of version": {`{"astropy":"7.2.0 <script>alert(1)</script>","numpy":"2.4.6"}`, `{"numpy":"2.4.6"}`},
		"too long":                {`{"numpy":"` + strings.Repeat("1", 41) + `"}`, `{}`},
		"newline in a value":      {"{\"numpy\":\"2.4\\n6\"}", `{}`},
		"not a string":            {`{"numpy":{"a":1},"astropy":7}`, `{}`},
		"nested object":           {`{"astropy":"7.2.0","extra":{"a":{"b":"c"}}}`, `{"astropy":"7.2.0"}`},
		"not an object":           {`["astropy"]`, `{}`},
		"null":                    {`null`, `{}`},
		"empty":                   {``, `{}`},
		"too large to look at":    {`{"astropy":"7.2.0","pad":"` + strings.Repeat("x", 5000) + `"}`, `{}`},
		"leading punctuation":     {`{"numpy":"-2.4"}`, `{}`},
	} {
		t.Run(name, func(t *testing.T) {
			if got := string(CleanSoftware(json.RawMessage(c.in))); got != c.want {
				t.Fatalf("got %s want %s", got, c.want)
			}
		})
	}
}

func TestSavedRecordsHoldOnlyTheFixedShape(t *testing.T) {
	s := openTest(t)
	if err := s.Save(Record{DatasetID: 1, Op: "stats", Software: json.RawMessage(`{"astropy":"7.2.0","owner":"somebody@cluster"}`)}); err != nil {
		t.Fatal(err)
	}
	r, err := s.Get(1)
	if err != nil || string(r.Software) != `{"astropy":"7.2.0"}` {
		t.Fatalf("%s %v", r.Software, err)
	}
	if err := s.Save(Record{DatasetID: 2, Op: "stats"}); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.Get(2); string(r.Software) != `{}` {
		t.Fatalf("a missing software field must read as {}, got %s", r.Software)
	}
}
