package imgmeta

import (
	"bytes"
	"image"
	"image/png"
	"testing"
)

// IsClean agrees with Clean on whether anything would be removed, from the structure alone.
func TestIsCleanAgreesWithClean(t *testing.T) {
	var b bytes.Buffer
	_ = png.Encode(&b, image.NewGray(image.Rect(0, 0, 4, 4)))
	clean := b.Bytes()
	if ok, err := IsClean(clean); !ok || err != nil {
		t.Fatalf("a plain PNG: %v %v", ok, err)
	}
	dirty := append(append([]byte{}, clean...), []byte("trailing data")...)
	if ok, err := IsClean(dirty); ok || err != nil {
		t.Fatalf("a PNG with trailing data: %v %v", ok, err)
	}
	if out, _, _ := Clean(dirty); !bytes.Equal(out, clean) {
		t.Fatal("Clean disagrees")
	}
	if _, err := IsClean([]byte("not a picture")); err == nil {
		t.Fatal("garbage accepted")
	}
}
