#!/usr/bin/env python3
"""The HTTP contract of the local API, the review API and the publish server, kept honest by machine.

  tools/contracts.py            rewrite packages/contracts/openapi.yaml from the Go routes
  tools/contracts.py --check    fail if the file is out of date, or if the web client calls a route that does not exist

The routes are read from the registrations in services/api (the single source of truth for what exists): s.route(mux, "METHOD /path",
authz.Permission, handler) on the owner listener, mux.Handle on the capability-token listeners. The file lists every path and method
with its handler, which listener serves it and the permission it needs (x-vesqira-permission, ADR 0050). Request and response bodies are
described one route group at a time in packages/contracts/bodies.json (ADR 0032); the generator merges them in.
Operations without a description are listed in packages/contracts/undescribed.txt, a ratchet: --check fails for an
undescribed operation that is not listed (a new route needs a description) and for a listed one that is now described
(trim the file). Running the generator trims the list; it never adds to it.
"""
import json
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
OUT = ROOT / "packages" / "contracts" / "openapi.yaml"
# Which listener serves the routes registered under each directory. A new Go file there is read automatically.
LISTENERS = [
    ("owner", ROOT / "services/api/internal/server"),
    ("review", ROOT / "services/api/internal/review"),
    ("publish", ROOT / "services/api/internal/publish"),
]
BODIES = ROOT / "packages" / "contracts" / "bodies.json"
UNDESCRIBED = ROOT / "packages" / "contracts" / "undescribed.txt"
# The owner listener declares a permission per route (internal/server/route.go); the other two register bare, because their
# routes are capabilities held in the URL (a review link, a reader link) or a publisher key, and they may not import authz.
OWNER_ROUTE = re.compile(r'\bs\.route\(mux,\s*"([A-Z]+) (/[^"]*)",\s*authz\.(\w+),\s*([A-Za-z_.()]+)')
OTHER_ROUTE = re.compile(r'mux\.Handle(?:Func)?\("([A-Z]+) (/[^"]*)"(?:,\s*([A-Za-z_.()]+))?')
OWNER_REGISTRATION = re.compile(r"\bs\.route\(")
OTHER_REGISTRATION = re.compile(r"\bmux\.Handle(?:Func)?\(")
AUTHZ = ROOT / "services/api/internal/authz/permissions.go"
# The permission of the routes that are not the owner's, by path prefix. A new prefix needs a decision here.
CAPABILITY_PERMISSIONS = [
    ("/review-api/", "capability.review-link"),
    ("/read-api/", "capability.reader-link"),
    ("/publish-api/", "capability.publisher-key"),
]


def permission_names():
    """Go constant name -> permission string, read from internal/authz."""
    return dict(re.findall(r'^\s*(\w+)\s+Permission\s*=\s*"([^"]+)"', AUTHZ.read_text(), re.M))


def other_permission(listener, url):
    if url == "/":
        return "anonymous"
    for prefix, perm in CAPABILITY_PERMISSIONS:
        if url.startswith(prefix):
            return perm
    raise SystemExit(f"contracts: {listener} serves {url}, which has no capability permission: add its prefix to CAPABILITY_PERMISSIONS")


def sources():
    for listener, d in LISTENERS:
        for f in sorted(d.glob("*.go")):
            if not f.name.endswith("_test.go"):
                yield listener, f


def routes():
    found = []
    names = permission_names()
    for listener, path in sources():
        text = path.read_text()
        if listener == "owner":
            for m in OWNER_ROUTE.finditer(text):
                if m.group(3) not in names:
                    raise SystemExit(f"contracts: {path.name} declares authz.{m.group(3)} for {m.group(1)} {m.group(2)}, which internal/authz does not define")
                found.append((m.group(2), m.group(1).lower(), listener, m.group(4).strip("()"), names[m.group(3)]))
        else:
            for m in OTHER_ROUTE.finditer(text):
                method, url, handler = m.group(1), m.group(2), (m.group(3) or "inline").strip("()")
                found.append((url, method.lower(), listener, handler, other_permission(listener, url)))
    return sorted(set(found))


def unread_registrations():
    """Registrations the route pattern cannot read (a computed path, a helper), and any outside LISTENERS.

    A route the contract does not know about is invisible to every check built on it, so this is an error, not a gap."""
    known = {f: listener for listener, f in sources()}
    bad = []
    for f in sorted((ROOT / "services/api").rglob("*.go")):
        if f.name.endswith("_test.go"):
            continue
        text = f.read_text()
        if f not in known:
            if OWNER_REGISTRATION.search(text) or OTHER_REGISTRATION.search(text):
                bad.append(f"{f.relative_to(ROOT)} registers routes but is not under a directory listed in LISTENERS")
            continue
        rel = f.relative_to(ROOT)
        if known[f] == "owner":
            # route.go holds the one bare registration, inside s.route; anywhere else a bare Handle skips the permission
            bare = re.findall(r"\.Handle(?:Func)?\(", text)
            if f.name != "route.go" and bare:
                bad.append(f"{rel} registers a route without declaring a permission; use s.route(mux, \"METHOD /path\", authz.<Permission>, handler)")
            if len(OWNER_REGISTRATION.findall(text)) != len(OWNER_ROUTE.findall(text)) and f.name != "route.go":
                bad.append(f"{rel} has an s.route call the route pattern cannot read; write it as s.route(mux, \"METHOD /path\", authz.<Permission>, handler)")
        else:
            total = len(OTHER_REGISTRATION.findall(text))
            if total != len(OTHER_ROUTE.findall(text)):
                bad.append(f"{rel} has a mux registration the route pattern cannot read; register it as mux.Handle(\"METHOD /path\", handler)")
    return bad


def load_bodies():
    return json.loads(BODIES.read_text())


def scalar(v):
    return json.dumps(v, ensure_ascii=False)


# Plain words YAML reads as null or a boolean, not as a string; such a key is quoted (a property may be named "null").
YAML_WORDS = {"null", "true", "false", "yes", "no", "on", "off", "y", "n", "~"}


def key(k):
    plain = re.fullmatch(r"[A-Za-z_$][A-Za-z0-9_$.\-]*", k) and k.lower() not in YAML_WORDS
    return k if plain else json.dumps(k)


def emit(v, ind):
    """Block YAML for plain JSON data (dicts, lists, scalars); strings are always double-quoted."""
    pad = " " * ind
    out = []
    if isinstance(v, dict):
        for k, x in v.items():
            if isinstance(x, (dict, list)) and x:
                out.append(f"{pad}{key(k)}:")
                out += emit(x, ind + 2)
            else:
                out.append(f"{pad}{key(k)}: " + ("{}" if x == {} else "[]" if x == [] else scalar(x)))
    else:
        for x in v:
            if isinstance(x, dict) and x:
                first, *rest = emit(x, ind + 2)
                out.append(f"{pad}- {first.lstrip()}")
                out += rest
            else:
                out.append(f"{pad}- {scalar(x)}")
    return out


def render(rs):
    bodies = load_bodies()
    ops = bodies["operations"]
    lines = [
        "# Generated by tools/contracts.py from the route registrations in services/api and packages/contracts/bodies.json. Do not edit by hand.",
        "# Scope: every path, method and listener; request and response bodies only for the operations described in bodies.json (ADR 0032).",
        "openapi: 3.0.3",
        "info:",
        "  title: Vesqira local, review and publish API",
        "  version: 0.2.0",
        "  description: The owner API listens on loopback only. The review API serves published previews to anyone holding a link. The publish server (a separate program) serves published reports to named reader links and takes publisher requests with a key.",
        "paths:",
    ]
    by_path = {}
    for url, method, listener, handler, perm in rs:
        by_path.setdefault(url, []).append((method, listener, handler, perm))
    for url in sorted(by_path):
        lines.append(f"  {url}:")
        params = re.findall(r"\{(\w+)\}", url)
        if params:
            lines.append("    parameters:")
            for p in params:
                lines.append(f"      - {{name: {p}, in: path, required: true, schema: {{type: string}}}}")
        # the same method and path can be served by both listeners (the static app); a YAML key may appear only once
        merged = {}
        for method, listener, handler, perm in sorted(by_path[url]):
            merged.setdefault(method, []).append((listener, handler, perm))
        for method, served in merged.items():
            listener, handler, perm = served[0]
            lines.append(f"    {method}:")
            lines.append(f"      operationId: {listener}_{re.sub(r'[^A-Za-z0-9]+', '_', handler).strip('_')}_{method}_{re.sub(r'[^A-Za-z0-9]+', '_', url).strip('_')}")
            lines.append(f"      x-listener: {', '.join(l for l, *_ in served)}")
            lines.append(f"      x-vesqira-permission: {perm}")
            d = ops.get(f"{method.upper()} {url}")
            if d is None:
                lines.append("      responses: {'200': {description: ok}}")
            else:
                lines += emit(d, 6)
    lines += ["components:", "  schemas:"] + emit(bodies["schemas"], 4)
    return "\n".join(lines) + "\n"


def body_errors(rs):
    """bodies.json must name only routes that exist and refer only to schemas it defines."""
    bodies = load_bodies()
    have = {f"{m.upper()} {u}" for u, m, *_ in rs}
    errs = [f"bodies.json describes {k}, which no route serves" for k in bodies["operations"] if k not in have]
    refs = set(re.findall(r"#/components/schemas/(\w+)", json.dumps(bodies)))
    errs += [f"bodies.json refers to the undefined schema {r}" for r in sorted(refs - set(bodies["schemas"]))]
    return errs


def undescribed_now(rs):
    return {f"{m.upper()} {u}" for u, m, *_ in rs} - set(load_bodies()["operations"])


def baseline():
    return [l for l in UNDESCRIBED.read_text().splitlines() if l.strip()] if UNDESCRIBED.exists() else []


def ratchet_errors(rs):
    """The undescribed operations may only shrink: no new one, and the baseline file stays exact."""
    now, base = undescribed_now(rs), set(baseline())
    errs = [f"{k} is a new route without a described body; describe it in packages/contracts/bodies.json (ADR 0032), do not add it to undescribed.txt"
            for k in sorted(now - base)]
    errs += [f"{k} is in undescribed.txt but is now described or gone; remove the line (tools/contracts.py trims the file)" for k in sorted(base - now)]
    if baseline() != sorted(baseline()):
        errs.append("packages/contracts/undescribed.txt is not sorted")
    return errs


def trim_baseline(rs):
    keep = sorted(set(baseline()) & undescribed_now(rs))
    UNDESCRIBED.write_text("".join(k + "\n" for k in keep))


def client_calls():
    """Every /api or /review-api path the web client mentions, with template placeholders normalised."""
    out = set()
    for f in (ROOT / "apps/web/src/api").glob("*.ts"):
        if f.name.endswith(".test.ts"):
            continue
        text = f.read_text()
        for m in re.finditer(r"""[`"'](/(?:api|review-api|read-api)/[^`"'?]*)""", text):
            url = re.sub(r"\$\{[^}]*\}", "{}", m.group(1))
            url = re.sub(r"\$\{.*$", "", url)            # a template that goes on past the path (a query or suffix)
            url = re.sub(r"\.reg$", ".reg", url)
            out.add((f.name, url))
    return sorted(out)


def matches(call, spec_paths):
    parts = call.strip("/").split("/")
    for p in spec_paths:
        sp = p.strip("/").split("/")
        if len(sp) == len(parts) and all(a == b or a.startswith("{") for a, b in zip(sp, parts)):
            return True
    return False


def main():
    rs = routes()
    text = render(rs)
    if "--check" not in sys.argv:
        OUT.parent.mkdir(parents=True, exist_ok=True)
        OUT.write_text(text)
        trim_baseline(rs)
        print(f"wrote {OUT.relative_to(ROOT)} ({len({(u, m) for u, m, *_ in rs})} operations)")
        return 0
    errors = unread_registrations() + body_errors(rs) + ratchet_errors(rs)
    if not OUT.exists() or OUT.read_text() != text:
        errors.append("packages/contracts/openapi.yaml is out of date: run tools/contracts.py")
    spec_paths = {u for u, *_ in rs}
    for fname, url in client_calls():
        # a path cut short at an unfinished template (e.g. `/api/hosts/${id}/list${query}`) must still be a prefix of a route
        if not matches(url, spec_paths) and not any(re.sub(r"\{\w+\}", "{}", sp).startswith(url) for sp in spec_paths):
            errors.append(f"apps/web/src/api/{fname} calls {url}, which no route serves")
    for e in errors:
        print("contracts:", e)
    if not errors:
        print(f"contracts: {len(undescribed_now(rs))} of {len({(u, m) for u, m, *_ in rs})} operations still have no described bodies (only goes down: packages/contracts/undescribed.txt)")
    return 1 if errors else 0


if __name__ == "__main__":
    sys.exit(main())
