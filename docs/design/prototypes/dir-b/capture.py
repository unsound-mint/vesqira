#!/usr/bin/env python3
"""Capture screenshots of the Direction B prototype with a real browser (Playwright, sync API).
Also logs console messages/page errors and horizontal overflow at each capture.
Usage: python3 capture.py   (writes PNGs to ./screenshots, prints a summary)"""
import json
import pathlib
import sys
from playwright.sync_api import sync_playwright

HERE = pathlib.Path(__file__).resolve().parent
OUT = HERE / 'screenshots'
OUT.mkdir(exist_ok=True)

def url(page, q=''):
    return f'file://{HERE}/{page}' + (f'?{q}' if q else '')

# (page, query, viewport, theme, name)
JOBS = []
for p, n in (('01-explorer.html', '01-explorer'), ('02-viewer.html', '02-viewer'), ('03-compare.html', '03-compare'), ('04-board.html', '04-board')):
    JOBS += [(p, '', (1440, 900), 'dark', f'{n}_1440x900_dark'),
             (p, '', (1440, 900), 'light', f'{n}_1440x900_light'),
             (p, '', (1280, 800), 'dark', f'{n}_1280x800_dark'),
             (p, '', (390, 844), 'dark', f'{n}_390x844_dark')]
JOBS += [
    ('01-explorer.html', 'state=empty', (1440, 900), 'dark', '01-explorer_empty_1440x900_dark'),
    ('01-explorer.html', 'item=nowcs', (1440, 900), 'dark', '01-explorer_nowcs_1440x900_dark'),
    ('01-explorer.html', 'item=off', (1440, 900), 'dark', '01-explorer_offline_1440x900_dark'),
    ('01-explorer.html', 'item=bad', (1440, 900), 'dark', '01-explorer_corrupt_1440x900_dark'),
    ('01-explorer.html', 'item=v2', (390, 844), 'dark', '01-explorer_detail_390x844_dark'),
    ('02-viewer.html', 'state=loading', (1440, 900), 'dark', '02-viewer_loading_1440x900_dark'),
    ('02-viewer.html', 'state=error', (1440, 900), 'dark', '02-viewer_error_1440x900_dark'),
    ('02-viewer.html', 'state=no-wcs', (1280, 800), 'dark', '02-viewer_no-wcs_1280x800_dark'),
    ('02-viewer.html', 'state=no-spectral', (1280, 800), 'dark', '02-viewer_no-spectral_1280x800_dark'),
    ('02-viewer.html', 'state=palette', (1440, 900), 'dark', '02-viewer_palette_1440x900_dark'),
    ('02-viewer.html', 'state=palette', (1280, 800), 'light', '02-viewer_palette_1280x800_light'),
    ('03-compare.html', 'state=blocked', (1440, 900), 'dark', '03-compare_blocked_1440x900_dark'),
    ('03-compare.html', 'state=blocked', (390, 844), 'dark', '03-compare_blocked_390x844_dark'),
    ('04-board.html', 'state=reviewer', (1440, 900), 'dark', '04-board_reviewer_1440x900_dark'),
    ('04-board.html', 'state=reviewer', (1440, 900), 'light', '04-board_reviewer_1440x900_light'),
    ('04-board.html', 'state=reviewer', (390, 844), 'dark', '04-board_reviewer_390x844_dark'),
    ('04-board.html', 'state=denied', (1440, 900), 'dark', '04-board_denied_1440x900_dark'),
    ('04-board.html', 'state=denied', (390, 844), 'dark', '04-board_denied_390x844_dark'),
]

def main():
    report = {'shots': 0, 'console': [], 'overflow': []}
    with sync_playwright() as pw:
        try:
            br = pw.chromium.launch()
        except Exception as e:  # fall back to system chrome
            print('chromium launch failed, trying google-chrome:', e, file=sys.stderr)
            br = pw.chromium.launch(executable_path='/usr/bin/google-chrome')
        for page_name, q, (w, h), theme, name in JOBS:
            ctx = br.new_context(viewport={'width': w, 'height': h}, device_scale_factor=1)
            pg = ctx.new_page()
            pg.on('console', lambda m, n=name: report['console'].append((n, m.type, m.text)) if m.type in ('error', 'warning') else None)
            pg.on('pageerror', lambda e, n=name: report['console'].append((n, 'pageerror', str(e))))
            pg.on('requestfailed', lambda r, n=name: report['console'].append((n, 'requestfailed', r.url)))
            qs = ('&' if q else '') + f'theme={theme}'
            pg.goto(url(page_name, q + qs))
            pg.wait_for_timeout(350)
            if 'viewer' in page_name or 'compare' in page_name:
                # park the pointer on the image so cursor HUD and linked crosshair are populated
                sel = '.stage'
                try:
                    box = pg.locator(sel).first.bounding_box()
                    if box and w > 600 and 'palette' not in q:
                        pg.mouse.move(box['x'] + box['width'] * 0.47, box['y'] + box['height'] * 0.46)
                        pg.wait_for_timeout(150)
                except Exception:
                    pass
            ov = pg.evaluate('({sw: document.documentElement.scrollWidth, cw: document.documentElement.clientWidth, bw: document.body.scrollWidth})')
            if ov['sw'] > ov['cw'] or ov['bw'] > ov['cw']:
                report['overflow'].append((name, ov))
            pg.screenshot(path=str(OUT / f'{name}.png'))
            report['shots'] += 1
            ctx.close()
        br.close()
    print(json.dumps({'shots': report['shots'], 'console_errors': len(report['console']), 'overflow': report['overflow']}, indent=1))
    for c in report['console']:
        print('CONSOLE', c)

if __name__ == '__main__':
    main()
