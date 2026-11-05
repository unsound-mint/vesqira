import { useEffect, useId, useMemo, useRef, useState } from "react";
import type { ChangeEvent, KeyboardEvent } from "react";
import { X } from "lucide-react";
import { Button } from "./Button";
import { Icon } from "./Icon";
import c from "./Controls.module.css";
import s from "./FilterBox.module.css";

/** One key the list can be asked about, with what occurs for it. Built from the data by the caller. */
export type VocabKey = {
  key: string;
  kind: "setting" | "result" | "qc" | "unit" | "host" | "nature";
  /** How many items have it. */
  count: number;
  numeric: boolean;
  /** The same value on every item: it narrows nothing, so it is ranked last. */
  same: boolean;
  /** The values that occur, with how many items each (already limited and, for numbers, sorted). */
  values: { value: string; count: number }[];
  range?: { min: number; max: number; n: number };
  /** ":" for keys written `key:value` (no operators). Others are written `key=value`, `key>value`, ... */
  sep?: ":";
  /** Display names for values, e.g. none -> "no unit declared". */
  labels?: Record<string, string>;
};
export type Vocabulary = { keys: VocabKey[]; names: string[] };

const KIND: Record<VocabKey["kind"], string> = { setting: "setting", result: "result", qc: "assessment", unit: "unit", host: "host", nature: "nature" };
const TOKEN = /^([^<>=:\s]+)(<=|>=|=|<|>|:)(.*)$/;
const ENDS_IN_OP = /[<>=:]$/;
const SHOW: Record<string, string> = { "<=": "≤", ">=": "≥" };
const NAMES_SHOWN = 5;

const tokens = (q: string) => q.split(/\s+/).filter(Boolean);
const norm = (t: string) => t.replace(/≥/g, ">=").replace(/≤/g, "<=");

/** What a chip says: `ker = 8`, `noise ≥ 0.3`, `QC: passed`; a plain word is shown as typed. */
function chipLabel(token: string, vocab: Vocabulary): string {
  const m = TOKEN.exec(token);
  if (!m) return token;
  const [, key = "", op = "", value = ""] = m;
  const vk = vocab.keys.find((k) => k.key.toLowerCase() === key.toLowerCase());
  if (op === ":") return `${key === "qc" ? "QC" : key.charAt(0).toUpperCase() + key.slice(1)}: ${vk?.labels?.[value] ?? value}`;
  return `${key} ${SHOW[op] ?? op} ${value}`;
}

type Option = { id: string; kind: "key" | "op" | "value" | "name"; label: string; meta?: string; draft?: string; commit?: string };

/** A draft that is a whole condition or a plain word may narrow the list while it is typed; a half-typed one does not. */
const liveDraft = (d: string) => d.trim() !== "" && !/\s/.test(d) && !ENDS_IN_OP.test(d);

function suggest(draft: string, vocab: Vocabulary, noun: string): { options: Option[]; hint?: string } {
  const d = norm(draft.trim());
  const find = (k: string) => vocab.keys.find((v) => v.key.toLowerCase() === k.toLowerCase());
  const valueOptions = (vk: VocabKey, op: string, prefix: string): Option[] =>
    vk.values
      .filter((v) => v.value.toLowerCase().startsWith(prefix.toLowerCase()) || (vk.labels?.[v.value] ?? "").toLowerCase().startsWith(prefix.toLowerCase()))
      .map((v) => ({
        id: `v:${vk.key}:${v.value}`,
        kind: "value" as const,
        label: vk.labels?.[v.value] ?? v.value,
        meta: `${v.count} ${noun}`,
        commit: vk.sep ? `${vk.key}:${v.value}` : `${vk.key}${op}${v.value}`,
      }));
  const rangeHint = (vk: VocabKey) => (vk.range ? `${vk.range.min} … ${vk.range.max}, ${vk.range.n} value${vk.range.n === 1 ? "" : "s"}` : undefined);

  const m = TOKEN.exec(d);
  if (m) {
    const vk = find(m[1] ?? "");
    const op = m[2] ?? "";
    if (!vk || (vk.sep ? op !== ":" : op === ":")) return { options: [] };
    return { options: valueOptions(vk, op, m[3] ?? ""), hint: vk.numeric && m[3] === "" ? rangeHint(vk) : undefined };
  }
  if (d === "") {
    return { options: vocab.keys.map(keyOption(noun)) };
  }
  const lower = d.toLowerCase();
  const exact = find(d);
  const options: Option[] = [];
  let hint: string | undefined;
  if (exact) {
    if (exact.sep) options.push(...valueOptions(exact, ":", ""));
    else {
      for (const op of exact.numeric ? ["=", "<", ">", "<=", ">="] : ["="]) {
        options.push({ id: `o:${exact.key}:${op}`, kind: "op", label: `${exact.key} ${SHOW[op] ?? op}`, draft: `${exact.key}${op}` });
      }
      hint = rangeHint(exact);
    }
  }
  const keys = vocab.keys.filter((k) => k !== exact && k.key.toLowerCase().includes(lower));
  keys.sort((a, b) => Number(b.key.toLowerCase().startsWith(lower)) - Number(a.key.toLowerCase().startsWith(lower)));
  options.push(...keys.map(keyOption(noun)));
  const names = vocab.names.filter((n) => n.toLowerCase().includes(lower)).slice(0, NAMES_SHOWN);
  options.push(...names.map((n) => ({ id: `n:${n}`, kind: "name" as const, label: n, commit: n })));
  return { options, hint };
}

const keyOption = (noun: string) => (k: VocabKey): Option => ({
  id: `k:${k.key}`,
  kind: "key",
  label: k.key,
  meta: `${KIND[k.kind]} · ${k.count} ${noun}${k.same ? " · same for all" : ""}`,
  draft: k.sep ? `${k.key}:` : k.key,
});

/**
 * A filter box that completes what occurs and shows each condition as a chip. It reads and writes the same query string
 * the matchers understand (`ker=8 noise>=0.3 qc:passed lmc`), so a query typed by hand works too; `≥` and `≤` are only how
 * a chip is shown. Space or Enter turns the typed text into a chip. Arrow keys move in the list, Enter or Tab take the
 * highlighted entry, Escape closes the list, Backspace in an empty box removes the last chip.
 */
export function FilterBox({ label, placeholder, value, onChange, vocabulary, shown, total, noun }: {
  label: string;
  placeholder?: string;
  /** The query: committed conditions as words. */
  value: string;
  onChange: (query: string) => void;
  vocabulary: Vocabulary;
  shown: number;
  total: number;
  /** What the items are called, plural ("runs"). */
  noun: string;
}) {
  const id = useId();
  const listId = `${id}-list`;
  const input = useRef<HTMLInputElement>(null);
  const [chips, setChips] = useState<string[]>(() => tokens(value));
  const [draft, setDraft] = useState("");
  const [open, setOpen] = useState(false);
  const [moved, setMoved] = useState<number | null>(null);
  const sent = useRef(value);

  // A query changed from outside (a reset) replaces the chips; one this box sent does not.
  useEffect(() => {
    if (value !== sent.current) {
      sent.current = value;
      setChips(tokens(value));
      setDraft("");
    }
  }, [value]);

  function update(nextChips: string[], nextDraft: string) {
    setChips(nextChips);
    setDraft(nextDraft);
    setMoved(null);
    const q = [...nextChips, ...(liveDraft(nextDraft) ? [norm(nextDraft.trim())] : [])].join(" ");
    if (q !== sent.current) {
      sent.current = q;
      onChange(q);
    }
  }

  const { options, hint } = useMemo(() => suggest(draft, vocabulary, noun), [draft, vocabulary, noun]);
  // The first completion is ready for Enter or Tab, except a run name: that needs an arrow key, so a plain word stays a word.
  // After an operator, only a value typed in full is taken as is (noise>0.3 must not become 0.37).
  const typedValue = TOKEN.test(draft.trim());
  const defaultActive = draft.trim() === "" ? -1 : typedValue ? options.findIndex((o) => o.commit?.toLowerCase() === norm(draft.trim()).toLowerCase()) : options[0] && options[0].kind !== "name" ? 0 : -1;
  const active = Math.min(moved ?? defaultActive, options.length - 1);
  const showList = open && options.length > 0;

  function commitToken(t: string, rest = chips): string[] {
    const w = norm(t.trim());
    return w === "" || rest.includes(w) ? rest : [...rest, w];
  }

  function accept(o: Option) {
    if (o.commit !== undefined) {
      update(commitToken(o.commit), "");
      setOpen(false);
    } else {
      update(chips, o.draft ?? "");
      setOpen(true);
    }
    input.current?.focus();
  }

  function typed(e: ChangeEvent<HTMLInputElement>) {
    const v = e.target.value;
    setOpen(true);
    if (!/\s/.test(v)) return update(chips, v);
    // Space ends a word: whole conditions become chips, a half-typed one (ker=) stays in the box.
    const parts = v.split(/\s+/);
    const tail = parts.pop() ?? "";
    let next = chips;
    for (const p of parts) {
      if (p === "") continue;
      if (ENDS_IN_OP.test(p)) return update(next, p + tail);
      next = commitToken(p, next);
    }
    update(next, tail);
  }

  function onKey(ev: KeyboardEvent<HTMLInputElement>) {
    if (ev.key === "ArrowDown" || ev.key === "ArrowUp") {
      if (options.length === 0) return;
      ev.preventDefault();
      if (!showList) {
        setOpen(true);
        setMoved(ev.key === "ArrowDown" ? 0 : options.length - 1);
        return;
      }
      const d = ev.key === "ArrowDown" ? 1 : -1;
      setMoved(active < 0 ? (d === 1 ? 0 : options.length - 1) : (active + d + options.length) % options.length);
    } else if (ev.key === "Enter") {
      const o = showList ? options[active] : undefined;
      if (o) {
        ev.preventDefault();
        accept(o);
      } else if (draft.trim() !== "" && !ENDS_IN_OP.test(draft.trim())) {
        ev.preventDefault();
        update(commitToken(draft), "");
        setOpen(false);
      }
    } else if (ev.key === "Tab" && !ev.shiftKey) {
      const o = showList ? options[active] : undefined;
      if (o) {
        ev.preventDefault();
        accept(o);
      }
    } else if (ev.key === "Escape") {
      if (showList) {
        ev.preventDefault();
        ev.stopPropagation();
        setOpen(false);
      }
    } else if (ev.key === "Backspace" && draft === "" && chips.length > 0) {
      ev.preventDefault();
      update(chips.slice(0, -1), "");
    }
  }

  function edit(i: number) {
    const t = chips[i] ?? "";
    update(chips.filter((_, j) => j !== i), t);
    setOpen(true);
    input.current?.focus();
  }

  function remove(i: number) {
    update(chips.filter((_, j) => j !== i), draft);
    input.current?.focus();
  }

  const any = chips.length > 0 || draft !== "";
  return (
    <div className={`${c.field} ${s.wrap}`}>
      <label htmlFor={`${id}-in`} className={c.label}>{label}</label>
      <div className={s.box} onClick={() => input.current?.focus()}>
        {chips.map((t, i) => {
          const text = chipLabel(t, vocabulary);
          return (
            <span key={t} className={s.chip}>
              <button type="button" className={s.chipText} aria-label={`Edit filter ${text}`} onClick={(e) => { e.stopPropagation(); edit(i); }}>{text}</button>
              <button type="button" className={s.chipX} aria-label={`Remove filter ${text}`} onClick={(e) => { e.stopPropagation(); remove(i); }}>
                <Icon icon={X} />
              </button>
            </span>
          );
        })}
        <input
          id={`${id}-in`}
          ref={input}
          className={s.input}
          role="combobox"
          aria-expanded={showList}
          aria-controls={listId}
          aria-autocomplete="list"
          aria-activedescendant={showList && active >= 0 ? `${id}-o${active}` : undefined}
          autoComplete="off"
          spellCheck={false}
          value={draft}
          placeholder={chips.length === 0 ? placeholder : undefined}
          onChange={typed}
          onKeyDown={onKey}
          onBlur={() => setOpen(false)}
        />
        {any ? (
          <span className={s.tail}>
            <span aria-hidden="true" className={s.count}>{shown} of {total}</span>
            <Button type="button" variant="ghost" onMouseDown={(e) => e.preventDefault()} onClick={() => { update([], ""); setOpen(false); input.current?.focus(); }}>Clear</Button>
          </span>
        ) : null}
      </div>
      <ul id={listId} role="listbox" aria-label="Suggestions" className={s.list} hidden={!showList}>
        {hint ? <li role="presentation" className={s.hint}>{hint}</li> : null}
        {options.map((o, i) => (
          <li
            key={o.id}
            id={`${id}-o${i}`}
            role="option"
            aria-selected={i === active}
            className={s.option}
            onMouseDown={(ev) => ev.preventDefault()} // keep the focus in the input
            onClick={() => accept(o)}
          >
            <span className={s.optLabel}>{o.label}</span>
            {o.meta ? <span className={s.meta}>{o.meta}</span> : null}
          </li>
        ))}
      </ul>
      <span role="status" className={s.live}>{`${shown} of ${total} ${noun} match`}</span>
    </div>
  );
}
