package imgmeta

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"slices"
	"testing"
)

const secret = "GPS-SECRET-51.5N-0.12W"

func gradient(w, h int) *image.NRGBA {
	im := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			im.SetNRGBA(x, y, color.NRGBA{uint8(x * 255 / w), uint8(y * 255 / h), uint8((x + y) * 7), 255})
		}
	}
	return im
}

// exifWith is a little-endian EXIF TIFF structure holding an Orientation and a text tag with something private in it.
func exifWith(orientation int, text string) []byte {
	b := []byte{'I', 'I', 0x2A, 0, 8, 0, 0, 0, 2, 0}
	b = append(b, 0x12, 0x01, 3, 0, 1, 0, 0, 0, byte(orientation), 0, 0, 0)
	textOff := 8 + 2 + 2*12 + 4
	b = append(b, 0x0F, 0x01, 2, 0, byte(len(text)+1), 0, 0, 0, byte(textOff), 0, 0, 0) // Make (ASCII), at textOff
	b = append(b, 0, 0, 0, 0)
	return append(b, append([]byte(text), 0)...)
}

func pngChunk(typ string, data []byte) []byte {
	c := binary.BigEndian.AppendUint32(nil, uint32(len(data)))
	c = append(c, typ...)
	c = append(c, data...)
	return binary.BigEndian.AppendUint32(c, crc32.ChecksumIEEE(c[4:]))
}

func encodePNG(t testing.TB) []byte {
	var b bytes.Buffer
	if err := png.Encode(&b, gradient(24, 16)); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// withPNGChunks inserts chunks right after IHDR and appends trailing bytes after IEND.
func withPNGChunks(png []byte, trailing []byte, chunks ...[]byte) []byte {
	ihdrEnd := 8 + 12 + 13
	out := append([]byte{}, png[:ihdrEnd]...)
	for _, c := range chunks {
		out = append(out, c...)
	}
	out = append(out, png[ihdrEnd:]...)
	return append(out, trailing...)
}

func decodePNG(t *testing.T, b []byte) image.Image {
	im, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return im
}

func TestPNGLosesTextTimeExifAndTrailingDataButKeepsThePicture(t *testing.T) {
	plain := encodePNG(t)
	dirty := withPNGChunks(plain, []byte("PK\x03\x04 a zip hidden after IEND "+secret),
		pngChunk("tEXt", []byte("Author\x00"+secret)),
		pngChunk("iTXt", []byte("XML:com.adobe.xmp\x00\x00\x00\x00\x00<x:xmpmeta>"+secret)),
		pngChunk("tIME", []byte{0x07, 0xE9, 11, 10, 1, 2, 3}),
		pngChunk("eXIf", exifWith(6, secret)),
		pngChunk("zzZz", []byte(secret)), // unknown ancillary
	)
	out, removed, err := Clean(dirty)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte(secret)) || bytes.Contains(out, []byte("PK\x03\x04")) {
		t.Fatal("private text survived")
	}
	for _, name := range []string{"EXIF", "text", "modification time", "other chunk", "data after the image"} {
		if !contains(removed, name) {
			t.Errorf("did not report %q: %v", name, removed)
		}
	}
	assertAllowed(t, out)
	if !samePixels(decodePNG(t, plain), decodePNG(t, out)) {
		t.Fatal("pixels changed")
	}
	// the orientation survives as the only EXIF content
	i := bytes.Index(out, []byte("eXIf"))
	if i < 0 || exifOrientation(out[i+4:i+4+26]) != 6 {
		t.Fatal("orientation was not kept")
	}
	if len(out) > len(plain)+12+26 {
		t.Fatalf("more than a minimal eXIf was added: %d vs %d", len(out), len(plain))
	}
}

func TestPNGWithNothingToRemoveKeepsItsBytes(t *testing.T) {
	plain := encodePNG(t)
	out, removed, err := Clean(plain)
	if err != nil || len(removed) != 0 || !bytes.Equal(out, plain) {
		t.Fatalf("%v %v", removed, err)
	}
}

func TestPNGNormalOrientationNeedsNoRecord(t *testing.T) {
	dirty := withPNGChunks(encodePNG(t), nil, pngChunk("eXIf", exifWith(1, secret)))
	out, _, err := Clean(dirty)
	if err != nil || bytes.Contains(out, []byte("eXIf")) || bytes.Contains(out, []byte(secret)) {
		t.Fatalf("%v", err)
	}
}

func encodeJPEG(t testing.TB) []byte {
	var b bytes.Buffer
	if err := jpeg.Encode(&b, gradient(32, 24), &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func jpegSeg(marker byte, payload []byte) []byte {
	n := len(payload) + 2
	return append([]byte{0xFF, marker, byte(n >> 8), byte(n)}, payload...)
}

func withJPEGSegments(j []byte, trailing []byte, segs ...[]byte) []byte {
	out := []byte{0xFF, 0xD8}
	for _, s := range segs {
		out = append(out, s...)
	}
	out = append(out, j[2:]...)
	return append(out, trailing...)
}

func TestJPEGLosesExifXmpIptcCommentsThumbnailsAndTrailingDataButKeepsThePictureAndOrientation(t *testing.T) {
	plain := encodeJPEG(t)
	dirty := withJPEGSegments(plain, []byte("PK\x03\x04 hidden "+secret),
		jpegSeg(0xE0, []byte("JFIF\x00\x01\x01\x00\x00\x01\x00\x01\x00\x00")),
		jpegSeg(0xE0, append([]byte("JFXX\x00\x10"), []byte("thumbnail "+secret)...)),
		jpegSeg(0xE1, append([]byte("Exif\x00\x00"), exifWith(3, secret)...)),
		jpegSeg(0xE1, append([]byte("http://ns.adobe.com/xap/1.0/\x00"), []byte("<x:xmpmeta>"+secret)...)),
		jpegSeg(0xED, []byte("Photoshop 3.0\x00"+secret)),
		jpegSeg(0xFE, []byte("shot by "+secret)),
		jpegSeg(0xE5, []byte("vendor "+secret)),
	)
	out, removed, err := Clean(dirty)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte(secret)) || bytes.Contains(out, []byte("PK\x03\x04")) {
		t.Fatal("private text survived")
	}
	for _, name := range []string{"EXIF", "XMP", "IPTC", "comment", "embedded thumbnail", "application data", "data after the image"} {
		if !contains(removed, name) {
			t.Errorf("did not report %q: %v", name, removed)
		}
	}
	assertAllowed(t, out)
	a, _ := jpeg.Decode(bytes.NewReader(plain))
	b, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil || !samePixels(a, b) {
		t.Fatalf("picture changed: %v", err)
	}
	i := bytes.Index(out, []byte("Exif\x00\x00"))
	if i < 0 || exifOrientation(out[i+6:]) != 3 {
		t.Fatal("orientation was not kept")
	}
	if bytes.Count(out, []byte("Exif\x00\x00")) != 1 || !bytes.HasPrefix(out, []byte{0xFF, 0xD8, 0xFF, 0xE0}) {
		t.Fatal("expected JFIF first and a single EXIF record")
	}
}

func TestJPEGWithNothingToRemoveKeepsItsBytes(t *testing.T) {
	plain := encodeJPEG(t)
	out, removed, err := Clean(plain)
	if err != nil || len(removed) != 0 || !bytes.Equal(out, plain) {
		t.Fatalf("%v %v", removed, err)
	}
}

func TestJPEGKeepsAnICCProfileAndTheAdobeMarker(t *testing.T) {
	plain := encodeJPEG(t)
	icc := jpegSeg(0xE2, append([]byte("ICC_PROFILE\x00\x01\x01"), bytes.Repeat([]byte{7}, 40)...))
	adobe := jpegSeg(0xEE, []byte("Adobe\x00\x64\x00\x00\x00\x00\x01"))
	out, removed, err := Clean(withJPEGSegments(plain, nil, icc, adobe))
	if err != nil || len(removed) != 0 || !bytes.Contains(out, icc) || !bytes.Contains(out, adobe) {
		t.Fatalf("a colour profile or transform marker was dropped: %v %v", removed, err)
	}
}

func TestJPEGDropsAnApplicationSegmentThatOnlyLooksLikeAProfile(t *testing.T) {
	plain := encodeJPEG(t)
	out, removed, err := Clean(withJPEGSegments(plain, nil, jpegSeg(0xE2, []byte("MPF\x00"+secret))))
	if err != nil || bytes.Contains(out, []byte(secret)) || !contains(removed, "application data") {
		t.Fatalf("%v %v", removed, err)
	}
}

func gifFrames(t testing.TB) []byte {
	pal := color.Palette{color.NRGBA{0, 0, 0, 255}, color.NRGBA{255, 0, 0, 255}, color.NRGBA{0, 255, 0, 255}, color.NRGBA{0, 0, 255, 255}}
	mk := func(c uint8) *image.Paletted {
		p := image.NewPaletted(image.Rect(0, 0, 10, 8), pal)
		for i := range p.Pix {
			p.Pix[i] = (uint8(i) + c) % 4
		}
		return p
	}
	var b bytes.Buffer
	if err := gif.EncodeAll(&b, &gif.GIF{Image: []*image.Paletted{mk(0), mk(1), mk(2)}, Delay: []int{5, 10, 15}, Disposal: []byte{gif.DisposalNone, gif.DisposalBackground, gif.DisposalNone}, LoopCount: 3}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func gifExt(label byte, blocks ...[]byte) []byte {
	out := []byte{0x21, label}
	for _, b := range blocks {
		out = append(out, byte(len(b)))
		out = append(out, b...)
	}
	return append(out, 0)
}

// insertBeforeFirstFrame puts extensions between the header (with its colour table and loop extension) and the first
// graphic control extension, and trailing bytes after the trailer.
func insertGIF(g []byte, at int, ext []byte) []byte {
	return append(append(append([]byte{}, g[:at]...), ext...), g[at:]...)
}

func TestGIFLosesCommentsXmpAndTrailingDataButKeepsEveryFrameAndItsTiming(t *testing.T) {
	g := gifFrames(t)
	gce := bytes.Index(g, []byte{0x21, 0xF9}) // first graphic control extension
	if gce < 0 {
		t.Fatal("no GCE")
	}
	dirty := insertGIF(g, gce, gifExt(0xFE, []byte("shot by "+secret)))
	dirty = insertGIF(dirty, gce, gifExt(0xFF, []byte("XMP DataXMP"), []byte("<x:xmpmeta>"+secret)))
	dirty = append(dirty, []byte("trailing "+secret)...)

	out, removed, err := Clean(dirty)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte(secret)) {
		t.Fatal("private text survived")
	}
	for _, name := range []string{"comment", "XMP", "data after the image"} {
		if !contains(removed, name) {
			t.Errorf("did not report %q: %v", name, removed)
		}
	}
	assertAllowed(t, out)
	a, _ := gif.DecodeAll(bytes.NewReader(g))
	b, err := gif.DecodeAll(bytes.NewReader(out))
	if err != nil || len(b.Image) != 3 || b.LoopCount != 3 || b.Delay[0] != 5 || b.Delay[1] != 10 || b.Delay[2] != 15 || b.Disposal[1] != gif.DisposalBackground {
		t.Fatalf("animation changed: %+v %v", b, err)
	}
	for i := range a.Image {
		if !samePixels(a.Image[i], b.Image[i]) {
			t.Fatalf("frame %d changed", i)
		}
	}
}

func TestGIFWithNothingToRemoveKeepsItsBytes(t *testing.T) {
	g := gifFrames(t)
	out, removed, err := Clean(g)
	if err != nil || len(removed) != 0 || !bytes.Equal(out, g) {
		t.Fatalf("%v %v", removed, err)
	}
}

// A graphic control extension in front of a plain-text overlay belongs to the overlay by the specification and to the next
// frame in some decoders, so such a file is refused instead of cleaned by guessing.
func TestGIFWithAPlainTextOverlayIsRefused(t *testing.T) {
	g := gifFrames(t)
	at := bytes.Index(g, []byte{0x21, 0xF9})
	dirty := insertGIF(g, at, gifExt(0x01, make([]byte, 12), []byte(secret)))
	if out, _, err := Clean(dirty); err == nil || out != nil {
		t.Fatalf("accepted: %v", err)
	}
}

func TestMalformedAndForeignInputIsRefused(t *testing.T) {
	png := encodePNG(t)
	jpg := encodeJPEG(t)
	gf := gifFrames(t)
	for name, b := range map[string][]byte{
		"empty":                    nil,
		"text":                     []byte("hello"),
		"svg":                      []byte("<svg xmlns='http://www.w3.org/2000/svg'/>"),
		"png cut off":              png[:len(png)-20],
		"png without IEND":         png[:len(png)-12],
		"png chunk too long":       append(append([]byte{}, png[:8]...), 0x7F, 0xFF, 0xFF, 0xFF, 'I', 'H', 'D', 'R'),
		"png critical unknown":     withPNGChunks(png, nil, pngChunk("ABCD", []byte("x"))),
		"png first chunk not IHDR": append(append([]byte{}, png[:8]...), pngChunk("tEXt", []byte("a\x00b"))...),
		"jpeg cut off":             jpg[:len(jpg)/2],
		"jpeg segment too long":    withJPEGSegments(jpg, nil, []byte{0xFF, 0xE1, 0xFF, 0xF0, 1, 2}),
		"jpeg segment length 1":    withJPEGSegments(jpg, nil, []byte{0xFF, 0xFE, 0x00, 0x01}),
		"gif cut off":              gf[:len(gf)/2],
		"gif unknown block":        append(append([]byte{}, gf[:13+12]...), 0x99),
	} {
		t.Run(name, func(t *testing.T) {
			out, removed, err := Clean(b)
			if err == nil {
				t.Fatalf("accepted: %d bytes, removed %v", len(out), removed)
			}
			if out != nil || removed != nil {
				t.Fatal("a refusal must return nothing")
			}
			if !errors.Is(err, ErrUnsupported) && !errors.Is(err, ErrChanged) {
				t.Fatalf("unexpected error type: %v", err)
			}
		})
	}
}

func TestOrientationReading(t *testing.T) {
	be := []byte{'M', 'M', 0, 0x2A, 0, 0, 0, 8, 0, 1, 0x01, 0x12, 0, 3, 0, 0, 0, 1, 0, 8, 0, 0, 0, 0, 0, 0}
	for name, c := range map[string]struct {
		in   []byte
		want int
	}{
		"little endian 6":        {exifWith(6, "x"), 6},
		"big endian 8":           {be, 8},
		"normal":                 {exifWith(1, "x"), 1},
		"out of range 9":         {exifWith(9, "x"), 1},
		"out of range 0":         {exifWith(0, "x"), 1},
		"empty":                  {nil, 1},
		"bad byte order":         {[]byte("XX\x2A\x00\x08\x00\x00\x00\x00\x00"), 1},
		"offset past the end":    {[]byte{'I', 'I', 0x2A, 0, 0xFF, 0xFF, 0xFF, 0x7F, 0, 0}, 1},
		"entry count past end":   {[]byte{'I', 'I', 0x2A, 0, 8, 0, 0, 0, 0xFF, 0xFF}, 1},
		"orientation as LONG":    {append([]byte{'I', 'I', 0x2A, 0, 8, 0, 0, 0, 1, 0}, 0x12, 0x01, 4, 0, 1, 0, 0, 0, 6, 0, 0, 0, 0, 0, 0, 0), 6},
		"orientation RATIONAL":   {append([]byte{'I', 'I', 0x2A, 0, 8, 0, 0, 0, 1, 0}, 0x12, 0x01, 5, 0, 1, 0, 0, 0, 6, 0, 0, 0, 0, 0, 0, 0), 1},
		"orientation count is 2": {append([]byte{'I', 'I', 0x2A, 0, 8, 0, 0, 0, 1, 0}, 0x12, 0x01, 3, 0, 2, 0, 0, 0, 6, 0, 0, 0, 0, 0, 0, 0), 1},
	} {
		t.Run(name, func(t *testing.T) {
			if got := exifOrientation(c.in); got != c.want {
				t.Fatalf("got %d want %d", got, c.want)
			}
		})
	}
}

// If a cleaned file would draw something else, it is refused instead of published.
func TestSamePictureNoticesADifferentPicture(t *testing.T) {
	a := encodePNG(t)
	var b bytes.Buffer
	im := gradient(24, 16)
	im.SetNRGBA(3, 3, color.NRGBA{1, 2, 3, 255})
	if err := png.Encode(&b, im); err != nil {
		t.Fatal(err)
	}
	if err := samePicture(a, b.Bytes()); !errors.Is(err, ErrChanged) {
		t.Fatalf("one changed pixel was not noticed: %v", err)
	}
	g := gifFrames(t)
	g2 := bytes.Clone(g)
	g2[bytes.Index(g2, []byte{0x21, 0xF9})+4] = 99 // a different delay
	if err := samePicture(g, g2); !errors.Is(err, ErrChanged) {
		t.Fatalf("a changed delay was not noticed: %v", err)
	}
}

func FuzzClean(f *testing.F) {
	f.Add(encodePNG(f))
	f.Add(encodeJPEG(f))
	f.Add(gifFrames(f))
	f.Add(withPNGChunks(encodePNG(f), []byte("tail"), pngChunk("tEXt", []byte("k\x00v")), pngChunk("eXIf", exifWith(6, "x"))))
	f.Add(withJPEGSegments(encodeJPEG(f), []byte("tail"), jpegSeg(0xE1, append([]byte("Exif\x00\x00"), exifWith(3, "x")...)), jpegSeg(0xFE, []byte("c"))))
	f.Fuzz(func(t *testing.T, b []byte) {
		out, _, err := Clean(b) // must not panic or hang
		if err != nil {
			return
		}
		if len(out) > len(b)+40 { // only a minimal orientation record can be added
			t.Fatalf("output grew from %d to %d", len(b), len(out))
		}
		if !bytes.Equal(out, b) { // what Clean changed must be clean in the independent sense of allowedStructure
			if err := allowedStructure(out); err != nil {
				t.Fatalf("Clean accepted and produced something that is not clean: %v", err)
			}
		}
		if _, _, err := Clean(out); err != nil { // cleaning is stable
			t.Fatalf("a cleaned image is refused when cleaned again: %v", err)
		}
	})
}

func contains(s []string, v string) bool { return slices.Contains(s, v) }

// ---- findings from the review of this package: each of these returned success with the private bytes still in the file ----

func TestJFIFThumbnailAndBytesAfterTheHeaderAreDropped(t *testing.T) {
	j := encodeJPEG(t)
	jf := jpegSeg(0xE0, append([]byte("JFIF\x00\x01\x02\x00\x00\x01\x00\x01\x02\x02"), []byte("THUMBSECRETTHUMB!")...))
	out, removed, err := Clean(withJPEGSegments(j, nil, jf))
	if err != nil || bytes.Contains(out, []byte("THUMBSECRET")) || !contains(removed, "embedded thumbnail") {
		t.Fatalf("%v %v", removed, err)
	}
	assertAllowed(t, out)
	// an ordinary JFIF header (no thumbnail) is kept exactly
	plain := jpegSeg(0xE0, []byte("JFIF\x00\x01\x01\x00\x00\x01\x00\x01\x00\x00"))
	out, removed, err = Clean(withJPEGSegments(j, nil, plain))
	if err != nil || len(removed) != 0 || !bytes.Contains(out, plain) {
		t.Fatalf("a plain JFIF header was changed: %v %v", removed, err)
	}
}

func TestAdobeMarkerKeepsOnlyItsTwelveBytes(t *testing.T) {
	j := encodeJPEG(t)
	out, removed, err := Clean(withJPEGSegments(j, nil, jpegSeg(0xEE, append([]byte("Adobe\x00\x64\x00\x00\x00\x00\x01"), []byte("ADOBESECRET")...))))
	if err != nil || bytes.Contains(out, []byte("ADOBESECRET")) || !contains(removed, "application data") || !bytes.Contains(out, []byte("Adobe\x00\x64\x00\x00\x00\x00\x01")) {
		t.Fatalf("%v %v", removed, err)
	}
	assertAllowed(t, out)
}

func TestAColourProfileIsKeptAndSaidSo(t *testing.T) {
	j := encodeJPEG(t)
	icc := jpegSeg(0xE2, append([]byte("ICC_PROFILE\x00\x01\x01"), bytes.Repeat([]byte{7}, 40)...))
	_, _, kept, err := CleanWithNotes(withJPEGSegments(j, nil, icc))
	if err != nil || !contains(kept, "colour profile") {
		t.Fatalf("%v %v", kept, err)
	}
	p := withPNGChunks(encodePNG(t), nil, pngChunk("iCCP", append([]byte("display\x00\x00"), bytes.Repeat([]byte{1}, 10)...)))
	if _, _, kept, err := CleanWithNotes(p); err != nil || !contains(kept, "colour profile") {
		t.Fatalf("png: %v %v", kept, err)
	}
	if _, _, kept, _ := CleanWithNotes(encodePNG(t)); len(kept) != 0 {
		t.Fatalf("nothing was kept in a plain picture: %v", kept)
	}
}

// What follows IEND is not part of the picture, so it must not decide how the picture is turned.
func TestAnExifAfterTheEndOfAPNGDoesNotRotateIt(t *testing.T) {
	tail := append(pngChunk("eXIf", exifWith(6, secret)), pngChunk("tEXt", []byte("k\x00"+secret))...)
	out, removed, err := Clean(withPNGChunks(encodePNG(t), tail))
	if err != nil || bytes.Contains(out, []byte("eXIf")) || bytes.Contains(out, []byte(secret)) || !contains(removed, "data after the image") {
		t.Fatalf("%v %v", removed, err)
	}
	assertAllowed(t, out)
}

func TestPNGChunksOfTheWrongSizeAreDroppedNotCarried(t *testing.T) {
	p := withPNGChunks(encodePNG(t), nil,
		pngChunk("bKGD", []byte(secret+secret)), // should be 1 to 6 bytes
		pngChunk("pHYs", []byte(secret)),        // should be 9
		pngChunk("fdAT", []byte("\x00\x00\x00\x01"+secret)),
	)
	out, removed, err := Clean(p)
	if err != nil || bytes.Contains(out, []byte(secret)) || !contains(removed, "other chunk") {
		t.Fatalf("%v %v", removed, err)
	}
	assertAllowed(t, out)
}

func TestGIFExtensionsKeepOnlyTheirCanonicalForm(t *testing.T) {
	g := gifFrames(t)
	at := bytes.Index(g, []byte{0x21, 0xF9})
	// a loop extension with extra sub-blocks after the loop count
	loop := gifExt(0xFF, []byte("NETSCAPE2.0"), []byte{1, 3, 0}, []byte("hidden "+secret)) // the same loop count as the file has
	out, removed, err := Clean(insertGIF(g, at, loop))
	if err != nil || bytes.Contains(out, []byte(secret)) || !contains(removed, "application data") {
		t.Fatalf("%v %v", removed, err)
	}
	assertAllowed(t, out)
	// a graphic control extension with a second block is not one we can keep honestly
	bad := insertGIF(g, at, gifExt(0xF9, []byte{0, 0, 0, 0}, []byte("more")))
	if _, _, err := Clean(bad); err == nil {
		t.Fatal("an odd graphic control extension was accepted")
	}
}

func TestAGIFTooLargeToCheckIsRefusedBeforeItIsDecoded(t *testing.T) {
	g := gifFrames(t)
	big := bytes.Clone(g)
	big[6], big[7], big[8], big[9] = 0xFF, 0xFF, 0xFF, 0xFF // a 65535 x 65535 canvas
	if _, _, err := Clean(big); err == nil {
		t.Fatal("accepted a canvas of four gigapixels")
	}
	// many frames that each decode cheaply but together exceed the budget
	var many bytes.Buffer
	many.Write(g[:13+6])
	for i := 0; i < 100; i++ {
		many.Write([]byte{0x2C, 0, 0, 0, 0, 0xFF, 0xFF, 0xFF, 0xFF, 0x00, 0x02, 0x01, 0x04, 0x00})
	}
	many.WriteByte(0x3B)
	if _, _, err := Clean(many.Bytes()); err == nil {
		t.Fatal("accepted frames beyond the pixel budget")
	}
}

func TestOrientationStoredAsLongIsHonoured(t *testing.T) {
	long := []byte{'I', 'I', 0x2A, 0, 8, 0, 0, 0, 1, 0, 0x12, 0x01, 4, 0, 1, 0, 0, 0, 6, 0, 0, 0, 0, 0, 0, 0}
	if got := exifOrientation(long); got != 6 {
		t.Fatalf("got %d", got)
	}
}

// ---- an oracle that does not share code with the cleaner ----

// assertAllowed walks a cleaned file with its own simple reader and fails on anything that is not on the allowlist, on a
// kept record of an unexpected size, and on bytes after the end of the image. It exists because the pixel check cannot see
// what a decoder ignores, and a fuzzer needs a statement of what "clean" means that does not come from the code under test.
func assertAllowed(t testing.TB, b []byte) {
	t.Helper()
	if err := allowedStructure(b); err != nil {
		t.Fatalf("not clean: %v", err)
	}
}

func allowedStructure(b []byte) error {
	switch {
	case bytes.HasPrefix(b, pngSig):
		pos := 8
		for pos+12 <= len(b) {
			n := int(binary.BigEndian.Uint32(b[pos:]))
			if n < 0 || n > len(b)-pos-12 {
				return errors.New("png: bad length")
			}
			typ := string(b[pos+4 : pos+8])
			if !pngKeep[typ] && typ != "eXIf" {
				return errors.New("png: chunk " + typ)
			}
			if typ == "eXIf" && n != 26 {
				return errors.New("png: eXIf is more than an orientation")
			}
			if sz, ok := pngSize[typ]; ok && (n < sz[0] || n > sz[1]) {
				return errors.New("png: size of " + typ)
			}
			pos += 12 + n
			if typ == "IEND" {
				if pos != len(b) {
					return errors.New("png: data after IEND")
				}
				return nil
			}
		}
		return errors.New("png: no IEND")
	case len(b) > 2 && b[0] == 0xFF && b[1] == 0xD8:
		pos := 2
		for pos+2 <= len(b) {
			if b[pos] != 0xFF {
				return errors.New("jpeg: not a marker")
			}
			m := b[pos+1]
			if m == 0xD9 {
				if pos+2 != len(b) {
					return errors.New("jpeg: data after EOI")
				}
				return nil
			}
			if m >= 0xD0 && m <= 0xD7 || m == 0x01 {
				pos += 2
				continue
			}
			if pos+4 > len(b) {
				return errors.New("jpeg: truncated")
			}
			l := int(b[pos+2])<<8 | int(b[pos+3])
			if l < 2 || pos+2+l > len(b) {
				return errors.New("jpeg: bad length")
			}
			p := b[pos+4 : pos+2+l]
			switch {
			case m == 0xE0 && (len(p) != 14 || string(p[:5]) != "JFIF\x00" || p[12] != 0 || p[13] != 0):
				return errors.New("jpeg: JFIF is more than the header")
			case m == 0xE1 && (len(p) != 6+26 || string(p[:6]) != "Exif\x00\x00"):
				return errors.New("jpeg: APP1 is more than an orientation")
			case m == 0xE2 && !bytes.HasPrefix(p, []byte("ICC_PROFILE\x00")):
				return errors.New("jpeg: APP2")
			case m == 0xEE && (len(p) != 12 || string(p[:5]) != "Adobe"):
				return errors.New("jpeg: Adobe is more than its fields")
			case m == 0xE3 || m >= 0xE4 && m <= 0xED || m == 0xEF || m == 0xFE:
				return errors.New("jpeg: application data or comment")
			}
			pos += 2 + l
			if m == 0xDA { // skip the entropy-coded data to the next real marker
				for pos+1 < len(b) && !(b[pos] == 0xFF && b[pos+1] != 0 && !(b[pos+1] >= 0xD0 && b[pos+1] <= 0xD7) && b[pos+1] != 0xFF) {
					pos++
				}
			}
		}
		return errors.New("jpeg: no EOI")
	case bytes.HasPrefix(b, []byte("GIF8")):
		pos := 13
		if b[10]&0x80 != 0 {
			pos += 3 << (b[10]&7 + 1)
		}
		for pos < len(b) {
			switch b[pos] {
			case 0x3B:
				if pos+1 != len(b) {
					return errors.New("gif: data after the trailer")
				}
				return nil
			case 0x21:
				label := b[pos+1]
				end, err := skipSubBlocks(b, pos+2)
				if err != nil {
					return err
				}
				switch label {
				case 0xF9:
					if end-pos != 8 {
						return errors.New("gif: graphic control extension is more than its fields")
					}
				case 0xFF:
					if end-pos != 19 || (string(b[pos+3:pos+14]) != "NETSCAPE2.0" && string(b[pos+3:pos+14]) != "ANIMEXTS1.0") {
						return errors.New("gif: application extension")
					}
				default:
					return errors.New("gif: extension")
				}
				pos = end
			case 0x2C:
				hdr := 10
				if b[pos+9]&0x80 != 0 {
					hdr += 3 << (b[pos+9]&7 + 1)
				}
				end, err := skipSubBlocks(b, pos+hdr+1)
				if err != nil {
					return err
				}
				pos = end
			default:
				return errors.New("gif: block")
			}
		}
		return errors.New("gif: no trailer")
	}
	return errors.New("not an image")
}
