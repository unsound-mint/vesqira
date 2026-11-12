// Package pushwire is the wire contract between a linked local instance and the group server (ADR 0066 section 2): the
// envelope of one pushed item, how its content hash and its opaque origin key are made, the limits, and the scan both sides run
// for anything that looks like a path, a machine, an account or a credential. It imports nothing of the program, so the client
// (internal/server in local mode) and the group server (internal/server with internal/pushed) cannot disagree about a hash.
package pushwire

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// The four kinds of pushed item, each a frozen document the local instance already builds for reviewers.
const (
	KindFigure = "figure" // the ADR 0011 frozen figure: picture asset, title, caption and reviewer-safe provenance
	KindRun    = "run"    // the ADR 0014 reviewer run line (runs.Summary)
	KindBoard  = "board"  // the ADR 0011 FrozenItem list with its layout
	KindReport = "report" // the ADR 0036 publish.Revision
)

// Kinds lists the kinds in the order a push uploads them (runs before the figures and boards that may show them).
func Kinds() []string { return []string{KindRun, KindFigure, KindBoard, KindReport} }

// Limits (ADR 0066 section 2). The asset and document sizes are the publish server's.
const (
	MaxAssetBytes  = 8 << 20
	MaxAssetPixels = 4096
	MaxDocBytes    = 4 << 20
	MaxBoardItems  = 200
	MaxHave        = 1000
	MaxReferences  = 64
	DailyItems     = 600     // per account, across workspaces, in any 24 hours
	DailyBytes     = 2 << 30 // per account: documents and new pictures
)

// Item is one pushed item. Its identity is Hash: the SHA-256 of its canonical JSON with Hash empty (ContentHash), the rule
// publish.ComputeID applies to a revision. The server recomputes it from what arrived and never takes the client's word.
type Item struct {
	Hash   string          `json:"hash"`
	Kind   string          `json:"kind"`
	Origin string          `json:"origin"` // OriginKey: opaque, stable per local object, reveals no local id
	Title  string          `json:"title"`
	Doc    json.RawMessage `json:"doc"`
	// Assets are the hashes of the pictures the document shows, sorted; each is uploaded first and checked on arrival.
	Assets []string `json:"assets"`
	// References are the datasets a figure or a run was made from, by what a reviewer may see: role, file name and size. What
	// identifies their bytes travels beside the item (Push.Identity), never inside it.
	References []Reference `json:"references"`
}

// Reference is a dataset a figure or a run was made from: never a path, a host or a local id.
type Reference struct {
	Role      string `json:"role"` // source | overlay | reliability | output
	Name      string `json:"name"` // the file name only
	SizeBytes int64  `json:"sizeBytes"`
}

// Identity is the push-only evidence about one reference's bytes (ADR 0053): the sampled fingerprint with its algorithm and,
// when it was computed, the full SHA-256. The server compares it with its own datasets and keeps it in pushed_references; it is
// never part of the document, a review snapshot or a publish-server revision.
type Identity struct {
	Algorithm string `json:"algorithm,omitempty"`
	Sampled   string `json:"sampled,omitempty"`
	Full      string `json:"fullSha256,omitempty"`
}

// Push is the body of POST /api/push/items: the instance id, the item, and Identity in the order of Item.References.
type Push struct {
	Instance string     `json:"instance"`
	Item     Item       `json:"item"`
	Identity []Identity `json:"identity"`
}

// Have is the body of POST /api/push/have and its answer: the hashes (items or assets) the server does not hold.
type Have struct {
	Instance string   `json:"instance"`
	Hashes   []string `json:"hashes"`
}

// ContentHash is the item's identity: the SHA-256 of its JSON with Hash empty.
func ContentHash(it Item) (string, error) {
	it.Hash = ""
	b, err := json.Marshal(it)
	if err != nil {
		return "", err
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:]), nil
}

// OriginKey is the opaque lineage key of a local object: the first 16 bytes, in hex, of HMAC-SHA256 under the instance's secret
// over "kind:local id". Stable across pushes of the same object, unrelated across instances, and no local id can be read from it.
func OriginKey(secret []byte, kind string, localID int64) string {
	m := hmac.New(sha256.New, secret)
	fmt.Fprintf(m, "%s:%d", kind, localID)
	return hex.EncodeToString(m.Sum(nil)[:16])
}

var (
	reSHA      = regexp.MustCompile(`^[0-9a-f]{64}$`)
	reOrigin   = regexp.MustCompile(`^[0-9a-f]{32}$`)
	reInstance = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// ValidSHA reports whether s is a lowercase hex SHA-256.
func ValidSHA(s string) bool { return reSHA.MatchString(s) }

// ValidInstance reports whether s is a 128-bit instance id in lowercase hex.
func ValidInstance(s string) bool { return reInstance.MatchString(s) }

// Check validates an item's envelope (not its document, which the server decodes by kind): known kind, the shapes of the origin
// and of every asset hash, sorted unique assets, references within bounds, identity beside them, and no machine-like field
// anywhere in the document.
func Check(p Push) error {
	it := p.Item
	if !ValidInstance(p.Instance) {
		return fmt.Errorf("the instance id must be 32 lowercase hex characters")
	}
	known := false
	for _, k := range Kinds() {
		known = known || it.Kind == k
	}
	if !known {
		return fmt.Errorf("unknown kind %q", it.Kind)
	}
	if !reOrigin.MatchString(it.Origin) {
		return fmt.Errorf("the origin key must be 32 lowercase hex characters")
	}
	if n := len([]rune(it.Title)); n < 1 || n > 300 {
		return fmt.Errorf("the title must be 1 to 300 characters")
	}
	if len(it.Doc) == 0 || len(it.Doc) > MaxDocBytes {
		return fmt.Errorf("a document is at most %d MiB", MaxDocBytes>>20)
	}
	if !sort.StringsAreSorted(it.Assets) {
		return fmt.Errorf("assets must be sorted")
	}
	for i, a := range it.Assets {
		if !ValidSHA(a) || (i > 0 && it.Assets[i-1] == a) {
			return fmt.Errorf("asset %d is not a distinct lowercase sha256", i)
		}
	}
	if len(it.References) > MaxReferences || len(p.Identity) != len(it.References) {
		return fmt.Errorf("at most %d references, each with its identity entry", MaxReferences)
	}
	for i, r := range it.References {
		switch r.Role {
		case "source", "overlay", "reliability", "output":
		default:
			return fmt.Errorf("reference %d: unknown role", i)
		}
		if r.Name == "" || len(r.Name) > 255 || strings.ContainsAny(r.Name, "/\\\x00") || r.SizeBytes < 0 {
			return fmt.Errorf("reference %d: a file name (no folder) and a size", i)
		}
		id := p.Identity[i]
		if (id.Sampled != "" && (!ValidSHA(id.Sampled) || id.Algorithm == "")) || (id.Full != "" && !ValidSHA(id.Full)) || len(id.Algorithm) > 40 {
			return fmt.Errorf("reference %d: fingerprints are lowercase sha256 values with their algorithm", i)
		}
	}
	return Scan(it.Doc)
}

// bannedKeys are JSON keys no pushed document may carry (ADR 0066 "what a push never carries", the ADR 0036 reflection list):
// matched on the lower-cased key with separators removed.
var bannedKeys = map[string]bool{
	"path": true, "paths": true, "dir": true, "directory": true, "folder": true, "workdir": true, "rundir": true, "jobdir": true,
	"host": true, "hostid": true, "hostname": true, "hosts": true, "sshalias": true, "alias": true, "destination": true,
	"username": true, "user": true, "login": true, "partition": true, "account": true, "slurmid": true, "jobid": true,
	"script": true, "config": true, "request": true, "token": true, "tokenhash": true, "secret": true,
	"password": true, "credential": true, "credentials": true, "datasetid": true, "datasetids": true, "figureid": true,
	"boardid": true, "runid": true, "reportid": true, "jobidlocal": true, "scene": true, "identity_record": true,
	"sweeplabel": true, "label_owner": true, "inputfile": true, "outputdir": true,
}

// suffixBanned are key endings that name a location whatever comes before (sourcePath, outputDir, remoteHost).
var suffixBanned = []string{"path", "dir", "host", "folder", "token", "secret", "password"}

// absPath matches a value that is an absolute or home-relative path (two segments or more), a Windows drive path or a file URL.
var absPath = regexp.MustCompile(`(^|[\s"'(=])(/[^/\s]+/|~/|[A-Za-z]:\\|file://)`)

// Scan walks a JSON document and refuses a banned key or a value that looks like a path. It is the server's filter and the
// client's last check before sending; neither replaces the freezing code, which never puts such a field in.
func Scan(doc json.RawMessage) error {
	var v any
	if err := json.Unmarshal(doc, &v); err != nil {
		return fmt.Errorf("the document is not JSON")
	}
	return scan(v, "")
}

func scan(v any, at string) error {
	switch x := v.(type) {
	case map[string]any:
		for k, vv := range x {
			norm := strings.NewReplacer("_", "", "-", "", " ", "").Replace(strings.ToLower(k))
			if bannedKeys[norm] {
				return fmt.Errorf("%s%s: a pushed document never carries this field", at, k)
			}
			for _, suf := range suffixBanned {
				if strings.HasSuffix(norm, suf) {
					return fmt.Errorf("%s%s: a pushed document never carries a location or a credential", at, k)
				}
			}
			if err := scan(vv, at+k+"."); err != nil {
				return err
			}
		}
	case []any:
		for _, vv := range x {
			if err := scan(vv, at); err != nil {
				return err
			}
		}
	case string:
		if absPath.MatchString(x) {
			return fmt.Errorf("%s: a value looks like a path on a computer", strings.TrimSuffix(at, "."))
		}
	}
	return nil
}
