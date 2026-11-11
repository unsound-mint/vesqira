import { useEffect, useRef, useState } from "react";
import type { editor as MonacoEditor } from "monaco-editor";
import { appIsLight, monacoColors } from "./theme";
import s from "./Editor.module.css";

/** Monaco's language id for a file name, or plaintext. Only languages with a bundled grammar are listed (no language servers). */
export function languageFor(name: string): string {
  const ext = name.includes(".") ? name.slice(name.lastIndexOf(".") + 1).toLowerCase() : "";
  return { py: "python", jl: "julia", md: "markdown", sh: "shell", slurm: "shell", sbatch: "shell", yaml: "yaml", yml: "yaml", ini: "ini", cfg: "ini", conf: "ini", toml: "ini" }[ext] ?? "plaintext";
}

type Monaco = typeof import("monaco-editor");
let loading: Promise<Monaco> | null = null;

/**
 * Monaco is loaded the first time a file is opened, never with the app: this module's only import of it is dynamic, and
 * the editor worker and the few grammars it needs are separate chunks. ADR 0029 names Monaco for the integrated editor.
 */
function loadMonaco(): Promise<Monaco> {
  loading ??= (async () => {
    const [monaco, worker] = await Promise.all([
      import("monaco-editor/esm/vs/editor/editor.api") as Promise<Monaco>,
      import("monaco-editor/esm/vs/editor/editor.worker?worker"),
      import("monaco-editor/esm/vs/basic-languages/python/python.contribution"),
      import("monaco-editor/esm/vs/basic-languages/julia/julia.contribution"),
      import("monaco-editor/esm/vs/basic-languages/markdown/markdown.contribution"),
      import("monaco-editor/esm/vs/basic-languages/shell/shell.contribution"),
      import("monaco-editor/esm/vs/basic-languages/yaml/yaml.contribution"),
      import("monaco-editor/esm/vs/basic-languages/ini/ini.contribution"),
    ]).then(([m, w]) => [m, w.default] as const);
    (self as unknown as { MonacoEnvironment: unknown }).MonacoEnvironment = { getWorker: () => new (worker as unknown as new () => Worker)() };
    return monaco;
  })();
  loading.catch(() => { loading = null; }); // a failed load may be tried again
  return loading;
}

/** Defines and selects the editor theme from the design tokens in force now; run again when the app theme changes. */
function applyTheme(monaco: Monaco) {
  const cs = getComputedStyle(document.documentElement);
  monaco.editor.defineTheme("vesqira", { base: appIsLight() ? "vs" : "vs-dark", inherit: true, rules: [], colors: monacoColors((name) => cs.getPropertyValue(name)) });
  monaco.editor.setTheme("vesqira");
}

/**
 * The text surface of the editor. `value` is text with "\n" line ends; changes come out through `onChange`. If Monaco
 * cannot be loaded the same contract is served by a plain textarea, so a save is never blocked by the bundle.
 */
export function CodeSurface({ value, onChange, language, label }: { value: string; onChange: (v: string) => void; language: string; label: string }) {
  const host = useRef<HTMLDivElement>(null);
  const editor = useRef<MonacoEditor.IStandaloneCodeEditor | null>(null);
  const emit = useRef(onChange);
  emit.current = onChange;
  const initial = useRef(value);
  const stopTheme = useRef<(() => void) | null>(null);
  const [state, setState] = useState<"loading" | "monaco" | "plain">("loading");

  useEffect(() => {
    let live = true;
    loadMonaco().then(
      (monaco) => {
        if (!live || !host.current) return;
        applyTheme(monaco);
        const e = monaco.editor.create(host.current, {
          value: initial.current,
          language,
          ariaLabel: label,
          theme: "vesqira",
          automaticLayout: true,
          minimap: { enabled: false },
          scrollBeyondLastLine: false,
          fontFamily: "'Geist Mono Variable','Geist Mono',ui-monospace,monospace",
          fontSize: 13,
          renderWhitespace: "selection",
          tabSize: 2,
        });
        e.getModel()?.setEOL(monaco.editor.EndOfLineSequence.LF); // the file's own line ends are put back on save
        e.onDidChangeModelContent(() => emit.current(e.getValue()));
        editor.current = e;
        setState("monaco");
        // the editor follows the app: a data-theme change or a change of the system preference re-themes it live
        const retheme = () => applyTheme(monaco);
        const mq = matchMedia?.("(prefers-color-scheme: light)");
        mq?.addEventListener?.("change", retheme);
        const mo = new MutationObserver(retheme);
        mo.observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
        stopTheme.current = () => { mq?.removeEventListener?.("change", retheme); mo.disconnect(); };
        // focus lands in the text unless the person has already moved to a control of the screen
        const at = document.activeElement;
        if (!at || !(at instanceof HTMLElement) || !at.closest("button, input, select, textarea")) e.focus();
      },
      () => live && setState("plain"),
    );
    return () => {
      live = false;
      stopTheme.current?.();
      stopTheme.current = null;
      editor.current?.dispose();
      editor.current = null;
    };
  }, [language, label]);

  // text replaced from outside (taking the version on disk)
  useEffect(() => {
    const e = editor.current;
    if (e && e.getValue() !== value) e.setValue(value);
  }, [value]);

  if (state === "plain") {
    return <textarea className={s.plain} aria-label={label} value={value} spellCheck={false} onChange={(e) => onChange(e.target.value)} />;
  }
  return (
    <div className={s.surface}>
      {state === "loading" ? <p className={s.loading} role="status">Loading the editor…</p> : null}
      <div ref={host} className={s.monaco} />
    </div>
  );
}
