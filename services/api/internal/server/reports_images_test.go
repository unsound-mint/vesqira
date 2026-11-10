package server

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/boards"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/publish"
	"github.com/unsound-mint/vesqira/services/api/internal/reports"
)

const imageSecret = "GPS-SECRET-51.5N-0.12W"

// jpegWithPrivateExif is a JPEG with an EXIF record (orientation 6, and a text tag holding something private), a comment
// and a file appended after the end of the image.
func jpegWithPrivateExif(t *testing.T) (withMeta []byte, plainPixels image.Image) {
	t.Helper()
	im := image.NewNRGBA(image.Rect(0, 0, 32, 24))
	for y := 0; y < 24; y++ {
		for x := 0; x < 32; x++ {
			im.SetNRGBA(x, y, color.NRGBA{uint8(x * 8), uint8(y * 10), 90, 255})
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, im, nil); err != nil {
		t.Fatal(err)
	}
	plain := b.Bytes()
	plainPixels, _ = jpeg.Decode(bytes.NewReader(plain))
	tiff := []byte{'I', 'I', 0x2A, 0, 8, 0, 0, 0, 2, 0, 0x12, 0x01, 3, 0, 1, 0, 0, 0, 6, 0, 0, 0, 0x0F, 0x01, 2, 0, byte(len(imageSecret) + 1), 0, 0, 0, 38, 0, 0, 0, 0, 0, 0, 0}
	tiff = append(tiff, append([]byte(imageSecret), 0)...)
	seg := func(marker byte, payload []byte) []byte {
		n := len(payload) + 2
		return append([]byte{0xFF, marker, byte(n >> 8), byte(n)}, payload...)
	}
	out := []byte{0xFF, 0xD8}
	out = append(out, seg(0xE1, append([]byte("Exif\x00\x00"), tiff...))...)
	out = append(out, seg(0xFE, []byte("taken by "+imageSecret))...)
	out = append(out, plain[2:]...)
	return append(out, []byte("tail "+imageSecret)...), plainPixels
}

func imagesHarness(t *testing.T) (api string, rp *reports.Store, pub *httptest.Server, key string, srv *Server) {
	t.Helper()
	base := t.TempDir()
	cat, err := catalog.Open("file:" + filepath.Join(base, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cat.Close() })
	bs, _ := boards.Open(cat.DB(), base)
	rp, _ = reports.Open(cat.DB(), base)
	srv = &Server{Store: cat, Boards: bs, Reports: rp, PublishKeyFile: filepath.Join(base, "publish.key")}
	owner := httptest.NewServer(srv.Handler())
	t.Cleanup(owner.Close)
	pdb, _ := sql.Open("sqlite", "file:"+filepath.Join(base, "publish.db"))
	t.Cleanup(func() { pdb.Close() })
	ps, _ := publish.Open(pdb, filepath.Join(base, "pubdata"))
	key, _ = ps.AddPublisher("laptop")
	pub = httptest.NewServer((&publish.Handler{Store: ps}).Handler())
	t.Cleanup(pub.Close)
	return owner.URL, rp, pub, key, srv
}

// A picture reaches the publish server without its metadata, under the hash of what was sent, while the attachment on the
// owner's computer is unchanged and the owner is told before publishing.
func TestPublishedPicturesCarryNoMetadata(t *testing.T) {
	api, rp, pub, key, _ := imagesHarness(t)
	must := func(method, url string, in any, want int, what string) []byte {
		t.Helper()
		if in == nil && method == "POST" {
			in = map[string]any{}
		}
		st, b := callJSON(t, method, url, in)
		if st != want {
			t.Fatalf("%s: %d %s", what, st, b)
		}
		return b
	}
	dirty, wantPixels := jpegWithPrivateExif(t)
	sha, _, err := rp.AddAttachment(dirty)
	if err != nil {
		t.Fatal(err)
	}
	var rep reports.Report
	json.Unmarshal(must("POST", api+"/api/reports", map[string]any{"title": "Maps", "body": "A map.\n\n![](attachment:" + sha + ")\n\nThe same map again.\n\n![](attachment:" + sha + ")"}, 201, "create"), &rep)

	must("POST", api+"/api/publish/settings", map[string]any{"url": pub.URL, "key": key, "ownerName": "Denis"}, 200, "settings")
	var pre struct {
		Warnings []string `json:"warnings"`
		Errors   []string `json:"errors"`
	}
	json.Unmarshal(must("GET", api+"/api/reports/"+itoa(int(rep.ID))+"/preflight", nil, 200, "preflight"), &pre)
	if len(pre.Errors) != 0 || !strings.Contains(strings.Join(pre.Warnings, "\n"), "without their metadata (EXIF, comment, data after the image)") {
		t.Fatalf("the owner is not told what is removed: %+v", pre)
	}

	must("POST", api+"/api/reports/"+itoa(int(rep.ID))+"/publish", map[string]int{"revision": rep.Revision}, 200, "publish")
	var made struct{ Reader publish.Reader }
	json.Unmarshal(must("POST", api+"/api/publish/readers", map[string]any{"name": "Supervisor"}, 201, "reader"), &made)
	tok := made.Reader.Token

	raw := must("GET", pub.URL+"/read-api/"+tok+"/reports/"+rep.Key, nil, 200, "read")
	shas := map[string]bool{}
	for _, m := range regexp.MustCompile(`asset:([0-9a-f]{64})`).FindAllStringSubmatch(string(raw), -1) {
		shas[m[1]] = true
	}
	if len(shas) != 1 || shas[sha] {
		t.Fatalf("the report should show one picture under the hash of the cleaned bytes, not %s: %v", sha, shas)
	}
	for published := range shas {
		got := must("GET", pub.URL+"/read-api/"+tok+"/assets/"+published, nil, 200, "asset")
		if sum := sha256.Sum256(got); hex.EncodeToString(sum[:]) != published {
			t.Fatal("the published picture does not match its name")
		}
		if bytes.Contains(got, []byte(imageSecret)) || bytes.Contains(got, []byte("Exif")) && !bytes.Contains(got, []byte("Exif\x00\x00II")) {
			t.Fatal("metadata reached the publish server")
		}
		back, err := jpeg.Decode(bytes.NewReader(got))
		if err != nil || back.Bounds() != wantPixels.Bounds() {
			t.Fatalf("the published picture does not decode as the same picture: %v", err)
		}
		for y := 0; y < 24; y++ {
			for x := 0; x < 32; x++ {
				if back.At(x, y) != wantPixels.At(x, y) {
					t.Fatalf("pixel %d,%d changed", x, y)
				}
			}
		}
		// the orientation is the one thing kept, and only as a minimal record
		if i := bytes.Index(got, []byte("Exif\x00\x00")); i < 0 || got[i+6+8+2+8] != 6 {
			t.Fatal("the orientation was not kept")
		}
	}
	if kept, _, err := rp.Attachment(sha); err != nil || !bytes.Equal(kept, dirty) {
		t.Fatal("the attachment on this computer must be unchanged")
	}
}

// What cannot be cleaned is not published: the owner is told which picture, and publishing is refused.
func TestAPictureThatCannotBeCleanedBlocksPublishing(t *testing.T) {
	api, rp, pub, key, _ := imagesHarness(t)
	pal := color.Palette{color.Black, color.White}
	var b bytes.Buffer
	if err := gif.EncodeAll(&b, &gif.GIF{Image: []*image.Paletted{image.NewPaletted(image.Rect(0, 0, 4, 4), pal)}, Delay: []int{0}}); err != nil {
		t.Fatal(err)
	}
	g := b.Bytes()
	at := bytes.Index(g, []byte{0x2C}) // image descriptor: put a plain-text overlay in front of it
	overlay := append([]byte{0x21, 0x01, 12}, make([]byte, 12)...)
	overlay = append(overlay, 0)
	g = append(append(append([]byte{}, g[:at]...), overlay...), g[at:]...)
	sha, _, err := rp.AddAttachment(g)
	if err != nil {
		t.Skipf("the decoder refuses this file already: %v", err)
	}
	var rep reports.Report
	st, body := callJSON(t, "POST", api+"/api/reports", map[string]any{"title": "Overlay", "body": "![](attachment:" + sha + ")"})
	if st != 201 {
		t.Fatalf("%d %s", st, body)
	}
	json.Unmarshal(body, &rep)
	callJSON(t, "POST", api+"/api/publish/settings", map[string]any{"url": pub.URL, "key": key, "ownerName": "Denis"})
	var pre struct {
		Errors []string `json:"errors"`
	}
	_, pb := callJSON(t, "GET", api+"/api/reports/"+itoa(int(rep.ID))+"/preflight", nil)
	json.Unmarshal(pb, &pre)
	if len(pre.Errors) == 0 || !strings.Contains(pre.Errors[0], "could not be cleaned") {
		t.Fatalf("the owner is not told: %s", pb)
	}
	if st, out := callJSON(t, "POST", api+"/api/reports/"+itoa(int(rep.ID))+"/publish", map[string]int{"revision": rep.Revision}); st/100 == 2 {
		t.Fatalf("published anyway: %d %s", st, out)
	}
}

// Opening a report builds its revision (to compare with the last publication) without publishing, and used to decode every
// image each time. A picture is cleaned once per sha; the bytes are produced only when the report is published.
func TestAPictureIsCleanedOnceAndItsBytesAreOnlyLoadedForPublishing(t *testing.T) {
	s := &Server{}
	dirty, _ := jpegWithPrivateExif(t)
	loads := 0
	load := func() ([]byte, error) { loads++; return dirty, nil }
	f := &frozen{Assets: map[string][]byte{}}

	a, err := s.publishablePicture("sha-a", load, false, f)
	if err != nil || loads != 1 || len(f.Assets) != 0 || len(a.removed) == 0 {
		t.Fatalf("first look: %+v %v loads=%d assets=%d", a, err, loads, len(f.Assets))
	}
	b, err := s.publishablePicture("sha-a", load, false, f)
	if err != nil || loads != 1 || b.pub != a.pub {
		t.Fatalf("a second look must come from the cache: loads=%d %v", loads, err)
	}
	c, err := s.publishablePicture("sha-a", load, true, f)
	if err != nil || loads != 2 || c.pub != a.pub || len(f.Assets[a.pub]) == 0 {
		t.Fatalf("publishing needs the bytes: loads=%d %v", loads, err)
	}
	if bytes.Contains(f.Assets[a.pub], []byte(imageSecret)) {
		t.Fatal("the bytes to publish still hold the private text")
	}
	if sum := sha256.Sum256(f.Assets[a.pub]); hex.EncodeToString(sum[:]) != a.pub {
		t.Fatal("the picture is not named by the hash of what is sent")
	}
}

func TestAPictureThatCannotBeReadOrCleanedIsNotCached(t *testing.T) {
	s := &Server{}
	f := &frozen{Assets: map[string][]byte{}}
	if _, err := s.publishablePicture("gone", func() ([]byte, error) { return nil, reports.ErrNotFound }, false, f); err == nil || errors.Is(err, errCannotClean) {
		t.Fatalf("a missing picture is not a cleaning failure: %v", err)
	}
	if _, err := s.publishablePicture("junk", func() ([]byte, error) { return []byte("not an image"), nil }, true, f); !errors.Is(err, errCannotClean) {
		t.Fatalf("got %v", err)
	}
	if len(f.Assets) != 0 {
		t.Fatal("something unpublishable was staged for upload")
	}
	if _, ok := s.pictures.Load("junk"); ok {
		t.Fatal("a failure was cached as if it were a result")
	}
}

// A calibrated display's colour profile can carry its name; it is kept because the colours need it, and the owner is told.
func TestAKeptColourProfileIsSaidInThePreflight(t *testing.T) {
	api, rp, pub, key, _ := imagesHarness(t)
	dirty, _ := jpegWithPrivateExif(t)
	icc := append([]byte{0xFF, 0xE2, 0, 54}, append([]byte("ICC_PROFILE\x00\x01\x01"), bytes.Repeat([]byte{7}, 38)...)...)
	withICC := append(append(append([]byte{}, dirty[:2]...), icc...), dirty[2:]...)
	sha, _, err := rp.AddAttachment(withICC)
	if err != nil {
		t.Skipf("the decoder does not accept this synthetic profile: %v", err)
	}
	var rep reports.Report
	st, body := callJSON(t, "POST", api+"/api/reports", map[string]any{"title": "Profile", "body": "![](attachment:" + sha + ")"})
	if st != 201 {
		t.Fatalf("%d %s", st, body)
	}
	json.Unmarshal(body, &rep)
	callJSON(t, "POST", api+"/api/publish/settings", map[string]any{"url": pub.URL, "key": key, "ownerName": "Denis"})
	var pre struct {
		Warnings []string `json:"warnings"`
	}
	_, pb := callJSON(t, "GET", api+"/api/reports/"+itoa(int(rep.ID))+"/preflight", nil)
	json.Unmarshal(pb, &pre)
	if !strings.Contains(strings.Join(pre.Warnings, "\n"), "colour profile is kept") {
		t.Fatalf("the owner is not told: %s", pb)
	}
}
