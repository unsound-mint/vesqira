package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/png"
	"testing"
)

func pngOf(t *testing.T, img image.Image) ([]byte, string) {
	t.Helper()
	var b bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b.Bytes())
	return b.Bytes(), hex.EncodeToString(sum[:])
}

// A small file that decodes to a huge picture never reaches the decoder: the decoded size is bounded from the header (width x
// height x bytes per pixel), a picture the server holds already is answered without decoding, a picture with metadata is
// refused by its structure alone, at most two pictures decode at once (503 beyond), and refused uploads count against the token.
func TestPushedPicturesCannotBeDecodeBombs(t *testing.T) {
	g, _, _ := pushGroup(t)
	full, _ := g.setupAdmin("ada")
	token := g.mintPush(full)

	// 3000 x 3000 at 16 bits per channel is 72 MB decoded from a few hundred KB of zeros
	bomb, bombSHA := pngOf(t, image.NewRGBA64(image.Rect(0, 0, 3000, 3000)))
	before := pictureDecodes.Load()
	if w := putAsset(g, token, bombSHA, bomb); w.Code != 413 {
		t.Fatalf("a picture that decodes past the bound: %d %s", w.Code, w.Body)
	}
	if pictureDecodes.Load() != before {
		t.Fatal("the oversized picture was decoded")
	}

	pic, sha := cleanPNG(t, 16, 12)
	if w := putAsset(g, token, sha, pic); w.Code != 201 {
		t.Fatalf("a small picture: %d %s", w.Code, w.Body)
	}
	before = pictureDecodes.Load()
	if w := putAsset(g, token, sha, pic); w.Code != 200 || pictureDecodes.Load() != before {
		t.Fatalf("a picture the server holds was decoded again: %d", w.Code)
	}
	meta := pngWithText(t, "someone")
	ms := sha256.Sum256(meta)
	if w := putAsset(g, token, hex.EncodeToString(ms[:]), meta); w.Code != 422 || pictureDecodes.Load() != before {
		t.Fatalf("a picture with metadata was decoded before it was refused: %d", w.Code)
	}

	// both decode slots taken: the next picture waits for no one
	slots := g.s.decodeSlots()
	slots <- struct{}{}
	slots <- struct{}{}
	other, otherSHA := cleanPNG(t, 20, 20)
	w := putAsset(g, token, otherSHA, other)
	<-slots
	<-slots
	if w.Code != 503 || w.Header().Get("Retry-After") == "" {
		t.Fatalf("with every decode slot taken: %d %s", w.Code, w.Body)
	}

	// refused uploads count: past the limit even a good picture is answered 429 without being decoded
	for i := 0; i < pushRefusalLimit; i++ {
		putAsset(g, token, hex.EncodeToString(ms[:]), meta)
	}
	before = pictureDecodes.Load()
	if w := putAsset(g, token, otherSHA, other); w.Code != 429 || pictureDecodes.Load() != before {
		t.Fatalf("after %d refused uploads: %d", pushRefusalLimit, w.Code)
	}
}
