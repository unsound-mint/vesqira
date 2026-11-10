// Package imgmeta removes what an image carries besides its picture, before the picture leaves the owner's laptop.
//
// A pasted screenshot or photo can hold the camera or software that made it, a location, a time, a user name, a comment, an
// embedded thumbnail, or whole extra files after the end of the image. A reader of a published report has no use for any of
// it and the owner did not choose to share it. Clean keeps an allowlist of what is needed to draw the picture (and the one
// thing that changes how it is drawn: the EXIF orientation, as a minimal record) and drops the rest.
//
// The image is untrusted input. Parsing is bounds-checked and refuses anything it does not understand rather than guessing,
// and the result is checked by decoding both files and comparing every pixel (and, for GIF, every frame's delay, disposal
// and transparency): if the cleaned file would not draw the same picture, Clean returns an error and the caller must not
// publish the image.
package imgmeta

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/gif"
	_ "image/jpeg" // decoders for the check
	_ "image/png"
	"reflect"
	"slices"
)

// ErrUnsupported is returned for anything that is not a well-formed PNG, JPEG or GIF.
var ErrUnsupported = errors.New("not a PNG, JPEG or GIF this can clean")

// ErrChanged is returned when the cleaned file does not draw the same picture as the original.
var ErrChanged = errors.New("the cleaned image does not match the original")

// Clean returns the image without its metadata, and what was removed (short names such as "EXIF" or "comment", each once,
// in the order met). When the bytes are already as clean as they can be, out is data itself, so the picture keeps its hash;
// removed can be empty while out differs from data only when the file was merely normalised (fill bytes, a repeated header).
func Clean(data []byte) (out []byte, removed []string, err error) {
	out, removed, _, err = CleanWithNotes(data)
	return out, removed, err
}

// CleanWithNotes is Clean that also says what it kept although it can carry text the author did not mean to share: today
// only an ICC colour profile ("colour profile"), which is needed to draw the colours and which a calibrated display can
// fill with a name or serial number. Callers show this to the owner before publishing.
func CleanWithNotes(data []byte) (out []byte, removed, kept []string, err error) {
	var r remover
	switch {
	case bytes.HasPrefix(data, pngSig):
		out, err = cleanPNG(data, &r)
	case len(data) > 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		out, err = cleanJPEG(data, &r)
	case bytes.HasPrefix(data, []byte("GIF87a")) || bytes.HasPrefix(data, []byte("GIF89a")):
		out, err = cleanGIF(data, &r)
	default:
		return nil, nil, nil, ErrUnsupported
	}
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%w: %v", ErrUnsupported, err)
	}
	if bytes.Equal(out, data) {
		return data, nil, r.kept, nil
	}
	if err := samePicture(data, out); err != nil {
		return nil, nil, nil, err
	}
	return out, r.names, r.kept, nil
}

// remover records what was dropped and what was knowingly kept, each name once.
type remover struct{ names, kept []string }

func (r *remover) keep(name string) {
	if !slices.Contains(r.kept, name) {
		r.kept = append(r.kept, name)
	}
}

func (r *remover) drop(name string) {
	if !slices.Contains(r.names, name) {
		r.names = append(r.names, name)
	}
}

// samePicture decodes both files and compares what is drawn.
func samePicture(a, b []byte) error {
	if bytes.HasPrefix(a, []byte("GIF")) {
		ga, err := gif.DecodeAll(bytes.NewReader(a))
		if err != nil {
			return fmt.Errorf("%w: the original does not decode: %v", ErrUnsupported, err)
		}
		gb, err := gif.DecodeAll(bytes.NewReader(b))
		if err != nil {
			return fmt.Errorf("%w: %v", ErrChanged, err)
		}
		if len(ga.Image) != len(gb.Image) || !reflect.DeepEqual(ga.Delay, gb.Delay) || !reflect.DeepEqual(ga.Disposal, gb.Disposal) ||
			ga.LoopCount != gb.LoopCount || ga.Config.Width != gb.Config.Width || ga.Config.Height != gb.Config.Height || ga.BackgroundIndex != gb.BackgroundIndex {
			return fmt.Errorf("%w: GIF timing or frames differ", ErrChanged)
		}
		for i := range ga.Image {
			if !samePixels(ga.Image[i], gb.Image[i]) {
				return fmt.Errorf("%w: GIF frame %d differs", ErrChanged, i)
			}
		}
		return nil
	}
	ia, _, err := image.Decode(bytes.NewReader(a))
	if err != nil {
		return fmt.Errorf("%w: the original does not decode: %v", ErrUnsupported, err)
	}
	ib, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrChanged, err)
	}
	if !samePixels(ia, ib) {
		return ErrChanged
	}
	return nil
}

// samePixels is equal bounds and equal pixels. The decoders return a few concrete types; for those the pixel buffers are
// compared directly (fast, and exact), and any other type falls back to comparing every pixel through At.
func samePixels(a, b image.Image) bool {
	if a.Bounds() != b.Bounds() {
		return false
	}
	switch x := a.(type) {
	case *image.NRGBA:
		y, ok := b.(*image.NRGBA)
		return ok && x.Stride == y.Stride && bytes.Equal(x.Pix, y.Pix)
	case *image.RGBA:
		y, ok := b.(*image.RGBA)
		return ok && x.Stride == y.Stride && bytes.Equal(x.Pix, y.Pix)
	case *image.NRGBA64:
		y, ok := b.(*image.NRGBA64)
		return ok && x.Stride == y.Stride && bytes.Equal(x.Pix, y.Pix)
	case *image.RGBA64:
		y, ok := b.(*image.RGBA64)
		return ok && x.Stride == y.Stride && bytes.Equal(x.Pix, y.Pix)
	case *image.Gray:
		y, ok := b.(*image.Gray)
		return ok && x.Stride == y.Stride && bytes.Equal(x.Pix, y.Pix)
	case *image.Gray16:
		y, ok := b.(*image.Gray16)
		return ok && x.Stride == y.Stride && bytes.Equal(x.Pix, y.Pix)
	case *image.Paletted:
		y, ok := b.(*image.Paletted)
		return ok && x.Stride == y.Stride && bytes.Equal(x.Pix, y.Pix) && reflect.DeepEqual(x.Palette, y.Palette)
	case *image.YCbCr:
		y, ok := b.(*image.YCbCr)
		return ok && x.SubsampleRatio == y.SubsampleRatio && x.YStride == y.YStride && x.CStride == y.CStride &&
			bytes.Equal(x.Y, y.Y) && bytes.Equal(x.Cb, y.Cb) && bytes.Equal(x.Cr, y.Cr)
	case *image.CMYK:
		y, ok := b.(*image.CMYK)
		return ok && x.Stride == y.Stride && bytes.Equal(x.Pix, y.Pix)
	}
	bounds := a.Bounds()
	for yy := bounds.Min.Y; yy < bounds.Max.Y; yy++ {
		for xx := bounds.Min.X; xx < bounds.Max.X; xx++ {
			ar, ag, ab, aa := a.At(xx, yy).RGBA()
			br, bg, bb, ba := b.At(xx, yy).RGBA()
			if ar != br || ag != bg || ab != bb || aa != ba {
				return false
			}
		}
	}
	return true
}

// orientationExif is the smallest EXIF record that says how to turn the picture upright: a little-endian TIFF header and
// one entry, Orientation (0x0112), a SHORT holding 2..8. Orientation 1 (normal) needs no record at all. Browsers draw a
// picture by its EXIF orientation, so dropping it would silently rotate or mirror the picture a reader sees.
func orientationExif(o int) []byte {
	return []byte{
		'I', 'I', 0x2A, 0, 8, 0, 0, 0, // TIFF header, first IFD at offset 8
		1, 0, // one entry
		0x12, 0x01, 3, 0, 1, 0, 0, 0, byte(o), 0, 0, 0, // tag 0x0112, type SHORT, count 1, value
		0, 0, 0, 0, // no next IFD
	}
}

// exifOrientation reads the Orientation tag of IFD0 from a TIFF structure (the part of an EXIF record after "Exif\0\0").
// It returns 1 (normal) when the tag is absent or anything is out of range: a damaged record is not trusted.
func exifOrientation(tiff []byte) int {
	if len(tiff) < 8 {
		return 1
	}
	var u16 func([]byte) int
	var u32 func([]byte) int
	switch string(tiff[:2]) {
	case "II":
		u16 = func(b []byte) int { return int(b[0]) | int(b[1])<<8 }
		u32 = func(b []byte) int { return int(b[0]) | int(b[1])<<8 | int(b[2])<<16 | int(b[3])<<24 }
	case "MM":
		u16 = func(b []byte) int { return int(b[0])<<8 | int(b[1]) }
		u32 = func(b []byte) int { return int(b[0])<<24 | int(b[1])<<16 | int(b[2])<<8 | int(b[3]) }
	default:
		return 1
	}
	if u16(tiff[2:]) != 0x2A {
		return 1
	}
	off := u32(tiff[4:])
	if off < 8 || off+2 > len(tiff) {
		return 1
	}
	n := u16(tiff[off:])
	for i := range n {
		e := off + 2 + 12*i
		if e+12 > len(tiff) {
			return 1
		}
		if u16(tiff[e:]) == 0x0112 {
			if u32(tiff[e+4:]) != 1 {
				return 1
			}
			v := 0
			switch u16(tiff[e+2:]) {
			case 3: // SHORT
				v = u16(tiff[e+8:])
			case 4: // LONG, which some writers use
				v = u32(tiff[e+8:])
			}
			if v >= 2 && v <= 8 {
				return v
			}
			return 1
		}
	}
	return 1
}
