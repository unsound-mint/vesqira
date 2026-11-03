# Science fixtures

Synthetic, deterministic astronomy inputs with independently known answers. Not real data. Files are generated on demand (`python3 fixtures/generate.py [dir]`) and are never committed.

- `hi_cube`: 32x24x64 cube, TAN celestial WCS, linear VRAD axis in m/s, a source at a known pixel and channel, a NaN block (missing) and two flagged channels in a separate `FLAGS` table (flagged data is still finite).
- Variants in another frame and with coarser pixels (incompatible with the base cube), a VGT result file (`ANGLE`, `RELIABILITY`).
- Variants without a spectral reference and without celestial WCS: unknown must stay unknown.
- VGT-style axial angle map with reliability, including the 1°/179° case, a degenerate pair and zero reliability that is not missing.
- Oracles: an independent TAN projection and a doubled-angle axial mean, used to check implementations later.

```sh
pip install -r fixtures/requirements.txt
make fixtures
```
