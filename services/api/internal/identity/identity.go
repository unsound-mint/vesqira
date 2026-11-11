// Package identity says whether two files hold the same bytes, and how sure we are (ADR 0053).
//
// A path or a file name proves nothing about bytes. Two honest facts can be had cheaply or expensively:
//
//   - the sampled fingerprint ("sampled/1"): a SHA-256 over the file size, the primary FITS header and 16 blocks of 64 KiB at
//     fixed offsets across the data. It reads about 1 MiB however large the file is, so it is fine on the interactive path. It
//     is a function of the bytes it reads, so a different value proves the files differ; an equal value does NOT prove they
//     are the same (a change between the sampled blocks is not seen). It is never a full hash and is never called one.
//   - the full SHA-256 of every byte: proof, but it reads the whole file, so it runs on request, capped and cancellable.
//
// The sampled algorithm is also implemented by the scientific worker (services/scientific-worker/fingerprint.py) for files
// on a remote host. A test compares the two on the same files; change them together.
package identity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"os"
)

const (
	// SampledAlgorithm names the layout hashed by Sampled. Any change to the layout is a new name, so a stored value is
	// never compared with one made another way.
	SampledAlgorithm = "sampled/1"
	// BlockSize and Blocks are the sampled data blocks: Blocks reads of BlockSize bytes, spread evenly from the end of
	// the header to the end of the file (the first and the last block are always read).
	BlockSize = 64 << 10
	Blocks    = 16
	// MaxHeader bounds how much of the front of a FITS file is taken as its primary header.
	MaxHeader = 1 << 20
	fitsBlock = 2880
	// SmallFull is the size up to which registration also takes the full SHA-256 at once: reading 16 MiB is as cheap as the
	// header read already done, and it gives a small file (a map, a catalog) a proven identity from the start.
	SmallFull = 16 << 20
)

// Covers is the one sentence that says what a sampled fingerprint covers; the UI and the stored identity use it verbatim.
const Covers = "file size, the primary FITS header, and 16 blocks of 64 KiB at fixed offsets across the data; a change elsewhere in the file is not detected"

// ErrTooLarge means a full hash was refused because the file is above the cap.
var ErrTooLarge = errors.New("the file is larger than the cap for a full hash")

// Sampled returns the sampled fingerprint of the file (lowercase hex SHA-256 of the layout described in the package comment).
func Sampled(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() {
		return "", errors.New("not a regular file")
	}
	return sampledFrom(f, st.Size())
}

func sampledFrom(f io.ReaderAt, size int64) (string, error) {
	hdr, err := primaryHeader(f, size)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write([]byte(SampledAlgorithm + "\n"))
	put := func(n int64) {
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(n))
		h.Write(b[:])
	}
	put(size)
	put(int64(len(hdr)))
	h.Write(hdr)
	dataLen := size - int64(len(hdr))
	buf := make([]byte, BlockSize)
	for i := int64(0); i < Blocks; i++ {
		var off int64
		if span := dataLen - BlockSize; span > 0 {
			off = span * i / (Blocks - 1)
		}
		n := min(int64(BlockSize), dataLen-off)
		if n < 0 {
			n = 0
		}
		put(int64(len(hdr)) + off)
		put(n)
		if n > 0 {
			if got, err := f.ReadAt(buf[:n], int64(len(hdr))+off); int64(got) < n { // a file cut short while read is an error, never a hash
				if err == nil {
					err = io.ErrUnexpectedEOF
				}
				return "", err
			}
			h.Write(buf[:n])
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// primaryHeader returns the 2880-byte blocks of the primary FITS header (through the one holding END), or nothing when the
// file does not start like FITS or no END is found within MaxHeader.
func primaryHeader(f io.ReaderAt, size int64) ([]byte, error) {
	magic := make([]byte, 9)
	if n, _ := f.ReadAt(magic, 0); n < 9 || string(magic) != "SIMPLE  =" {
		return nil, nil
	}
	var hdr []byte
	blk := make([]byte, fitsBlock)
	for int64(len(hdr))+fitsBlock <= min(size, MaxHeader) {
		if _, err := f.ReadAt(blk, int64(len(hdr))); err != nil {
			return nil, err
		}
		hdr = append(hdr, blk...)
		for c := 0; c < fitsBlock; c += 80 {
			if bytes.Equal(blk[c:c+8], []byte("END     ")) {
				return hdr, nil
			}
		}
	}
	return nil, nil
}

// Full returns the SHA-256 of every byte and the byte count. It stops with ctx.Err() when ctx is cancelled (checked every
// MiB), with ErrTooLarge before reading when the file is above max, and also if the file grows past max while it is read.
// progress, when set, is told the bytes read so far.
func Full(ctx context.Context, path string, max int64, progress func(done int64)) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	if !st.Mode().IsRegular() {
		return "", 0, errors.New("not a regular file")
	}
	if max > 0 && st.Size() > max {
		return "", 0, ErrTooLarge
	}
	h := sha256.New()
	buf := make([]byte, 1<<20)
	var done int64
	for {
		if err := ctx.Err(); err != nil {
			return "", done, err
		}
		n, err := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
			done += int64(n)
			if max > 0 && done > max {
				return "", done, ErrTooLarge
			}
			if progress != nil {
				progress(done)
			}
		}
		if errors.Is(err, io.EOF) {
			return hex.EncodeToString(h.Sum(nil)), done, nil
		}
		if err != nil {
			return "", done, err
		}
	}
}

// Evidence is what is known about the bytes of one file. Empty strings mean not computed.
type Evidence struct {
	Size    int64
	Sampled string
	Full    string
}

// Relation says what the evidence of two files proves.
const (
	Verified  = "verified"  // the full SHA-256 values are equal: the same bytes
	Probable  = "probable"  // same size and equal sampled fingerprints, no full hash on both: probably the same, not proven
	Different = "different" // the bytes differ for certain (sizes, sampled values or full hashes differ)
	Unknown   = "unknown"   // not enough computed to say
)

// Relate compares two files' evidence. Only equal full hashes give Verified. A difference in any computed value is proof of
// a difference, because every value is a function of the bytes.
func Relate(a, b Evidence) string {
	if a.Full != "" && b.Full != "" {
		if a.Full == b.Full {
			return Verified
		}
		return Different
	}
	if a.Size != b.Size {
		return Different
	}
	if a.Sampled != "" && b.Sampled != "" {
		if a.Sampled == b.Sampled {
			return Probable
		}
		return Different
	}
	return Unknown
}
