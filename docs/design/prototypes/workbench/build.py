#!/usr/bin/env python3
"""Inline the five sample planes and the mock spectrum into workbench.template.html.
Output: workbench.html (self-contained, synthetic data). Run: python3 build.py"""
import base64, json
from pathlib import Path

here = Path(__file__).parent
page = (here / "workbench.template.html").read_text()
for k in ["-12", "-4", "+0", "+4", "+12"]:
    data = base64.b64encode((here / "planes" / f"c{k}.webp").read_bytes()).decode()
    page = page.replace(f"__C{k}__", "data:image/webp;base64," + data)
spec = json.loads((here.parent / "_shared" / "assets" / "spectrum.json").read_text())
page = page.replace("__SPEC__", json.dumps({"v": spec["velocity_kms"], "t": spec["T_K"]}))
(here / "workbench.html").write_text(page)
print("wrote workbench.html", len(page) // 1024, "KB")
