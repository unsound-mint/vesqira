import s from "./PathText.module.css";

/** A path cut in two at the last `keep` segments: the head may be shortened, the tail (the file and its folder) never is. */
export function splitPath(path: string, keep = 2): { head: string; tail: string } {
  const parts = path.split("/");
  if (parts.length <= keep + 1) return { head: "", tail: path };
  const at = parts.length - keep;
  return { head: parts.slice(0, at).join("/"), tail: `/${parts.slice(at).join("/")}` };
}

/**
 * A path on one line that shortens in the middle when the room is short ("/home/spectre/…/configs/vgt.toml"), so the end,
 * which names the file, always shows. The full path is the tooltip and the accessible text, so nothing is lost to the eye
 * or to a screen reader. Pure CSS: the head takes the leftover width and ends in an ellipsis; the tail does not shrink.
 */
export function PathText({ path, keep = 2, className }: { path: string; keep?: number; className?: string }) {
  const { head, tail } = splitPath(path, keep);
  return (
    <span className={`${s.path} ${className ?? ""}`} title={path}>
      <span className={s.head} aria-hidden="true">{head}</span>
      <span className={s.tail} aria-hidden="true">{tail}</span>
      <span className={s.full}>{path}</span>
    </span>
  );
}
