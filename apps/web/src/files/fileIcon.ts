// VS Code's Material Icon Theme (MIT), the set most editors users already know. Only the icons named here are bundled.
import file from "material-icon-theme/icons/file.svg";
import database from "material-icon-theme/icons/database.svg";
import python from "material-icon-theme/icons/python.svg";
import julia from "material-icon-theme/icons/julia.svg";
import toml from "material-icon-theme/icons/toml.svg";
import yaml from "material-icon-theme/icons/yaml.svg";
import json from "material-icon-theme/icons/json.svg";
import markdown from "material-icon-theme/icons/markdown.svg";
import log from "material-icon-theme/icons/log.svg";
import console from "material-icon-theme/icons/console.svg";
import jupyter from "material-icon-theme/icons/jupyter.svg";
import image from "material-icon-theme/icons/image.svg";
import pdf from "material-icon-theme/icons/pdf.svg";
import tex from "material-icon-theme/icons/tex.svg";
import zip from "material-icon-theme/icons/zip.svg";
import document from "material-icon-theme/icons/document.svg";
import license from "material-icon-theme/icons/license.svg";
import readme from "material-icon-theme/icons/readme.svg";
import settings from "material-icon-theme/icons/settings.svg";
import lock from "material-icon-theme/icons/lock.svg";
import git from "material-icon-theme/icons/git.svg";
import folder from "material-icon-theme/icons/folder-base.svg";
import folderOpen from "material-icon-theme/icons/folder-base-open.svg";
import folderData from "material-icon-theme/icons/folder-database.svg";
import folderDataOpen from "material-icon-theme/icons/folder-database-open.svg";
import folderConfig from "material-icon-theme/icons/folder-config.svg";
import folderConfigOpen from "material-icon-theme/icons/folder-config-open.svg";
import folderJob from "material-icon-theme/icons/folder-job.svg";
import folderJobOpen from "material-icon-theme/icons/folder-job-open.svg";
import folderScripts from "material-icon-theme/icons/folder-scripts.svg";
import folderScriptsOpen from "material-icon-theme/icons/folder-scripts-open.svg";
import folderDocs from "material-icon-theme/icons/folder-docs.svg";
import folderDocsOpen from "material-icon-theme/icons/folder-docs-open.svg";
import folderLog from "material-icon-theme/icons/folder-log.svg";
import folderLogOpen from "material-icon-theme/icons/folder-log-open.svg";
import folderExport from "material-icon-theme/icons/folder-export.svg";
import folderExportOpen from "material-icon-theme/icons/folder-export-open.svg";
import folderImages from "material-icon-theme/icons/folder-images.svg";
import folderImagesOpen from "material-icon-theme/icons/folder-images-open.svg";
import folderSrc from "material-icon-theme/icons/folder-src.svg";
import folderSrcOpen from "material-icon-theme/icons/folder-src-open.svg";
import folderTest from "material-icon-theme/icons/folder-test.svg";
import folderTestOpen from "material-icon-theme/icons/folder-test-open.svg";
import folderTemp from "material-icon-theme/icons/folder-temp.svg";
import folderTempOpen from "material-icon-theme/icons/folder-temp-open.svg";

const BY_EXT: Record<string, string> = {
  fits: database, fit: database, fts: database, h5: database, hdf5: database, parquet: database, db: database, sqlite: database,
  py: python, jl: julia, toml, yaml, yml: yaml, json, md: markdown, markdown, log, out: log, err: log,
  sh: console, bash: console, zsh: console, slurm: console, sbatch: console, ipynb: jupyter,
  png: image, jpg: image, jpeg: image, gif: image, svg: image, webp: image, tif: image, tiff: image,
  pdf, tex, bib: tex, zip, gz: zip, tgz: zip, tar: zip, xz: zip, csv: document, txt: document, cfg: settings, ini: settings, lock,
};
const BY_NAME: Record<string, string> = { license: license, "license.md": license, readme: readme, "readme.md": readme, ".gitignore": git, ".gitattributes": git, "manifest.toml": lock, "project.toml": settings };

type Pair = [string, string];
const FOLDERS: Record<string, Pair> = {
  data: [folderData, folderDataOpen], raw: [folderData, folderDataOpen], interim: [folderData, folderDataOpen], processed: [folderData, folderDataOpen], external: [folderData, folderDataOpen],
  config: [folderConfig, folderConfigOpen], configs: [folderConfig, folderConfigOpen],
  hpc: [folderJob, folderJobOpen], slurm: [folderJob, folderJobOpen], jobs: [folderJob, folderJobOpen],
  scripts: [folderScripts, folderScriptsOpen], docs: [folderDocs, folderDocsOpen], doc: [folderDocs, folderDocsOpen],
  logs: [folderLog, folderLogOpen], log: [folderLog, folderLogOpen],
  results: [folderExport, folderExportOpen], outputs: [folderExport, folderExportOpen], output: [folderExport, folderExportOpen], runs: [folderExport, folderExportOpen],
  figures: [folderImages, folderImagesOpen], plots: [folderImages, folderImagesOpen], images: [folderImages, folderImagesOpen],
  src: [folderSrc, folderSrcOpen], lib: [folderSrc, folderSrcOpen], test: [folderTest, folderTestOpen], tests: [folderTest, folderTestOpen],
  tmp: [folderTemp, folderTempOpen], temp: [folderTemp, folderTempOpen],
};

/** Icon file URL for one tree entry, by the conventions VS Code's theme uses (folder names and extensions only; nothing is read). */
export function iconFor(name: string, kind: "dir" | "file" | "link", open = false): string {
  const lower = name.toLowerCase();
  if (kind === "dir") return (FOLDERS[lower] ?? [folder, folderOpen])[open ? 1 : 0];
  if (BY_NAME[lower]) return BY_NAME[lower];
  const dot = lower.lastIndexOf(".");
  return (dot > 0 ? BY_EXT[lower.slice(dot + 1)] : undefined) ?? file;
}

export const isFits = (name: string) => /\.(fits?|fts)$/i.test(name);
