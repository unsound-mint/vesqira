// Package publish is the publish server (ADR 0030): an always-on, internet-facing store of report revisions the owner
// chose to publish, the named reader links that open them, and the comments readers leave. It is built from its own
// database and data directory and nothing else: no catalog, source roots, worker, hosts, jobs or credentials of the
// owner's instance can be reached from here (enforced in internal/arch). This file is the wire contract shared with
// the owner's instance, which builds revisions and pushes them.
package publish

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

// Block kinds. A text block is Markdown; a figure block is a frozen picture with its reviewer-safe provenance; a run
// block is the reviewer-safe summary of a run (the same facts a figure's provenance carries about its run).
const (
	BlockText   = "text"
	BlockFigure = "figure"
	BlockRun    = "run"
)

// Revision is one published, immutable state of a report. The owner's instance builds it; the publish server computes
// its ID from the content, so pushing the same revision twice stores it once.
//
// Text blocks are Markdown with raw HTML never rendered. Two reference forms survive freezing:
//   - [[report:<key>|<title>]] links another report; a reader whose space lacks that report sees "not shared with you".
//   - ![alt](asset:<sha256>) shows an uploaded asset.
//
// No path, host, dataset id, run directory, script or token may appear in a revision; the owner's instance never puts
// one there and tests on both sides check it.
type Revision struct {
	ID          string    `json:"id"`     // sha256 of the revision with ID empty; set by the server
	Report      string    `json:"report"` // the owner's opaque, stable report key (16 hex characters)
	Number      int       `json:"number"` // 1, 2, 3 ... per report, assigned by the owner
	Title       string    `json:"title"`
	PublishedAt time.Time `json:"publishedAt"`
	Authors     string    `json:"authors"`           // plain attribution, for example "Drafted by VGT agent, edited by Denis"
	Summary     string    `json:"summary,omitempty"` // one plain line shown in the reader's list; omitted when empty so older revisions keep their ID
	Date        string    `json:"date,omitempty"`    // the report's own date, YYYY-MM-DD; omitted when empty
	Blocks      []Block   `json:"blocks"`
}

type Block struct {
	ID     string          `json:"id"` // ContentID of the block: anchors carry over to a later revision when it is unchanged
	Kind   string          `json:"kind"`
	Text   string          `json:"text,omitempty"`   // text blocks
	Figure *Figure         `json:"figure,omitempty"` // figure blocks
	Run    json.RawMessage `json:"run,omitempty"`    // run blocks: a JSON object, the reviewer-safe run summary
}

type Figure struct {
	Asset      string          `json:"asset"` // sha256 of the uploaded PNG
	Width      int             `json:"width"`
	Height     int             `json:"height"`
	Title      string          `json:"title"`
	Caption    string          `json:"caption"`
	Revision   string          `json:"revision"`   // the owner's figure revision (ADR 0011), for reference only
	Provenance json.RawMessage `json:"provenance"` // the reviewer-safe provenance document, a JSON object
}

// ContentID identifies a block by its kind and content, so the same paragraph in a later revision keeps its comments.
func ContentID(kind, content string) string {
	s := sha256.Sum256([]byte(kind + "\x00" + content))
	return hex.EncodeToString(s[:12])
}

// ComputeID is the revision's identity: the sha256 of its JSON with ID cleared.
func ComputeID(r Revision) (string, error) {
	r.ID = ""
	b, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:]), nil
}

// Anchor is a place on a figure as drawn, in fractions of its width and height (ADR 0023). It is never a sky position.
type Anchor struct {
	Kind string  `json:"kind"` // point | box
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	X1   float64 `json:"x1,omitempty"`
	Y1   float64 `json:"y1,omitempty"`
}

// Comment is a reader's or the owner's comment on a block of a revision. Replies are one level deep and inherit the
// report, revision and block of their thread; only the owner resolves a thread.
type Comment struct {
	ID         int64      `json:"id"`
	Space      string     `json:"space,omitempty"` // publisher API only
	Report     string     `json:"report"`
	Revision   string     `json:"revision"`
	Block      string     `json:"block"`
	Anchor     *Anchor    `json:"anchor,omitempty"` // only on a figure block's first comment
	Quote      string     `json:"quote,omitempty"`  // the phrase of a text block a thread's first comment is about; untrusted plain text
	ParentID   int64      `json:"parentId"`
	Author     string     `json:"author"` // the reader link's name, or the owner's name
	FromOwner  bool       `json:"fromOwner"`
	Body       string     `json:"body"`
	CreatedAt  time.Time  `json:"createdAt"`
	ResolvedAt *time.Time `json:"resolvedAt,omitempty"`
	Mine       bool       `json:"mine,omitempty"` // reader view only: posted through the requesting link, so it may be deleted by it

	readerID int64 // the link it came through; never serialised, so readers cannot learn each other's link ids
}

// Deletion is a tombstone: comment ID was deleted, so the owner's mirror of the feed can drop it.
type Deletion struct {
	Seq int64 `json:"seq"`
	ID  int64 `json:"id"`
}

// Space is what the owner publishes to a group of readers: a title and the reports it contains.
type Space struct {
	Slug    string   `json:"slug"` // [a-z0-9-]{1,40}
	Title   string   `json:"title"`
	Reports []string `json:"reports"` // report keys
}

// Reader is a named capability link to one space. The token is returned once, when the link is made.
type Reader struct {
	ID        int64      `json:"id"`
	Space     string     `json:"space"`
	Name      string     `json:"name"`
	CreatedAt time.Time  `json:"createdAt"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	Revoked   bool       `json:"revoked"`
	Token     string     `json:"token,omitempty"` // only in the response that creates the link
}
