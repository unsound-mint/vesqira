# Demo walk-through

A complete path from registering a cube to a supervisor's comment, on generated synthetic fixtures. Nothing here is real data, and no data file is committed: `tools/demo.sh` generates the files on demand.

## Run it

```sh
pip install -r fixtures/requirements.txt     # numpy and astropy
(cd apps/web && npm ci)
tools/demo.sh
```

Open <http://127.0.0.1:5173>. The script prints the folder holding the generated files.

## The walk-through

1. **Register** `hi_cube.fits` (type its full path in the explorer). The inspector shows what the header *declares*; nothing is verified.
2. **Read a plane.** Focus the channel slider: arrows step one channel, Page Up and Page Down step ten, Home and End jump to the ends. At channel 41 the synthetic source is at pixel 21, 10: hover it for value, position and velocity. Hover the hatched 3 by 3 block: it says "no value" (NaN), never zero. Channels 11 and 12 are flagged by the file's `FLAGS` extension: the data are kept and the status says "flagged by provider".
3. **Draw a region** (Region section: Circle, then drag). Its spectrum peaks at channel 41; the panel states the pixel count, the area from the WCS and the mask policy.
4. **Orientation maps.** Register `vgt_psi_deg.fits` and `vgt_reliability.fits`. Open the psi map, choose *Axial orientation*, choose the unit (degrees: required, never guessed) and the reliability map. Set a rectangle x 1 to 2, y 1 to 1 (pixels of 1° and 179°): the mean orientation is 0°, not 90°. Set y 4 to 4 (0° and 90°): the mean is *undefined* because the orientations cancel.
5. **Compare.** Compare the psi map with the reliability map: same grid, cursor and channel linked. Compare `hi_cube.fits` with `hi_cube_coarser.fits`: not linked, with the reasons, and nothing is resampled.
6. **Figure and board.** In *Figure*, save the picture with a title and add it to a new board. In *Boards* (rail), add a note, then *Freeze and create link*.
7. **Review.** Open the link (it points at the reviewer address) in another browser profile. The reviewer sees the frozen pictures, notes and provenance and can comment; the owner API does not exist on that address. Read the comment in *Boards*, then *Revoke link*: the link stops working.

## Automated version

`make e2e` runs this same path with Playwright against the real API, the real worker and the built app (including axe checks for dark and light themes and a phone width). It generates its own fixtures and needs Chromium (`cd e2e && npx playwright install chromium`).
