package imgmeta

import (
	"bytes"
	"errors"
)

// cleanJPEG keeps the segments that are needed to decode and draw the picture and copies them byte for byte: quantisation
// and Huffman tables, the frame and scan headers with their entropy-coded data, the restart interval, a JFIF header, an ICC
// colour profile and the Adobe colour-transform marker. EXIF and XMP (camera, software, location, time, thumbnails),
// IPTC, comments, every other application segment, and anything after the end-of-image marker are dropped. The EXIF
// orientation, if it is not "normal", is written back as a minimal record because it changes how the picture is drawn.
func cleanJPEG(data []byte, r *remover) ([]byte, error) {
	var jfif, body []byte
	orientation, sawExif := 1, false
	pos := 2
	for pos < len(data) {
		if data[pos] != 0xFF {
			return nil, errors.New("expected a marker")
		}
		for pos < len(data) && data[pos] == 0xFF { // fill bytes
			pos++
		}
		if pos >= len(data) {
			return nil, errors.New("truncated")
		}
		m := data[pos]
		pos++
		switch {
		case m == 0xD9: // end of image
			body = append(body, 0xFF, 0xD9)
			if pos < len(data) {
				r.drop("data after the image")
			}
			return assembleJPEG(jfif, orientation, body), nil
		case m == 0xD8:
			return nil, errors.New("second start-of-image")
		case m == 0x01 || (m >= 0xD0 && m <= 0xD7):
			body = append(body, 0xFF, m)
			continue
		}
		if pos+2 > len(data) {
			return nil, errors.New("truncated segment")
		}
		l := int(data[pos])<<8 | int(data[pos+1])
		end := pos + l
		if l < 2 || end > len(data) {
			return nil, errors.New("bad segment length")
		}
		payload := data[pos+2 : end]
		seg := append([]byte{0xFF, m}, data[pos:end]...)
		switch {
		case m == 0xE0:
			if bytes.HasPrefix(payload, []byte("JFIF\x00")) && len(payload) >= 14 {
				if jfif == nil {
					// the header proper (version, units, densities) with a zero-size thumbnail: a JFIF thumbnail is a
					// second, often uncropped, copy of the picture, and the bytes after it are not ours to carry
					jfif = append([]byte{0xFF, 0xE0, 0x00, 0x10}, payload[:12]...)
					jfif = append(jfif, 0, 0)
					if len(payload) != 14 || payload[12] != 0 || payload[13] != 0 {
						r.drop("embedded thumbnail")
					}
				}
			} else {
				r.drop("embedded thumbnail")
			}
		case m == 0xE1:
			switch {
			case bytes.HasPrefix(payload, []byte("Exif\x00\x00")):
				if !sawExif {
					orientation, sawExif = exifOrientation(payload[6:]), true
				}
				r.drop("EXIF")
			case bytes.HasPrefix(payload, []byte("http://ns.adobe.com/xap/1.0/")):
				r.drop("XMP")
			default:
				r.drop("application data")
			}
		case m == 0xE2:
			if bytes.HasPrefix(payload, []byte("ICC_PROFILE\x00")) {
				body = append(body, seg...)
				r.keep("colour profile")
			} else {
				r.drop("application data")
			}
		case m == 0xEE:
			if bytes.HasPrefix(payload, []byte("Adobe")) && len(payload) >= 12 {
				// version, two flag words and the colour transform: 12 bytes, whatever follows is not ours to carry
				body = append(body, 0xFF, 0xEE, 0x00, 0x0E)
				body = append(body, payload[:12]...)
				if len(payload) != 12 {
					r.drop("application data")
				}
			} else {
				r.drop("application data")
			}
		case m == 0xED:
			r.drop("IPTC")
		case m >= 0xE3 && m <= 0xEF:
			r.drop("application data")
		case m == 0xFE:
			r.drop("comment")
		case m == 0xDA: // start of scan: the header, then the entropy-coded data up to the next real marker
			i := end
			for i < len(data) {
				if data[i] != 0xFF {
					i++
					continue
				}
				if i+1 >= len(data) {
					return nil, errors.New("truncated scan")
				}
				if nb := data[i+1]; nb == 0x00 || (nb >= 0xD0 && nb <= 0xD7) {
					i += 2
				} else if nb == 0xFF {
					i++
				} else {
					break
				}
			}
			if i >= len(data) {
				return nil, errors.New("scan without an end")
			}
			body = append(body, 0xFF, m)
			body = append(body, data[pos:i]...)
			pos = i
			continue
		case (m >= 0xC0 && m <= 0xCF) || m == 0xDB || m == 0xDC || m == 0xDD:
			body = append(body, seg...)
		default:
			r.drop("other segment")
		}
		pos = end
	}
	return nil, errors.New("no end-of-image marker")
}

func assembleJPEG(jfif []byte, orientation int, body []byte) []byte {
	out := []byte{0xFF, 0xD8}
	out = append(out, jfif...)
	if orientation > 1 {
		tiff := orientationExif(orientation)
		n := 2 + 6 + len(tiff)
		out = append(out, 0xFF, 0xE1, byte(n>>8), byte(n))
		out = append(out, "Exif\x00\x00"...)
		out = append(out, tiff...)
	}
	return append(out, body...)
}
