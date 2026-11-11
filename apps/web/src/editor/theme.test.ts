import { appIsLight, monacoColors } from "./theme";

const root = (theme: string | null) => ({ getAttribute: () => theme });

describe("editor theme follows the app theme", () => {
  it("an explicit theme wins over the system preference, either way", () => {
    expect(appIsLight(root("dark"), true)).toBe(false);
    expect(appIsLight(root("light"), false)).toBe(true);
  });
  it("without an explicit theme the system preference decides", () => {
    expect(appIsLight(root(null), true)).toBe(true);
    expect(appIsLight(root(null), false)).toBe(false);
  });
});

describe("editor colours from the design tokens", () => {
  const dark: Record<string, string> = { "--bg-1": "#0f151c", "--text": "#e7ecf2", "--accent": "#3cc4e8", "--line": "#222d39", "--accent-bg": "rgba(60,196,232,.14)" };
  it("uses the surface and text tokens, and a translucent accent for the selection", () => {
    const c = monacoColors((n) => dark[n] ?? "");
    expect(c["editor.background"]).toBe("#0f151c");
    expect(c["editor.foreground"]).toBe("#e7ecf2");
    expect(c["editor.selectionBackground"]).toBe("#3cc4e840");
  });
  it("leaves out a colour whose token is not a plain #rrggbb, so Monaco keeps its own", () => {
    const c = monacoColors((n) => (n === "--bg-1" ? "rgba(0,0,0,.5)" : dark[n] ?? ""));
    expect("editor.background" in c).toBe(false);
  });
  it("gives a light surface for the light tokens", () => {
    expect(monacoColors((n) => (n === "--bg-1" ? "#f3f6f9" : ""))["editor.background"]).toBe("#f3f6f9");
  });
});
