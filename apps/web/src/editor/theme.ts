/** Whether the app is showing its light theme: an explicit data-theme wins, else the system preference (design/tokens.css). */
export function appIsLight(root: { getAttribute(name: string): string | null } = document.documentElement, prefersLight: boolean = !!matchMedia?.("(prefers-color-scheme: light)").matches): boolean {
  const explicit = root.getAttribute("data-theme");
  return explicit === "light" || (explicit !== "dark" && prefersLight);
}

const hex = /^#[0-9a-f]{6}$/i;

/**
 * The editor's colours, taken from the design tokens in force right now (so light and dark come from tokens.css, not from a
 * second palette here). `read` returns a token's value; one that is not a plain #rrggbb falls back to Monaco's own colour.
 * Syntax colours stay Monaco's (`vs` or `vs-dark` as base), tuned for contrast on a surface of this brightness.
 */
export function monacoColors(read: (token: string) => string): Record<string, string> {
  const t = (name: string) => {
    const v = read(name).trim();
    return hex.test(v) ? v : null;
  };
  const accent = t("--accent");
  const entries: [string, string | null][] = [
    ["editor.background", t("--bg-1")],
    ["editor.foreground", t("--text")],
    ["editorLineNumber.foreground", t("--text-3")],
    ["editorLineNumber.activeForeground", t("--text-2")],
    ["editor.lineHighlightBackground", t("--bg-3")],
    ["editor.lineHighlightBorder", t("--bg-3")],
    ["editorCursor.foreground", accent],
    ["editor.selectionBackground", accent ? `${accent}40` : null],
    ["editor.inactiveSelectionBackground", accent ? `${accent}22` : null],
    ["editorIndentGuide.background1", t("--line")],
    ["editorIndentGuide.activeBackground1", t("--line-strong")],
    ["editorWhitespace.foreground", t("--line-strong")],
    ["editorGutter.background", t("--bg-1")],
    ["scrollbarSlider.background", t("--line-strong") ? `${t("--line-strong")}80` : null],
    ["scrollbarSlider.hoverBackground", t("--text-3") ? `${t("--text-3")}80` : null],
    ["focusBorder", t("--focus")],
  ];
  return Object.fromEntries(entries.filter((e): e is [string, string] => e[1] !== null));
}
