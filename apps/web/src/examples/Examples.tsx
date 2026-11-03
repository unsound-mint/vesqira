import { useEffect, useState } from "react";
import { Database, FileText, FolderOpen, Moon, PanelLeft, Settings, Sun } from "lucide-react";
import { Button, Checkbox, IconButton, Input, Pill, ResizeHandle, Section, Select, Slider, Switch, TreeRow } from "../ui";
import s from "./Examples.module.css";

/** Component gallery for the foundation. Synthetic labels only; no viewer behaviour. */
export function Examples() {
  const [dark, setDark] = useState(() => !matchMedia("(prefers-color-scheme: light)").matches);
  const [side, setSide] = useState(280);
  const [sel, setSel] = useState("cube");
  const [ch, setCh] = useState(128);
  useEffect(() => {
    document.documentElement.dataset.theme = dark ? "dark" : "light";
  }, [dark]);

  return (
    <div className={s.page}>
      <header className={s.bar}>
        <h1 className={s.title}>Vesqira components</h1>
        <IconButton icon={dark ? Sun : Moon} label={dark ? "Switch to light theme" : "Switch to dark theme"} onClick={() => setDark(!dark)} />
      </header>
      <div className={s.shell} style={{ gridTemplateColumns: `${side}px 1fr` }}>
        <nav className={s.side} aria-label="Example explorer">
          <TreeRow icon={FolderOpen}>Project</TreeRow>
          <TreeRow icon={Database} level={1} selected={sel === "cube"} onSelect={() => setSel("cube")} trailing="3D">Sample cube</TreeRow>
          <TreeRow icon={FileText} level={1} selected={sel === "map"} onSelect={() => setSel("map")} trailing="2D">Sample map</TreeRow>
          <ResizeHandle orientation="vertical" label="Resize explorer" value={side} min={200} max={480} onChange={setSide} />
        </nav>
        <main className={s.main}>
          <Section title="Buttons">
            <div className={s.cluster}>
              <Button variant="primary">Primary</Button>
              <Button>Default</Button>
              <Button variant="ghost">Ghost</Button>
              <Button icon={Settings}>With icon</Button>
              <Button disabled>Disabled</Button>
              <IconButton icon={PanelLeft} label="Toggle explorer" />
              <IconButton icon={Settings} label="Settings" pressed />
            </div>
          </Section>
          <Section title="Form controls">
            <Checkbox label="Show masked pixels" defaultChecked />
            <Checkbox label="Disabled option" disabled />
            <Switch label="Lock cursor between planes" defaultChecked />
            <Select label="Colormap" defaultValue="viridis">
              <option value="viridis">Viridis</option>
              <option value="gray">Grayscale</option>
            </Select>
            <Input label="Name" placeholder="Untitled view" />
            <Slider label="Channel" valueText={String(ch)} min={0} max={255} step={1} value={ch} onChange={(e) => setCh(Number(e.target.value))} />
          </Section>
          <Section title="Status" meta="text, not colour alone">
            <div className={s.cluster}>
              <Pill tone="ok">Valid</Pill>
              <Pill tone="warn">Flagged</Pill>
              <Pill tone="err">Failed</Pill>
              <Pill tone="accent">Selected</Pill>
              <Pill>Unknown: —</Pill>
            </div>
          </Section>
          <Section title="Collapsed by default" defaultOpen={false}>
            <p className={s.note}>Sections keep their state in the component, ready for saved workspaces later.</p>
          </Section>
        </main>
      </div>
    </div>
  );
}
