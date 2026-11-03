"""Capture screenshots for Direction C and report console errors / horizontal overflow.
Usage: python3 capture.py   (writes ./screenshots/*.png, prints a summary)"""
import os, sys, json
from playwright.sync_api import sync_playwright

HERE = os.path.dirname(os.path.abspath(__file__))
OUT = os.path.join(HERE, 'screenshots')
os.makedirs(OUT, exist_ok=True)
U = lambda p: 'file://' + os.path.join(HERE, p)

VP = {'1440x900': (1440, 900), '1280x800': (1280, 800), '390x844': (390, 844)}
JOBS = []  # (name, url, viewport, theme)
for page in ['01-explorer', '02-viewer', '03-compare', '04-board']:
    JOBS += [(page, page + '.html', '1440x900', 'light'), (page, page + '.html', '1440x900', 'dark'),
             (page, page + '.html', '1280x800', 'light'), (page, page + '.html', '390x844', 'light')]
for st in ['loading', 'error', 'no-wcs', 'no-spectral']:
    JOBS.append(('02-viewer-' + st, '02-viewer.html?state=' + st, '1280x800', 'light'))
JOBS += [('03-compare-blocked', '03-compare.html?state=blocked', '1440x900', 'light'),
         ('03-compare-blocked', '03-compare.html?state=blocked', '390x844', 'light'),
         ('04-board-reviewer', '04-board.html?role=reviewer', '1440x900', 'light'),
         ('04-board-reviewer', '04-board.html?role=reviewer', '390x844', 'light'),
         ('04-board-denied', '04-board.html?role=reviewer&state=denied', '1440x900', 'light'),
         ('01-explorer-runs', '01-explorer.html?view=runs', '1440x900', 'light'),
         ('01-explorer-runs', '01-explorer.html?view=runs', '1440x900', 'dark'),
         ('01-explorer-runs', '01-explorer.html?view=runs', '390x844', 'light'),
         ('01-explorer-empty', '01-explorer.html?state=empty', '1440x900', 'light'),
         ('01-explorer-attention', '01-explorer.html?saved=att', '1440x900', 'light'),
         ('02-viewer-drawer-overlay', '02-viewer.html#overlay', '1280x800', 'light'),
         ('02-viewer-sheet-open', '02-viewer.html#sheet', '390x844', 'light')]

def main():
    summary = {'shots': 0, 'console_errors': [], 'overflow': []}
    with sync_playwright() as p:
        try:
            b = p.chromium.launch()
        except Exception:
            b = p.chromium.launch(executable_path='/usr/bin/google-chrome')
        for name, url, vp, theme in JOBS:
            w, h = VP[vp]
            ctx = b.new_context(viewport={'width': w, 'height': h}, reduced_motion='reduce')
            pg = ctx.new_page()
            errs = []
            pg.on('console', lambda m, e=errs: e.append(m.text) if m.type in ('error', 'warning') else None)
            pg.on('pageerror', lambda x, e=errs: e.append('pageerror: %s' % x))
            sep = '&' if '?' in url.split('#')[0] else '?'
            base, _, frag = url.partition('#')
            pg.goto(U(base + sep + 'theme=' + theme))
            pg.wait_for_timeout(300)
            if frag == 'overlay':
                pg.keyboard.press('[')
                pg.wait_for_timeout(100)
            if frag == 'sheet':
                pg.click('#sheet-toggle')
                pg.wait_for_timeout(100)
            sw = pg.evaluate('document.documentElement.scrollWidth')
            if sw > w:
                summary['overflow'].append('%s %s scrollWidth=%d' % (name, vp, sw))
            for e in errs:
                summary['console_errors'].append('%s %s: %s' % (name, vp, e))
            pg.screenshot(path=os.path.join(OUT, '%s_%s_%s.png' % (name, vp, theme)))
            summary['shots'] += 1
            ctx.close()
        b.close()
    print(json.dumps(summary, indent=1))

if __name__ == '__main__':
    main()
