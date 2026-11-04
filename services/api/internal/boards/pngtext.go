package boards

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
)

// EmbedText inserts UTF-8 text chunks (iTXt) after the header of a PNG, so a downloaded figure still says where it
// came from when it is separated from the board. The picture's pixels are not touched.
func EmbedText(png []byte, entries [][2]string) ([]byte, error) {
	sig := []byte("\x89PNG\r\n\x1a\n")
	if len(png) < 8+12 || !bytes.Equal(png[:8], sig) {
		return nil, errors.New("not a PNG")
	}
	ihdrLen := int(binary.BigEndian.Uint32(png[8:12]))
	end := 8 + 12 + ihdrLen
	if string(png[12:16]) != "IHDR" || end > len(png) {
		return nil, errors.New("PNG header missing")
	}
	var out bytes.Buffer
	out.Write(png[:end])
	for _, e := range entries {
		var data bytes.Buffer
		data.WriteString(e[0])
		data.Write([]byte{0, 0, 0, 0, 0}) // keyword end, uncompressed, no method, empty language tag, empty translated keyword
		data.WriteString(e[1])
		writeChunk(&out, "iTXt", data.Bytes())
	}
	out.Write(png[end:])
	return out.Bytes(), nil
}

func writeChunk(w *bytes.Buffer, typ string, data []byte) {
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(data)))
	w.Write(n[:])
	w.WriteString(typ)
	w.Write(data)
	crc := crc32.NewIEEE()
	crc.Write([]byte(typ))
	crc.Write(data)
	binary.BigEndian.PutUint32(n[:], crc.Sum32())
	w.Write(n[:])
}
