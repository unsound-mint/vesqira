package imgmeta

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
)

var pngSig = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1A, '\n'}

// pngKeep are the chunks needed to decode and draw the picture, its colour description and animation. Everything else
// that is ancillary is dropped; an unknown critical chunk is an error, since it cannot be dropped and cannot be trusted.
var pngKeep = map[string]bool{
	"IHDR": true, "PLTE": true, "IDAT": true, "IEND": true,
	"tRNS": true, "cHRM": true, "gAMA": true, "iCCP": true, "sBIT": true, "sRGB": true, "cICP": true, "mDCV": true, "cLLI": true,
	"bKGD": true, "pHYs": true, "acTL": true, "fcTL": true, "fdAT": true,
}

// pngSize is the length range of the chunks whose size is fixed by the specification. A kept chunk of any other length
// would be a place to carry bytes that the decoder ignores, so it is dropped (an ancillary chunk) instead.
var pngSize = map[string][2]int{
	"cHRM": {32, 32}, "gAMA": {4, 4}, "sRGB": {1, 1}, "cICP": {4, 4}, "mDCV": {24, 24}, "cLLI": {8, 8},
	"pHYs": {9, 9}, "sBIT": {1, 4}, "bKGD": {1, 6}, "tRNS": {0, 256}, "acTL": {8, 8}, "fcTL": {26, 26},
}

// cleanPNG drops text chunks (tEXt, zTXt, iTXt: author, software, comments, XMP), the modification time, EXIF, other
// ancillary chunks and anything after IEND. The EXIF orientation is kept as a minimal eXIf chunk when it is not normal.
func cleanPNG(data []byte, r *remover) ([]byte, error) {
	out := append([]byte{}, pngSig...)
	pos := len(pngSig)
	first, animated := true, false
	for pos+12 <= len(data) {
		n := int(binary.BigEndian.Uint32(data[pos:]))
		if n < 0 || n > len(data)-pos-12 {
			return nil, errors.New("bad chunk length")
		}
		typ := string(data[pos+4 : pos+8])
		chunk := data[pos : pos+12+n]
		if first && typ != "IHDR" {
			return nil, errors.New("IHDR is not first")
		}
		pos += 12 + n
		if sz, ok := pngSize[typ]; ok && (n < sz[0] || n > sz[1]) {
			r.drop("other chunk")
			continue
		}
		if (typ == "fcTL" || typ == "fdAT") && !animated { // animation frames without an animation header
			r.drop("other chunk")
			continue
		}
		switch {
		case pngKeep[typ]:
			out = append(out, chunk...)
			first = false
			animated = animated || typ == "acTL"
			if typ == "iCCP" {
				r.keep("colour profile")
			}
			if typ == "IEND" {
				if n != 0 {
					return nil, errors.New("IEND with data")
				}
				if pos < len(data) {
					r.drop("data after the image")
				}
				return finishPNG(out, data)
			}
		case typ == "eXIf":
			r.drop("EXIF")
		case typ == "tEXt" || typ == "zTXt" || typ == "iTXt":
			r.drop("text")
		case typ == "tIME":
			r.drop("modification time")
		case typ[0]&0x20 != 0: // ancillary (lower-case first letter)
			r.drop("other chunk")
		default:
			return nil, errors.New("unknown critical chunk " + typ)
		}
	}
	return nil, errors.New("no IEND chunk")
}

// finishPNG reads the first eXIf chunk for its orientation and, when it is not normal, inserts a minimal one after IHDR.
func finishPNG(out, data []byte) ([]byte, error) {
	o := 1
	for pos := len(pngSig); pos+12 <= len(data); {
		n := int(binary.BigEndian.Uint32(data[pos:]))
		if n < 0 || n > len(data)-pos-12 {
			break
		}
		typ := string(data[pos+4 : pos+8])
		if typ == "IEND" { // what follows the end of the image is dropped, so it cannot say how to turn the picture
			break
		}
		if typ == "eXIf" {
			o = exifOrientation(data[pos+8 : pos+8+n])
			break
		}
		pos += 12 + n
	}
	if o <= 1 {
		return out, nil
	}
	tiff := orientationExif(o)
	chunk := make([]byte, 0, 12+len(tiff))
	chunk = binary.BigEndian.AppendUint32(chunk, uint32(len(tiff)))
	chunk = append(chunk, "eXIf"...)
	chunk = append(chunk, tiff...)
	chunk = binary.BigEndian.AppendUint32(chunk, crc32.ChecksumIEEE(chunk[4:]))
	ihdrEnd := len(pngSig) + 12 + int(binary.BigEndian.Uint32(out[len(pngSig):]))
	res := append([]byte{}, out[:ihdrEnd]...)
	res = append(res, chunk...)
	return append(res, out[ihdrEnd:]...), nil
}
