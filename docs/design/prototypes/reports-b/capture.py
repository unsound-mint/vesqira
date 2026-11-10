#!/usr/bin/env python3
"""Screenshots (webp), console errors and horizontal-scroll check for reports-b. Usage: python3 capture.py"""
import io, os
from pathlib import Path
from playwright.sync_api import sync_playwright
from PIL import Image

HERE = Path(__file__).resolve().parent
SHOTS = HERE / 'screenshots'
SHOTS.mkdir(exist_ok=True)
V = {'1440x900': (1440, 900), '1280x800': (1280, 800), '390x844': (390, 844)}
PAGES = ['01-owner-editor', '02-reader', '03-graph']
STATES = [('01-owner-editor', 'preflight'), ('02-reader', 'empty'), ('02-reader', 'revoked'), ('02-reader', 'offline')]
EXTRA = [  # name, page, query, viewport, theme
    ('01-owner-editor-popover', '01-owner-editor', 'open=pop', '1440x900', 'light'),
    ('02-reader-comments', '02-reader', 'open=comments', '1440x900', 'light'),
    ('02-reader-sheet', '02-reader', 'open=sheet', '390x844', 'light'),
    ('01-owner-editor-tree', '01-owner-editor', 'drawer=1', '390x844', 'light'),
]
JOBS = []
for p in PAGES:
    JOBS += [(p, p, '', '1440x900', 'light'), (p, p, '', '1440x900', 'dark'), (p, p, '', '1280x800', 'light'), (p, p, '', '390x844', 'light')]
for p, st in STATES:
    JOBS += [(p + '-' + st, p, 'state=' + st, '1440x900', 'light'), (p + '-' + st, p, 'state=' + st, '390x844', 'light')]
JOBS += EXTRA
JOBS += [('02-reader-offline', '02-reader', 'state=offline', '1440x900', 'dark'), ('01-owner-editor-preflight', '01-owner-editor', 'state=preflight', '1440x900', 'dark')]


def launch(p):
    try:
        return p.chromium.launch()
    except Exception as e:
        for path in ('/usr/bin/google-chrome', '/usr/bin/chromium-browser', '/usr/bin/chromium'):
            if os.path.exists(path):
                return p.chromium.launch(executable_path=path)
        raise e


def main():
    errors, hscroll, n = [], [], 0
    with sync_playwright() as p:
        b = launch(p)
        for name, page, qs, vp, theme in JOBS:
            tag = '%s_%s_%s' % (name, vp, theme)
            w, h = V[vp]
            ctx = b.new_context(viewport={'width': w, 'height': h}, reduced_motion='reduce')
            pg = ctx.new_page()
            # Google Fonts is unreachable in the sandbox; the CSS falls back to system fonts.
            pg.route('**/fonts.g*apis.com/**', lambda r: r.abort())
            pg.on('console', lambda m, t=tag: errors.append((t, m.type, m.text)) if m.type in ('error', 'warning') and 'ERR_FAILED' not in m.text else None)
            pg.on('pageerror', lambda e, t=tag: errors.append((t, 'pageerror', str(e))))
            q = (qs + '&' if qs else '') + 'theme=' + theme
            pg.goto('file://%s/%s.html?%s' % (HERE, page, q))
            pg.wait_for_timeout(350)
            sw = pg.evaluate('document.documentElement.scrollWidth'); cw = pg.evaluate('document.documentElement.clientWidth')
            inner = pg.evaluate("""() => [...document.querySelectorAll('.main,.docwrap,.mbar,.gview,dialog[open]')].filter(e => e.scrollWidth > e.clientWidth + 1).map(e => e.className + ' ' + e.scrollWidth + '>' + e.clientWidth)""")
            if sw > cw or inner:
                hscroll.append((tag, sw, cw, inner))
            png = pg.screenshot()
            Image.open(io.BytesIO(png)).save(SHOTS / (tag + '.webp'), quality=82)
            n += 1
            ctx.close()
        b.close()
    print('screenshots:', n)
    print('console errors/warnings:', len(errors))
    for e in errors:
        print('  ', e)
    print('horizontal scroll cases:', len(hscroll))
    for h in hscroll:
        print('  ', h)


if __name__ == '__main__':
    main()
