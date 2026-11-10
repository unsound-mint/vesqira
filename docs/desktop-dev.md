# Desktop development loop

No RPM and no `dnf` while developing. `tools/dev-desktop.sh` builds the API, web app and Tauri window from the working tree into `.local/dev` (gitignored) and runs them with their own data, config and port (8798), so it can run next to the installed Vesqira. Your real `HOME` and ssh config are used. Incremental rebuild plus start is about 6 seconds; the first run compiles the window (about a minute).

| Command | What it does |
| --- | --- |
| `tools/dev-desktop.sh run` | Build, then open the window on your screen. |
| `tools/dev-desktop.sh up` | Build, then run the real window on a private virtual X screen (`:99`); nothing appears on your screen. |
| `tools/dev-desktop.sh shot FILE.png` | Screenshot of that screen: the real native window and its GTK dialogs. |
| `tools/dev-desktop.sh click X Y`, `key ctrl+l Return`, `type TEXT` | Drive it (`tools/xinput.py`, libX11/XTest through ctypes; no extra packages). |
| `tools/dev-desktop.sh open DIR` | Make DIR a project the way the folder chooser does. |
| `tools/dev-desktop.sh logs`, `down` | Window and API logs; stop everything. |

Notes: the virtual screen has no GPU, so WebKit runs with software compositing, and rendering and timing there say nothing about WebKit performance. The GTK file chooser hides dot-folders (such as `.local`), and its location bar completes inline, so type unambiguous names or click through the sidebar. Web-only changes can still be checked in a browser with Playwright against `http://127.0.0.1:8798/`; use the virtual screen for anything that depends on the native shell (window, chooser, `__TAURI_INTERNALS__`).
