"""Content fingerprints for files on this machine (ADR 0053); the host-side twin of services/api/internal/identity.

"sampled/1" is a SHA-256 over the file size, the primary FITS header and 16 blocks of 64 KiB at fixed offsets across the data.
It reads about 1 MiB however large the file is. It is a function of the bytes it reads: a different value proves the files
differ, an equal value does not prove they are the same. "full" is the SHA-256 of every byte, capped and read in chunks.
The layout must stay byte-identical to identity.go (a test hashes the same files with both).
"""
import hashlib
import os
import struct
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))   # python -P does not add the script dir
from srcopen import source_file

ALGORITHM = "sampled/1"
BLOCK = 64 << 10
BLOCKS = 16
MAX_HEADER = 1 << 20
FITS_BLOCK = 2880
DEFAULT_FULL_CAP = 4 << 30      # a full hash goes through the host's worker and its request timeout: keep it modest
MAX_FULL_CAP = 64 << 30


def primary_header(f, size: int) -> bytes:
    f.seek(0)
    if f.read(9) != b"SIMPLE  =":
        return b""
    hdr = b""
    while len(hdr) + FITS_BLOCK <= min(size, MAX_HEADER):
        f.seek(len(hdr))
        blk = f.read(FITS_BLOCK)
        if len(blk) != FITS_BLOCK:
            raise OSError("short read")
        hdr += blk
        if any(blk[c:c + 8] == b"END     " for c in range(0, FITS_BLOCK, 80)):
            return hdr
    return b""


def sampled(path: str) -> str:
    with source_file(path) as (_, f):
        size = os.fstat(f.fileno()).st_size
        hdr = primary_header(f, size)
        h = hashlib.sha256()
        h.update((ALGORITHM + "\n").encode())
        h.update(struct.pack(">Q", size))
        h.update(struct.pack(">Q", len(hdr)))
        h.update(hdr)
        data_len = size - len(hdr)
        for i in range(BLOCKS):
            span = data_len - BLOCK
            off = span * i // (BLOCKS - 1) if span > 0 else 0
            n = max(0, min(BLOCK, data_len - off))
            h.update(struct.pack(">Q", len(hdr) + off))
            h.update(struct.pack(">Q", n))
            if n:
                f.seek(len(hdr) + off)
                blk = f.read(n)
                if len(blk) != n:
                    raise OSError("short read")
                h.update(blk)
        return h.hexdigest()


def full(path: str, cap: int):
    """(sha256 hex, bytes read), or None when the file is above the cap (checked before reading and while reading)."""
    with source_file(path) as (_, f):
        if os.fstat(f.fileno()).st_size > cap:
            return None
        h, done = hashlib.sha256(), 0
        while True:
            chunk = f.read(1 << 20)
            if not chunk:
                return h.hexdigest(), done
            done += len(chunk)
            if done > cap:
                return None
            h.update(chunk)
