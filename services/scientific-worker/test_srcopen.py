"""Opening a source file without following a link (ADR 0050, section 8): the check-then-open window is closed.

The race is made deterministic with srcopen._hook, which runs after the path is resolved and checked and before the open.
"""
import os
import stat
import sys
import tempfile
import unittest

import numpy as np
from astropy.io import fits

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import plane_worker as pw
import srcopen


def write_fits(path, fill):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    fits.PrimaryHDU(np.full((2, 3, 4), fill, dtype="f4")).writeto(path)


class OpenNoFollow(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        base = os.path.realpath(self.tmp.name)
        self.root, self.outside = os.path.join(base, "root"), os.path.join(base, "outside")
        write_fits(os.path.join(self.root, "sub", "cube.fits"), 1.0)
        write_fits(os.path.join(self.outside, "cube.fits"), 99.0)
        self.real = os.path.join(self.root, "sub", "cube.fits")
        self.saved = (srcopen.ROOTS, srcopen._hook)
        srcopen.ROOTS = [self.root]
        self.addCleanup(lambda: setattr(srcopen, "ROOTS", self.saved[0]))
        self.addCleanup(lambda: setattr(srcopen, "_hook", self.saved[1]))

    def not_available(self, fn):
        with self.assertRaises(srcopen.Refused) as cm:
            fn()
        self.assertEqual((cm.exception.code, cm.exception.message), ("forbidden", "not available"))

    def watch_opens(self):
        """Record the inode of every file the walk opens successfully, so a test can show the outside file was never opened."""
        seen, real_open = [], os.open

        def spy(*a, **kw):
            fd = real_open(*a, **kw)
            seen.append(os.fstat(fd).st_ino)
            return fd
        os.open = spy
        self.addCleanup(lambda: setattr(os, "open", real_open))
        return seen

    def test_reads_a_file_inside_the_roots_mapped_and_read_only(self):
        with srcopen.fits_open(self.real) as h:
            self.assertTrue(h[0]._file.memmap)
            self.assertFalse(h[0].data.flags.writeable)       # still a denywrite mapping, which RLIMIT_DATA does not count
            self.assertEqual(float(h[0].data.max()), 1.0)

    def test_a_parent_swapped_for_a_link_after_the_check_is_refused_and_never_read(self):
        outside_ino = os.stat(os.path.join(self.outside, "cube.fits")).st_ino

        def swap(_real):
            os.rename(os.path.join(self.root, "sub"), os.path.join(self.root, "sub.moved"))
            os.symlink(self.outside, os.path.join(self.root, "sub"))
        srcopen._hook = swap
        seen = self.watch_opens()
        self.not_available(lambda: srcopen.open_source(self.real))
        self.assertNotIn(outside_ino, seen)

    def test_the_file_swapped_for_a_link_after_the_check_is_refused_and_never_read(self):
        outside = os.path.join(self.outside, "cube.fits")
        outside_ino = os.stat(outside).st_ino

        def swap(_real):
            os.unlink(self.real)
            os.symlink(outside, self.real)
        srcopen._hook = swap
        seen = self.watch_opens()
        self.not_available(lambda: srcopen.fits_open(self.real).__enter__())
        self.assertNotIn(outside_ino, seen)

    def test_the_worker_refuses_a_swapped_cube_the_same_way(self):
        def swap(_real):
            os.rename(os.path.join(self.root, "sub"), os.path.join(self.root, "sub.moved"))
            os.symlink(self.outside, os.path.join(self.root, "sub"))
        pw.authorize(self.real)                                  # passes: the check is only the first half
        srcopen._hook = swap
        self.not_available(lambda: pw.Cube(self.real))

    def test_an_honest_link_that_resolves_inside_the_roots_keeps_working(self):
        os.symlink(os.path.join(self.root, "sub"), os.path.join(self.root, "scratch"))
        via_link = os.path.join(self.root, "scratch", "cube.fits")
        self.assertEqual(pw.authorize(via_link), self.real)
        real, f = srcopen.open_source(via_link)
        f.close()
        self.assertEqual(real, self.real)
        with srcopen.fits_open(via_link) as h:
            self.assertEqual(float(h[0].data.max()), 1.0)

    def test_a_link_pointing_out_of_the_roots_is_refused_as_before(self):
        os.symlink(self.outside, os.path.join(self.root, "escape"))
        link = os.path.join(self.root, "escape", "cube.fits")
        self.not_available(lambda: pw.authorize(link))
        self.not_available(lambda: srcopen.open_source(link))

    def test_a_fifo_or_a_device_in_place_of_the_file_is_refused(self):
        fifo = os.path.join(self.root, "pipe.fits")
        os.mkfifo(fifo)
        self.not_available(lambda: srcopen.open_source(fifo))     # must neither hang nor open
        srcopen.ROOTS = ["/dev"]
        self.not_available(lambda: srcopen.open_source("/dev/null"))

    def test_a_directory_is_not_a_source_file(self):
        self.not_available(lambda: srcopen.open_source(os.path.join(self.root, "sub")))

    def test_a_different_device_and_inode_than_the_caller_saw_is_refused(self):
        st = os.stat(self.real)
        _, f = srcopen.open_source(self.real, (st.st_dev, st.st_ino))
        f.close()
        self.not_available(lambda: srcopen.open_source(self.real, (st.st_dev, st.st_ino + 1)))

    def test_without_roots_the_walk_still_refuses_a_swapped_component(self):
        srcopen.ROOTS = None
        srcopen._hook = lambda _r: (os.rename(os.path.join(self.root, "sub"), os.path.join(self.root, "sub.moved")),
                                    os.symlink(self.outside, os.path.join(self.root, "sub")))
        self.not_available(lambda: srcopen.open_source(self.real))

    def test_relative_and_nul_paths_are_a_bad_request(self):
        for p in ("cube.fits", self.real + "\0x", None):
            with self.assertRaises(srcopen.Refused) as cm:
                srcopen.open_source(p)
            self.assertEqual(cm.exception.code, "bad_request")

    def test_a_regular_file_check_survives_a_file_replaced_by_a_directory(self):
        os.unlink(self.real)
        os.mkdir(self.real)
        self.assertTrue(stat.S_ISDIR(os.stat(self.real).st_mode))
        self.not_available(lambda: srcopen.open_source(self.real))


if __name__ == "__main__":
    unittest.main()
