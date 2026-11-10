#!/usr/bin/env python3
"""Screenshots (webp), console errors and horizontal scroll for reports-c. Usage: python3 capture.py"""
import io, os
from pathlib import Path
from playwright.sync_api import sync_playwright
from PIL import Image

HERE = Path(__file__).resolve().parent
SHOTS = HERE / 'screenshots'
SHOTS.mkdir(exist_ok=True)
URL = lambda page, qs='': 'file://%s/%s%s' % (HERE, page, ('?' + qs) if qs else '')
V = {'1440x900': (1440, 900), '1280x800': (1280, 800), '390x844': (390, 844)}
PAGES = ['01-owner-desk', '02-reader-inbox', '03-reader-report']
JOBS = []
for p in PAGES:
    JOBS += [(p, p, '', '1440x900', 'light'), (p, p, '', '1440x900', 'dark'), (p, p, '', '1280x800', 'light'), (p, p, '', '390x844', 'light')]
for st in ['empty', 'revoked', 'offline']:
    JOBS += [('02-reader-inbox-' + st, '02-reader-inbox', 'state=' + st, '1440x900', 'light'), ('02-reader-inbox-' + st, '02-reader-inbox', 'state=' + st, '390x844', 'light')]
JOBS += [
    ('01-owner-desk-preflight', '01-owner-desk', 'state=preflight', '1440x900', 'light'),
    ('01-owner-desk-preflight', '01-owner-desk', 'state=preflight', '390x844', 'light'),
    ('01-owner-desk-report', '01-owner-desk', 'pane=c', '390x844', 'light'),
    ('03-reader-report-sheet', '03-reader-report', 'sheet=1', '390x844', 'light'),
    ('03-reader-report-changes', '03-reader-report', 'focus=b5', '1440x900', 'light'),
    ('03-reader-report-prev', '03-reader-report', 'focus=b5&prev=1', '1440x900', 'dark'),
]

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
            pg.on('console', lambda m, t=tag: errors.append((t, m.type, m.text)) if m.type in ('error', 'warning') else None)
            pg.on('pageerror', lambda e, t=tag: errors.append((t, 'pageerror', str(e))))
            # Google Fonts is not reachable from the sandbox; that is not a prototype error.
            pg.on('requestfailed', lambda r, t=tag: errors.append((t, 'requestfailed', r.url)) if 'fonts.g' not in r.url else None)
            q = (qs + '&' if qs else '') + 'theme=' + theme
            pg.goto(URL(page + '.html', q))
            pg.wait_for_timeout(300)
            if 'prev=1' in qs:
                pg.click('#tPrev'); pg.wait_for_timeout(100)
                pg.evaluate("document.getElementById('b5').scrollIntoView({block:'start'})")
            sw = pg.evaluate('document.documentElement.scrollWidth'); cw = pg.evaluate('document.documentElement.clientWidth')
            if sw > cw:
                wide = pg.evaluate("""() => { const cw=document.documentElement.clientWidth; return [...document.querySelectorAll('body *')].filter(e=>e.getBoundingClientRect().right>cw+1 && getComputedStyle(e).position!=='fixed').slice(0,4).map(e=>e.tagName+'.'+e.className+' '+Math.round(e.getBoundingClientRect().right)); }""")
                hscroll.append((tag, sw, cw, wide))
            full = page != '03-reader-report' or 'focus' not in qs
            png = pg.screenshot(full_page=full and 'sheet' not in qs and 'state=preflight' not in qs)
            Image.open(io.BytesIO(png)).save(str(SHOTS / (tag + '.webp')), 'WEBP', quality=80)
            n += 1
            ctx.close()
        b.close()
    print('screenshots:', n)
    print('console errors/warnings:', len(errors))
    for e in errors: print('  ', e)
    print('horizontal scroll cases:', len(hscroll))
    for h in hscroll: print('  ', h)

if __name__ == '__main__':
    main()
