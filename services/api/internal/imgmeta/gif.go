package imgmeta

import "errors"

// maxGIFPixels bounds the pixels of all frames of one GIF (the decoder holds every frame, twice while checking). A
// legitimate animation is far below it; the largest allowed is about 64 MB per copy.
const maxGIFPixels = 1 << 26

const maxGIFFrames = 5000

// cleanGIF drops comment extensions, application extensions other than the animation loop count, and anything after the
// trailer. A plain-text overlay is refused: no browser draws one, and a graphic control extension in front of it belongs to
// it by the specification but to the next frame in some decoders, so there is no safe way to remove it without guessing.
func cleanGIF(data []byte, r *remover) ([]byte, error) {
	if len(data) < 14 {
		return nil, errors.New("truncated")
	}
	pos := 13
	if data[10]&0x80 != 0 {
		pos += 3 << (data[10]&7 + 1)
	}
	if pos > len(data) {
		return nil, errors.New("truncated colour table")
	}
	out := append([]byte{}, data[:pos]...)
	pixels, frames := 0, 0
	if w, h := int(data[6])|int(data[7])<<8, int(data[8])|int(data[9])<<8; w*h > maxGIFPixels {
		return nil, errors.New("canvas too large")
	}
	for pos < len(data) {
		switch data[pos] {
		case 0x3B:
			if pos+1 < len(data) {
				r.drop("data after the image")
			}
			return append(out, 0x3B), nil
		case 0x21:
			if pos+2 > len(data) {
				return nil, errors.New("truncated extension")
			}
			label := data[pos+1]
			end, err := skipSubBlocks(data, pos+2)
			if err != nil {
				return nil, err
			}
			ext := data[pos:end]
			switch {
			case label == 0xF9:
				if len(ext) != 8 || ext[2] != 4 { // one block of four bytes: nothing else is part of a graphic control extension
					return nil, errors.New("odd graphic control extension")
				}
				out = append(out, ext...)
			case label == 0xFF && len(ext) == 19 && ext[2] == 11 && ext[14] == 3 && ext[15] == 1 && (string(ext[3:14]) == "NETSCAPE2.0" || string(ext[3:14]) == "ANIMEXTS1.0"):
				out = append(out, ext...) // the loop count and nothing else
			case label == 0xFF && len(ext) >= 14 && ext[2] == 11 && string(ext[3:14]) == "XMP DataXMP":
				r.drop("XMP")
			case label == 0xFF:
				r.drop("application data")
			case label == 0xFE:
				r.drop("comment")
			case label == 0x01:
				return nil, errors.New("plain-text overlay")
			default:
				r.drop("other extension")
			}
			pos = end
		case 0x2C:
			if pos+11 > len(data) {
				return nil, errors.New("truncated image")
			}
			hdr := 10
			if data[pos+9]&0x80 != 0 {
				hdr += 3 << (data[pos+9]&7 + 1)
			}
			if pos+hdr+1 > len(data) {
				return nil, errors.New("truncated image")
			}
			frames++
			pixels += (int(data[pos+5]) | int(data[pos+6])<<8) * (int(data[pos+7]) | int(data[pos+8])<<8)
			if frames > maxGIFFrames || pixels > maxGIFPixels {
				return nil, errors.New("too many frames or pixels")
			}
			end, err := skipSubBlocks(data, pos+hdr+1)
			if err != nil {
				return nil, err
			}
			out = append(out, data[pos:end]...)
			pos = end
		default:
			return nil, errors.New("unknown block")
		}
	}
	return nil, errors.New("no trailer")
}

// skipSubBlocks returns the index after the zero-length terminator of a run of data sub-blocks starting at pos.
func skipSubBlocks(data []byte, pos int) (int, error) {
	for {
		if pos >= len(data) {
			return 0, errors.New("truncated sub-blocks")
		}
		n := int(data[pos])
		pos++
		if n == 0 {
			return pos, nil
		}
		pos += n
	}
}
