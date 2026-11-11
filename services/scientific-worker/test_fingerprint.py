import hashlib
import os
import tempfile
import unittest

import fingerprint as fp


def fits_like(path: str, data: bytes) -> int:
    cards = [b"SIMPLE  =                    T", b"BITPIX  =                    8", b"END"]
    hdr = b"".join(c.ljust(80) for c in cards)
    hdr = hdr.ljust(-(-len(hdr) // 2880) * 2880)
    with open(path, "wb") as f:
        f.write(hdr + data)
    return len(hdr)


class FingerprintTest(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.mkdtemp()
        self.path = os.path.join(self.dir, "a.fits")
        self.data = bytes((i * 7) % 251 for i in range(3 << 20))
        self.hlen = fits_like(self.path, self.data)

    def test_stable_and_header_sensitive(self):
        a = fp.sampled(self.path)
        self.assertEqual(a, fp.sampled(self.path))
        data = bytearray(open(self.path, "rb").read())
        data[100] ^= 1                                    # inside the header
        open(self.path, "wb").write(data)
        self.assertNotEqual(a, fp.sampled(self.path))

    def test_blind_spot_is_not_seen_by_sampled_but_by_full(self):
        a, fa = fp.sampled(self.path), fp.full(self.path, 1 << 30)[0]
        data = bytearray(open(self.path, "rb").read())
        data[self.hlen + 1_000_001] ^= 1                  # between two sampled blocks
        open(self.path, "wb").write(data)
        self.assertEqual(a, fp.sampled(self.path))
        self.assertNotEqual(fa, fp.full(self.path, 1 << 30)[0])

    def test_full_matches_hashlib_and_obeys_the_cap(self):
        self.assertEqual(fp.full(self.path, 1 << 30)[0], hashlib.sha256(open(self.path, "rb").read()).hexdigest())
        self.assertIsNone(fp.full(self.path, 1000))

    def test_small_and_non_fits_files(self):
        p = os.path.join(self.dir, "t.csv")
        open(p, "wb").write(b"a,b\n1,2\n")
        self.assertEqual(len(fp.sampled(p)), 64)


if __name__ == "__main__":
    unittest.main()
