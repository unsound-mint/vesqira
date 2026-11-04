package boards

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

func TestEmbeddedTextKeepsThePicturePixelForPixel(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 5, 4))
	for x := 0; x < 5; x++ {
		for y := 0; y < 4; y++ {
			img.Set(x, y, color.RGBA{uint8(x * 50), uint8(y * 60), 7, 255})
		}
	}
	var in bytes.Buffer
	png.Encode(&in, img)
	out, err := EmbedText(in.Bytes(), [][2]string{{"Title", "Peak channel"}, {"Provenance", `{"source":{"name":"cube.fits"}}`}})
	if err != nil {
		t.Fatal(err)
	}
	// still a valid PNG with the same pixels (the decoder verifies every chunk's checksum)
	dec, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("the result must still decode: %v", err)
	}
	for x := 0; x < 5; x++ {
		for y := 0; y < 4; y++ {
			r1, g1, b1, a1 := img.At(x, y).RGBA()
			r2, g2, b2, a2 := dec.At(x, y).RGBA()
			if r1 != r2 || g1 != g2 || b1 != b2 || a1 != a2 {
				t.Fatalf("pixel %d,%d changed", x, y)
			}
		}
	}
	if !strings.Contains(string(out), "iTXt") || !strings.Contains(string(out), "Peak channel") || !strings.Contains(string(out), "cube.fits") {
		t.Error("the text is not in the file")
	}
	if _, err := EmbedText([]byte("not a png at all, really not one"), nil); err == nil {
		t.Error("garbage must be refused")
	}
}
