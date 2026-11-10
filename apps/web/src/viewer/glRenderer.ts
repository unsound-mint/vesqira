import type { RenderOptions } from "./render";

/**
 * WebGL2 plane renderer (ADR 0034). It draws exactly what `renderPlane` draws, pixel for pixel on the canvas grid:
 * nearest-neighbour sampling with the same integer mapping, north up, the same stripe pattern for "no value", the
 * same 256-step colormap index, and the same axial modulo. Stretch, range and colormap are uniforms, so changing them
 * does not touch the pixels on the CPU or upload the plane again.
 *
 * The plane is uploaded as raw 32-bit words (R32UI), not as a float texture: "not finite" is then decided from the
 * exponent bits, which is exact on every driver. `isnan` in a shader is allowed to be optimised away, and a NaN must
 * never reach a colormap entry. Zero is an ordinary value.
 */

const VERTEX = `#version 300 es
void main() {
  vec2 p = vec2(float((gl_VertexID << 1) & 2), float(gl_VertexID & 2));
  gl_Position = vec4(p * 2.0 - 1.0, 0.0, 1.0);
}`;

const FRAGMENT = `#version 300 es
precision highp float;
precision highp int;
precision highp usampler2D;
precision highp sampler2D;
uniform usampler2D uPlane;
uniform sampler2D uLut;
uniform ivec2 uSrc;
uniform ivec2 uOut;
uniform vec2 uRange;
uniform int uStretch;   // 0 linear, 1 square root, 2 inverse hyperbolic sine
uniform int uMode;      // 0 scalar with a range, 1 axial orientation, 2 no range: everything is "no value"
uniform float uToDeg;
uniform vec3 uNodataA;
uniform vec3 uNodataB;
out vec4 outColor;

float asinhf(float x) { return log(x + sqrt(x * x + 1.0)); }

void main() {
  ivec2 p = ivec2(gl_FragCoord.xy);
  int ox = p.x;
  int oy = uOut.y - 1 - p.y;
  int sx = (ox * uSrc.x) / uOut.x;
  int sy = uSrc.y - 1 - (oy * uSrc.y) / uOut.y;
  uint bits = texelFetch(uPlane, ivec2(sx, sy), 0).r;
  bool finite = (bits & 0x7f800000u) != 0x7f800000u;
  if (finite && uMode != 2) {
    float v = uintBitsToFloat(bits);
    float t;
    if (uMode == 1) {
      t = mod(v * uToDeg, 180.0) / 180.0;
    } else if (uRange.y <= uRange.x) {
      t = 0.5;
    } else {
      t = clamp((v - uRange.x) / (uRange.y - uRange.x), 0.0, 1.0);
      if (uStretch == 1) t = sqrt(t);
      else if (uStretch == 2) t = asinhf(t * 10.0) / asinhf(10.0);
    }
    int i = clamp(int(floor(t * 255.0 + 0.5)), 0, 255);
    outColor = vec4(texelFetch(uLut, ivec2(i, 0), 0).rgb, 1.0);
  } else {
    bool b = (((ox + oy) / 3) & 1) == 1;
    outColor = vec4(b ? uNodataB : uNodataA, 1.0);
  }
}`;

const STRETCH = { linear: 0, sqrt: 1, asinh: 2 } as const;

/** Whether this browser can run the renderer at all (WebGL2). A stored choice "cpu" turns it off, for support and tests. */
let probed: boolean | null = null;
let limits = { tex: 0, vw: 0, vh: 0 };
export function gpuSupported(): boolean {
  try {
    if (globalThis.localStorage?.getItem("vesqira.renderer") === "cpu") return false;
  } catch { /* storage can be blocked */ }
  if (probed === null) {
    try {
      const gl = document.createElement("canvas").getContext("webgl2");
      if (gl) {
        const [vw, vh] = gl.getParameter(gl.MAX_VIEWPORT_DIMS) as Int32Array;
        limits = { tex: gl.getParameter(gl.MAX_TEXTURE_SIZE) as number, vw: vw!, vh: vh! };
      }
      probed = !!gl && limits.tex >= 1024;
      gl?.getExtension("WEBGL_lose_context")?.loseContext();
    } catch {
      probed = false;
    }
  }
  return probed;
}

export class GlPlaneRenderer {
  readonly canvas: HTMLCanvasElement;
  private gl: WebGL2RenderingContext;
  private program: WebGLProgram;
  private plane: WebGLTexture;
  private lutTex: WebGLTexture;
  private uploaded: Float32Array | null = null;
  private lutSource: Uint8ClampedArray | null = null;
  private u: Record<string, WebGLUniformLocation | null> = {};
  private onLost: () => void;
  private lostHandler = (e: Event) => { e.preventDefault(); this.onLost(); };

  private constructor(canvas: HTMLCanvasElement, gl: WebGL2RenderingContext, program: WebGLProgram, onLost: () => void) {
    this.canvas = canvas;
    this.gl = gl;
    this.program = program;
    this.onLost = onLost;
    this.plane = gl.createTexture()!;
    this.lutTex = gl.createTexture()!;
    for (const name of ["uPlane", "uLut", "uSrc", "uOut", "uRange", "uStretch", "uMode", "uToDeg", "uNodataA", "uNodataB"]) this.u[name] = gl.getUniformLocation(program, name);
    canvas.addEventListener("webglcontextlost", this.lostHandler);
  }

  /** null when WebGL2 or the shader is unavailable; the caller then uses the CPU renderer on a fresh canvas. */
  static create(canvas: HTMLCanvasElement, onLost: () => void): GlPlaneRenderer | null {
    // preserveDrawingBuffer: a saved figure and a test read the canvas after it was drawn.
    const gl = canvas.getContext("webgl2", { alpha: false, antialias: false, depth: false, stencil: false, preserveDrawingBuffer: true });
    if (!gl) return null;
    const program = gl.createProgram()!;
    for (const [type, src] of [[gl.VERTEX_SHADER, VERTEX], [gl.FRAGMENT_SHADER, FRAGMENT]] as const) {
      const sh = gl.createShader(type)!;
      gl.shaderSource(sh, src);
      gl.compileShader(sh);
      if (!gl.getShaderParameter(sh, gl.COMPILE_STATUS)) return null;
      gl.attachShader(program, sh);
    }
    gl.linkProgram(program);
    if (!gl.getProgramParameter(program, gl.LINK_STATUS)) return null;
    return new GlPlaneRenderer(canvas, gl, program, onLost);
  }

  /** Whether a plane of w by h shown on an outW by outH canvas fits the device limits. */
  static fits(w: number, h: number, outW: number, outH: number): boolean {
    return gpuSupported() && w <= limits.tex && h <= limits.tex && outW <= limits.vw && outH <= limits.vh;
  }

  draw(data: Float32Array, w: number, h: number, o: RenderOptions): void {
    const gl = this.gl;
    if (gl.isContextLost()) return this.onLost();
    const c = this.canvas;
    if (c.width !== o.outW || c.height !== o.outH) { c.width = o.outW; c.height = o.outH; }
    gl.viewport(0, 0, o.outW, o.outH);
    gl.useProgram(this.program);
    if (this.uploaded !== data) {
      gl.activeTexture(gl.TEXTURE0);
      gl.bindTexture(gl.TEXTURE_2D, this.plane);
      gl.pixelStorei(gl.UNPACK_ALIGNMENT, 4);
      gl.texImage2D(gl.TEXTURE_2D, 0, gl.R32UI, w, h, 0, gl.RED_INTEGER, gl.UNSIGNED_INT, new Uint32Array(data.buffer, data.byteOffset, w * h));
      for (const p of [gl.TEXTURE_MIN_FILTER, gl.TEXTURE_MAG_FILTER]) gl.texParameteri(gl.TEXTURE_2D, p, gl.NEAREST);
      this.uploaded = data;
    }
    if (this.lutSource !== o.lut) {
      const rgba = new Uint8Array(256 * 4);
      for (let i = 0; i < 256; i++) rgba.set([o.lut[i * 3]!, o.lut[i * 3 + 1]!, o.lut[i * 3 + 2]!, 255], i * 4);
      gl.activeTexture(gl.TEXTURE1);
      gl.bindTexture(gl.TEXTURE_2D, this.lutTex);
      gl.pixelStorei(gl.UNPACK_ALIGNMENT, 1);
      gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA8, 256, 1, 0, gl.RGBA, gl.UNSIGNED_BYTE, rgba);
      for (const p of [gl.TEXTURE_MIN_FILTER, gl.TEXTURE_MAG_FILTER]) gl.texParameteri(gl.TEXTURE_2D, p, gl.NEAREST);
      this.lutSource = o.lut;
    }
    const u = this.u;
    gl.uniform1i(u.uPlane!, 0);
    gl.uniform1i(u.uLut!, 1);
    gl.uniform2i(u.uSrc!, w, h);
    gl.uniform2i(u.uOut!, o.outW, o.outH);
    gl.uniform2f(u.uRange!, o.range?.[0] ?? 0, o.range?.[1] ?? 0);
    gl.uniform1i(u.uStretch!, STRETCH[o.stretch]);
    gl.uniform1i(u.uMode!, o.axial ? 1 : o.range ? 0 : 2);
    gl.uniform1f(u.uToDeg!, o.axial?.toDegrees ?? 1);
    gl.uniform3f(u.uNodataA!, o.nodata[0][0] / 255, o.nodata[0][1] / 255, o.nodata[0][2] / 255);
    gl.uniform3f(u.uNodataB!, o.nodata[1][0] / 255, o.nodata[1][1] / 255, o.nodata[1][2] / 255);
    gl.drawArrays(gl.TRIANGLES, 0, 3);
  }

  dispose(): void {
    this.canvas.removeEventListener("webglcontextlost", this.lostHandler);
    const gl = this.gl;
    if (gl.isContextLost()) return;
    gl.deleteTexture(this.plane);
    gl.deleteTexture(this.lutTex);
    gl.deleteProgram(this.program);
    this.uploaded = null;
  }
}
