#!/usr/bin/env python3
"""Generate ILLUSTRATIVE, SYNTHETIC mock imagery for Vesqira design prototypes.

Nothing here is real telescope or VGT output. Every image is procedural noise
plus smooth structure, produced with a fixed seed so the three design
directions compare identical content. Do not cite these as scientific results.

Outputs (PNG + JSON) go to ./assets next to this script.
"""
import json
from pathlib import Path

import matplotlib

matplotlib.use("Agg")
import matplotlib.pyplot as plt
import numpy as np
from matplotlib.colors import hsv_to_rgb

OUT = Path(__file__).parent / "assets"
OUT.mkdir(exist_ok=True)
rng = np.random.default_rng(20261103)
N = 512


def smooth_field(sigma, seed):
    r = np.random.default_rng(seed)
    f = np.fft.fft2(r.normal(size=(N, N)))
    kx = np.fft.fftfreq(N)[:, None]
    ky = np.fft.fftfreq(N)[None, :]
    k = np.hypot(kx, ky)
    f *= np.exp(-(k * sigma) ** 2)
    out = np.real(np.fft.ifft2(f))
    return (out - out.mean()) / out.std()


def save_img(arr, name, cmap, vmin=None, vmax=None):
    plt.imsave(OUT / name, arr, cmap=cmap, vmin=vmin, vmax=vmax, origin="lower")


# Synthetic "H I" channel maps: a filamentary structure whose position shifts with velocity.
base = smooth_field(18, 1)
fil = smooth_field(6, 2)
yy, xx = np.mgrid[0:N, 0:N]
channels = {}
for i, v in enumerate([-12.0, -4.0, 0.0, 4.0, 12.0]):
    shift = int(v * 3)
    img = np.exp(0.9 * np.roll(base, shift, axis=1) + 0.35 * np.roll(fil, shift // 2, axis=0))
    img += 0.05 * rng.normal(size=img.shape)
    channels[v] = img
    save_img(img, f"chan_{int(v):+d}kms.png", "magma", np.percentile(img, 2), np.percentile(img, 99.5))

# Moment-0-like integrated map
mom0 = sum(channels.values())
save_img(mom0, "mom0.png", "magma", np.percentile(mom0, 2), np.percentile(mom0, 99.7))

# Synthetic axial orientation maps (degrees in [0,180)); candidate = baseline + smooth perturbation.
theta_base = (90 + 55 * smooth_field(40, 3) + 12 * smooth_field(9, 4)) % 180
theta_cand = (theta_base + 9 * smooth_field(25, 5) + 3 * smooth_field(7, 6)) % 180
reliab = 1 / (1 + np.exp(-(1.4 * smooth_field(30, 7) + 0.6)))  # 0..1 synthetic
valid = reliab > 0.35


def axial_rgb(theta, valid_mask=None, dim_invalid=True):
    # Cyclic colour with period 180 deg so 0 and 180 are identical colour (axial).
    h = (theta % 180) / 180.0
    hsv = np.stack([h, np.full_like(h, 0.75), np.full_like(h, 0.92)], -1)
    rgb = hsv_to_rgb(hsv)
    if valid_mask is not None and dim_invalid:
        grey = np.full_like(rgb, 0.5)
        rgb = np.where(valid_mask[..., None], rgb, 0.55 * rgb + 0.45 * grey)
    return rgb


plt.imsave(OUT / "vgt_baseline_axial.png", axial_rgb(theta_base, valid), origin="lower")
plt.imsave(OUT / "vgt_candidate_axial.png", axial_rgb(theta_cand, valid), origin="lower")

# Axial difference: smallest angle between orientations, in [0, 90].
d = np.abs(theta_cand - theta_base) % 180
d = np.minimum(d, 180 - d)
save_img(np.where(valid, d, np.nan), "vgt_axial_diff.png", "cividis", 0, 30)
save_img(reliab, "vgt_reliability.png", "gray", 0, 1)
mask_rgba = np.zeros((N, N, 4))
mask_rgba[~valid] = [0.05, 0.05, 0.05, 0.55]
plt.imsave(OUT / "vgt_invalid_mask.png", mask_rgba, origin="lower")

# Legend: cyclic axial wheel (0..180 deg).
wheel = np.linspace(0, 180, 361)
plt.imsave(OUT / "axial_legend.png", axial_rgb(np.tile(wheel, (24, 1))))
# Colourbars
grad = np.linspace(0, 1, 256)[None, :].repeat(16, 0)
plt.imsave(OUT / "bar_magma.png", grad, cmap="magma")
plt.imsave(OUT / "bar_cividis.png", grad, cmap="cividis")

# Synthetic region spectrum (K vs km/s), illustrative only.
v = np.linspace(-30, 30, 241)
spec = 18 * np.exp(-0.5 * ((v + 6) / 4.2) ** 2) + 9 * np.exp(-0.5 * ((v - 7) / 5.5) ** 2)
spec += rng.normal(0, 0.55, v.size)
json.dump(
    {"label": "ILLUSTRATIVE SYNTHETIC DATA", "velocity_kms": v.round(2).tolist(), "T_K": spec.round(2).tolist()},
    open(OUT / "spectrum.json", "w"),
)

stats = {
    "label": "ILLUSTRATIVE SYNTHETIC DATA - not a scientific result",
    "valid_fraction": float(valid.mean().round(3)),
    "axial_diff_median_deg": float(np.median(d[valid]).round(2)),
    "axial_diff_p90_deg": float(np.percentile(d[valid], 90).round(2)),
    # circular (axial) mean of angle doubling, demonstrates the 1/179 deg case
    "axial_mean_1_179_deg": float(
        (np.degrees(np.angle(np.exp(2j * np.radians(1)) + np.exp(2j * np.radians(179)))) / 2) % 180
    ),
}
json.dump(stats, open(OUT / "stats.json", "w"), indent=2)
print(json.dumps(stats, indent=2))
