# Performance

The viewer must stay responsive on cubes far larger than memory (shape.md section 10, Q149, Q164, Q169, Q182). This page says what is measured, how, and what is not yet.

## What `make bench` measures

`tools/bench.py` builds a sparse FITS cube (default 1500 x 1500 x 600, 5 GiB logical; a few channels hold random data, the rest are holes) and drives the scientific worker over its own stdin/stdout protocol, the way the API does. It reports first-call, median and p95 time and the worker's resident memory for: header read (`view`), a plane, a coordinate readout, a small and a box-sized region spectrum. `--check` compares with `tools/bench_budgets.json`; CI runs it with budgets about five times the development-laptop numbers.

Limits of the numbers: a sparse file in the page cache measures access pattern and software overhead, not a disk or a network. No ssh, no 4D cube, no concurrent requests (contention lives in the Go worker pool, not here).

## Reference numbers (development laptop, 2026-11-05)

| operation | before | after |
|---|---|---|
| plane (2.25 MP) | 42 ms | 42 ms (dominated by the percentile statistics) |
| spectrum, 10 x 10 px, 600 channels | 49 ms | 4 ms |
| spectrum, 187 x 187 px, 600 channels | 791 ms | 150 ms |

The gain comes from slicing the memory-mapped file instead of astropy's section reader and from reading channels in blocks with one vectorised mean per block. Files that cannot be mapped still fall back to the section reader.

## Not yet done

Two worker lanes so a heavy operation never blocks scrubbing, a downsampled preview with a full-resolution follow-up, prefetch and a browser cache, a worker memory cap, measurement over real disks and ssh. See `docs/roadmap.md` stage P.

## Preview and prefetch

On a remote host, a plane larger than about a quarter of a megapixel is first requested reduced (`step=N`, at most 64; the worker returns the mean of the finite pixels of each N x N block, NaN where a block has none, and marks `preview` in the header as "not for measurement"). The viewer draws it on the full grid, so pixel indices, readouts and regions keep their meaning, shows "preview, reduced N×" under the picture, and requests the full plane 150 ms after the last channel change. When the full plane arrives, the next and previous channels are requested so the server cache already holds them. Readouts of coordinates always come from the full data (`coord`), never from the preview. Local datasets skip the preview: the reduction still reads the whole plane, so it only helps when the link is the bottleneck.
