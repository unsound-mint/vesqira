import type { JobTemplate } from "../api/jobs";

/** Templates as GET /api/job-templates returns them, trimmed to what the job form reads. Test data, not a copy of the shipped profiles. */
const base = {
  about: "Runs the tool on a copy of a config from the host.",
  repo: { url: "https://example.org/x/tool", commit: "0123456789abcdef0123456789abcdef01234567", folderLabel: "Tool folder on the host", placeholder: "/shared/home/you/tool", hint: "The checkout of the tool.", refusal: "plain path", verified: "Nothing was run." },
  config: { var: "cfg", inputKey: "input_file", edit: "section" as const, required: false, label: "Base config on the host", placeholder: "/shared/home/you/tool/run.toml" },
  ties: [],
  steps: [
    { kind: "set" as const, name: "threads", value: "{{cpus}}" },
    { kind: "export" as const, name: "OMP_NUM_THREADS", value: "{{var:threads}}" },
    { kind: "require" as const, command: "python3", title: "Python" },
  ],
  command: ["srun", "--ntasks=1", "python3", "tool.py", "--config", "{{config}}"],
  resources: { cpus: 4, mem: "8G", time: "02:00:00", reason: "A starting request for a new job, not a measurement of this tool." },
  probes: [],
};

export const synthTemplate: JobTemplate = {
  ...base,
  profile: "SynthFit@1",
  id: "synthfit",
  title: "SynthFit run",
  tool: "SynthFit",
  keys: [
    { name: "input_cube", section: "input", kind: "path", label: "Input cube on the host", description: "The cube the run reads.", expect: "" },
    { name: "output_dir", section: "output", kind: "path", label: "Output folder", description: "Set by Vesqira.", expect: "", internal: true },
    { name: "n_bins", section: "fit", kind: "int", label: "n_bins", description: "Number of histogram bins.", expect: "a whole number from 2 to 64", min: 2, max: 64, default: { value: "16", reason: "invented" } },
    { name: "threshold", section: "fit", kind: "number", label: "threshold", description: "Detection threshold in units of the noise.", expect: "", min: 0, max: 10 },
    { name: "mode", section: "fit", kind: "enum", label: "mode", description: "fast or exact.", expect: "", enum: ["fast", "exact"] },
    { name: "smooth", section: "fit", kind: "bool", label: "smooth", description: "Smooth the result.", expect: "" },
  ],
};

export const vgtTemplate: JobTemplate = {
  ...base,
  profile: "VGT.jl@1",
  id: "vgt",
  title: "VGT run",
  tool: "VGT",
  sweepKey: "dn",
  resources: { ...base.resources, cpus: 32, mem: "64G", time: "1-00:00:00" },
  repo: { ...base.repo, folderLabel: "VGT folder on the host", placeholder: "/shared/home/you/vgt" },
  keys: [
    { name: "input_file", section: "input", kind: "path", label: "Input cube on the host", description: "The FITS cube the run reads.", expect: "" },
    { name: "ker", section: "vgt", kind: "int", label: "ker (smoothing, pixels)", description: "Gaussian smoothing sigma.", expect: "" },
    { name: "dn", section: "vgt", kind: "int", label: "dn (window, pixels)", description: "Window size.", expect: "" },
    { name: "min_valid", section: "vgt", kind: "int", label: "min_valid", description: "Fewest valid samples in a window.", expect: "" },
    { name: "angle_frame", section: "output", kind: "enum", label: "angle frame (PSIFRAME of the outputs)", description: "The frame the angles are written in.", expect: "", enum: ["pixel", "galactic", "icrs"] },
    { name: "output_bin", section: "output", kind: "int", label: "output binning (pixels, 1 or more)", description: "Bin the outputs.", expect: "", min: 1 },
    { name: "noise_mode", section: "noise", kind: "enum", label: "noise mode", description: "How the noise threshold is modelled.", expect: "", enum: ["scalar", "channel", "spatial", "separable", "auto"] },
    { name: "output_dir", section: "input", kind: "path", label: "Output folder", description: "Set by Vesqira.", expect: "", internal: true },
  ],
};

export const rohsaTemplate: JobTemplate = {
  ...base,
  profile: "ROHSA-LMC@1",
  id: "rohsa",
  title: "ROHSA decomposition",
  tool: "ROHSA",
  sweepKey: "n_gauss",
  resources: { ...base.resources, cpus: 32, mem: "64G", time: "1-00:00:00" },
  about: "Runs the ROHSA build-and-decompose script on a copy of a ROHSA config from the host. The output folder is set for each job.",
  repo: { ...base.repo, folderLabel: "ROHSA-LMC folder on the host", placeholder: "/shared/home/you/ROHSA-LMC" },
  config: { ...base.config, edit: "by_name", required: true, inputKey: "input", label: "ROHSA config on the host" },
  sizing: "rohsa_crop_table",
  ties: [{ name: "lambda", label: "lambda (amp, mu and sig together)", keys: ["lambda_amp", "lambda_mu", "lambda_sig"], reason: "The owner's smoothNNN configs move the three weights together." }],
  keys: [
    { name: "n_gauss", section: "rohsa", kind: "int", label: "n_gauss (Gaussians per pixel)", description: "Number of Gaussian components fitted to each spectrum.", expect: "", min: 1, max: 64, list: true },
    { name: "lambda_amp", section: "rohsa", kind: "number", label: "lambda_amp (smoothing, amplitude)", description: "Smoothness weight of the amplitude.", expect: "", real: true },
    { name: "lb_sig", section: "rohsa", kind: "number", label: "lb_sig (width lower bound, channels)", description: "Lower bound of the width.", expect: "", real: true },
    { name: "maxiter", section: "rohsa", kind: "int", label: "maxiter (iteration limit)", description: "Iteration limit.", expect: "", min: 1 },
    { name: "noise", section: "rohsa", kind: "bool", label: "noise (use a noise estimate)", description: "Whether the fit uses a noise estimate.", expect: "" },
    { name: "spectral_kernel", section: "input", kind: "enum", label: "spectral_kernel", description: "Smooths every spectrum before the fit.", expect: "none, hanning or gauss<sigma>", enum: ["none", "hanning"], enumNumeric: ["gauss"] },
    { name: "crop_size", section: "input", kind: "int", label: "crop_size (0 = whole cube)", description: "Side of the square crop in pixels.", expect: "", min: 0, max: 100000 },
    { name: "output_dir", section: "input", kind: "path", label: "Output folder", description: "Set by Vesqira.", expect: "", internal: true },
  ],
};
