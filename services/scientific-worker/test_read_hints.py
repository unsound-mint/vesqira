"""Read hints (madvise over the bytes a block reads) must never change a value: the block read with hints equals the block read
without them and an independent astropy read, on random boxes, for float data with NaN and for integer data with BLANK, BSCALE
and BZERO. The hints exist for speed on a cold disk (docs/performance.md); this is the guard that they stay hints."""
import os
import sys
import tempfile
import unittest
from pathlib import Path

import numpy as np
from astropy.io import fits

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import plane_worker as pw  # noqa: E402


def write(path, data, **keys):
    h = fits.PrimaryHDU(data)
    h.header.update({"CTYPE1": "RA---SIN", "CTYPE2": "DEC--SIN", "CTYPE3": "VRAD", "CUNIT3": "m/s",
                     "CRVAL1": 83.0, "CRVAL2": -5.0, "CRVAL3": 0.0, "CRPIX1": 1, "CRPIX2": 1, "CRPIX3": 1,
                     "CDELT1": -0.001, "CDELT2": 0.001, "CDELT3": 1000.0})
    h.header.update(keys)
    h.writeto(path)


class ReadHints(unittest.TestCase):
    def blocks(self, path, expect, boxes):
        c = pw.Cube(path, None)
        for (c0, c1, y0, y1, x0, x1) in boxes:
            ys, xs = slice(y0, y1 + 1), slice(x0, x1 + 1)
            with pw.fits_open(path, do_not_scale_image_data=True) as hdul:
                os.environ["VESQIRA_NO_READ_HINTS"] = "1"
                try:
                    plain = c._channel_block(hdul, c0, c1, ys, xs)
                finally:
                    del os.environ["VESQIRA_NO_READ_HINTS"]
                hinted = c._channel_block(hdul, c0, c1, ys, xs)
            self.assertEqual(plain.dtype, hinted.dtype)
            self.assertTrue(np.array_equal(plain, hinted, equal_nan=True))
            want = expect[c0:c1, y0:y1 + 1, x0:x1 + 1]
            self.assertTrue(np.array_equal(hinted, want, equal_nan=True), (c0, c1, y0, y1, x0, x1))

    def boxes(self, rng, nc, ny, nx, n=25):
        out = []
        for _ in range(n):
            c0 = int(rng.integers(0, nc))
            c1 = int(rng.integers(c0 + 1, nc + 1))
            y0 = int(rng.integers(0, ny))
            x0 = int(rng.integers(0, nx))
            out.append((c0, c1, y0, int(rng.integers(y0, ny)), x0, int(rng.integers(x0, nx))))
        return out

    def test_float_cube_with_nan_and_a_fourth_axis(self):
        rng = np.random.default_rng(3)
        data = rng.random((1, 40, 70, 90)).astype(">f4")
        data[0, rng.random((40, 70, 90)) < 0.1] = np.nan
        with tempfile.TemporaryDirectory() as d:
            p = os.path.join(d, "f.fits")
            write(p, data, CTYPE4="STOKES", CRVAL4=1.0, CRPIX4=1, CDELT4=1.0)
            self.blocks(p, data[0].astype("<f8"), self.boxes(rng, 40, 70, 90))

    def test_integer_cube_with_blank_bscale_and_bzero(self):
        rng = np.random.default_rng(4)
        raw = rng.integers(-300, 300, (30, 50, 60)).astype(">i2")
        raw[rng.random((30, 50, 60)) < 0.1] = -999
        with tempfile.TemporaryDirectory() as d:
            p = os.path.join(d, "i.fits")
            h = fits.PrimaryHDU(raw)
            h.header.update({"BLANK": -999, "BSCALE": 0.5, "BZERO": 10.0})
            h.header.update({"CTYPE1": "RA---SIN", "CTYPE2": "DEC--SIN", "CTYPE3": "VRAD", "CUNIT3": "m/s", "CRVAL1": 83.0, "CRVAL2": -5.0,
                             "CRVAL3": 0.0, "CRPIX1": 1, "CRPIX2": 1, "CRPIX3": 1, "CDELT1": -0.001, "CDELT2": 0.001, "CDELT3": 1000.0})
            h.writeto(p)
            want = raw.astype("<f8") * 0.5 + 10.0
            want[raw == -999] = np.nan           # BLANK is unknown, never a number
            self.blocks(p, want, self.boxes(rng, 30, 50, 60))


if __name__ == "__main__":
    unittest.main()
