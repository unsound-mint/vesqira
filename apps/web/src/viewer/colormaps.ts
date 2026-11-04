// Sampled from matplotlib 3.11 colormaps (viridis and inferno, CC0) at 33 stops; interpolated linearly in sRGB.
// Grayscale is the identity ramp. Both ramps are perceptually ordered, so brightness order is data order.
export type Rgb = [number, number, number];

const VIRIDIS: Rgb[] = [[68, 1, 84], [71, 13, 96], [72, 24, 106], [72, 35, 116], [71, 45, 123], [69, 55, 129], [66, 64, 134], [62, 73, 137], [59, 82, 139], [55, 91, 141], [51, 99, 141], [47, 107, 142], [44, 114, 142], [41, 122, 142], [38, 130, 142], [35, 137, 142], [33, 145, 140], [31, 152, 139], [31, 160, 136], [34, 167, 133], [40, 174, 128], [50, 182, 122], [63, 188, 115], [78, 195, 107], [94, 201, 98], [112, 207, 87], [132, 212, 75], [152, 216, 62], [173, 220, 48], [194, 223, 35], [216, 226, 25], [236, 229, 27], [253, 231, 37]];
const INFERNO: Rgb[] = [[0, 0, 4], [4, 3, 18], [11, 7, 36], [21, 11, 55], [33, 12, 74], [47, 10, 91], [61, 9, 101], [74, 12, 107], [87, 16, 110], [100, 21, 110], [113, 25, 110], [125, 30, 109], [138, 34, 106], [151, 39, 102], [163, 44, 97], [176, 49, 91], [188, 55, 84], [199, 62, 76], [210, 70, 68], [219, 80, 59], [228, 90, 49], [235, 102, 40], [241, 115, 29], [246, 128, 19], [249, 142, 9], [251, 157, 7], [252, 172, 17], [251, 188, 33], [249, 203, 53], [245, 219, 76], [242, 234, 105], [243, 246, 138], [252, 255, 164]];


// Cyclic: the ends meet, so an axial angle (0 and 180 degrees are the same orientation) has no seam.
const TWILIGHT: Rgb[] = [[226, 217, 226], [215, 215, 221], [196, 206, 212], [172, 194, 204], [149, 181, 199], [129, 166, 195], [114, 151, 193], [104, 135, 190], [98, 118, 186], [95, 100, 181], [94, 81, 173], [93, 61, 161], [89, 42, 143], [81, 27, 119], [69, 19, 92], [56, 17, 69], [47, 20, 54], [58, 17, 58], [74, 19, 66], [95, 23, 74], [116, 30, 79], [135, 39, 80], [152, 53, 80], [166, 69, 80], [178, 86, 82], [187, 105, 88], [194, 124, 99], [200, 144, 115], [204, 163, 137], [209, 182, 163], [216, 199, 190], [223, 212, 214], [226, 217, 226]];

export type ColormapId = "viridis" | "inferno" | "gray" | "twilight";

export const COLORMAPS: { id: ColormapId; label: string }[] = [
  { id: "viridis", label: "Viridis" },
  { id: "inferno", label: "Inferno" },
  { id: "gray", label: "Grayscale" },
];

const STOPS: Record<Exclude<ColormapId, "gray">, Rgb[]> = { viridis: VIRIDIS, inferno: INFERNO, twilight: TWILIGHT };

/** 256-entry RGB lookup table (768 bytes) for a colormap. */
export function lut(id: ColormapId): Uint8ClampedArray {
  const out = new Uint8ClampedArray(256 * 3);
  for (let i = 0; i < 256; i++) {
    let rgb: Rgb;
    if (id === "gray") {
      rgb = [i, i, i];
    } else {
      const stops = STOPS[id];
      const t = (i / 255) * (stops.length - 1);
      const a = Math.min(Math.floor(t), stops.length - 2);
      const f = t - a;
      rgb = [0, 1, 2].map((c) => stops[a]![c]! + (stops[a + 1]![c]! - stops[a]![c]!) * f) as Rgb;
    }
    out.set(rgb, i * 3);
  }
  return out;
}
