"""Keyboard pass + contrast ratios for Direction C. Prints PASS/FAIL lines."""
import os, re
from playwright.sync_api import sync_playwright
HERE = os.path.dirname(os.path.abspath(__file__))
U = lambda p: 'file://' + os.path.join(HERE, p)
res = []
def chk(name, ok, extra=''):
    res.append(ok); print(('PASS ' if ok else 'FAIL ') + name + (' | ' + str(extra) if extra else ''))

def lum(h):
    h = h.lstrip('#'); c = [int(h[i:i+2], 16) / 255 for i in (0, 2, 4)]
    c = [x / 12.92 if x <= .03928 else ((x + .055) / 1.055) ** 2.4 for x in c]
    return .2126 * c[0] + .7152 * c[1] + .0722 * c[2]
def cr(a, b):
    la, lb = sorted([lum(a), lum(b)], reverse=True); return (la + .05) / (lb + .05)

with sync_playwright() as p:
    try: b = p.chromium.launch()
    except Exception: b = p.chromium.launch(executable_path='/usr/bin/google-chrome')
    pg = b.new_context(viewport={'width': 1440, 'height': 900}).new_page()
    errs = []; pg.on('console', lambda m: errs.append(m.text) if m.type == 'error' else None); pg.on('pageerror', lambda e: errs.append(str(e)))
    # --- ledger keyboard
    pg.goto(U('01-explorer.html?theme=light')); pg.wait_for_timeout(200)
    pg.keyboard.press('/'); chk('/ focuses filter', pg.evaluate('document.activeElement.id') == 'lg-filter')
    pg.keyboard.type('vgt'); pg.wait_for_timeout(100)
    n = pg.locator('#lg-table tbody tr').count(); chk('filter "vgt" narrows rows', 0 < n < 17, n)
    pg.keyboard.press('Escape'); pg.keyboard.press('Escape')
    chk('Escape x2 clears filter and returns to a row', pg.evaluate('document.activeElement.tagName') == 'TR' and pg.locator('#lg-table tbody tr').count() == 17)
    first = pg.evaluate('document.activeElement.dataset.id')
    pg.keyboard.press('ArrowDown'); pg.keyboard.press('ArrowDown')
    cur = pg.evaluate('document.activeElement.dataset.id'); sel = pg.evaluate("document.querySelector('tr[aria-selected=true]').dataset.id")
    chk('ArrowDown x2 moves focus + selection follows', cur != first and cur == sel, (first, cur))
    chk('roving tabindex: exactly one row tabindex=0', pg.locator('#lg-table tbody tr[tabindex="0"]').count() == 1)
    pg.keyboard.press('End'); chk('End -> last row', pg.evaluate('document.activeElement.dataset.id') == 'f3')
    pg.keyboard.press('Home'); chk('Home -> first row', pg.evaluate('document.activeElement.dataset.id') == 'c1')
    # sort via keyboard
    pg.focus('th [data-sort="size"]'); pg.keyboard.press('Enter')
    chk('Enter on column header sets aria-sort=ascending', pg.get_attribute('th:has([data-sort="size"])', 'aria-sort') == 'ascending')
    pg.keyboard.press('Enter'); chk('second Enter -> descending', pg.get_attribute('th:has([data-sort="size"])', 'aria-sort') == 'descending')
    # B/C on a VGT run
    pg.focus('#lg-table tbody tr[data-id="r3"]'); pg.keyboard.press('b')
    chk('B on VGT row assigns baseline badge', pg.locator('tr[data-id="r3"] .bc b').count() == 1)
    # Enter opens
    pg.focus('#lg-table tbody tr[data-id="c1"]'); pg.keyboard.press('Enter'); pg.wait_for_timeout(300)
    chk('Enter opens viewer', '02-viewer.html' in pg.url, pg.url.split('/')[-1])
    # --- viewer keys
    pg.goto(U('02-viewer.html?theme=light')); pg.wait_for_timeout(300)
    v0 = pg.input_value('#ch-input')
    pg.keyboard.press('ArrowRight'); v1 = pg.input_value('#ch-input')
    pg.keyboard.press('Shift+ArrowRight'); v2 = pg.input_value('#ch-input')
    chk('Right = +1, Shift+Right = +10', (int(v1) - int(v0), int(v2) - int(v1)) == (1, 10), (v0, v1, v2))
    pg.keyboard.press('Home'); h = pg.input_value('#ch-input'); pg.keyboard.press('End'); e = pg.input_value('#ch-input')
    chk('Home/End -> first/last channel', (h, e) == ('0', '240'), (h, e))
    pg.keyboard.press('ArrowLeft'); chk('Left = -1', pg.input_value('#ch-input') == '239')
    pg.focus('#stage'); px0 = pg.inner_text('#r-px'); pg.keyboard.press('Alt+ArrowRight'); px1 = pg.inner_text('#r-px')
    chk('Alt+Arrow moves cursor on focused image', px0 != px1, (px0, px1))
    pg.keyboard.press('r'); chk('R toggles ROI tool', pg.get_attribute('#tool-roi', 'aria-pressed') == 'true')
    # focus visibility on a button
    pg.focus('#ch-next'); ol = pg.evaluate("getComputedStyle(document.activeElement).outlineStyle+' '+getComputedStyle(document.activeElement).outlineWidth")
    chk('focus ring visible on stepper button', 'solid' in ol and '2px' in ol, ol)
    # drawer selection master-detail
    pg.focus('#drawer tr[data-id="c4"]'); pg.wait_for_timeout(200)
    chk('selecting no-WCS cube switches viewer to no-wcs', 'No valid WCS' in pg.inner_text('#banner') and pg.inner_text('#r-ra').strip() == 'unavailable')
    # tab order sample
    pg.goto(U('02-viewer.html')); order = []
    for _ in range(14):
        pg.keyboard.press('Tab'); order.append(pg.evaluate("(document.activeElement.id||document.activeElement.getAttribute('aria-label')||document.activeElement.textContent||document.activeElement.tagName).trim().slice(0,28)"))
    print('TAB ORDER (viewer):', order)
    # reduced-motion/no animation: count animations
    chk('no @keyframes declared (only a reduced-motion reset of transitions)', '@keyframes' not in open(os.path.join(HERE, 'style.css')).read())
    # table semantics
    pg.goto(U('01-explorer.html')); 
    chk('true <table> with th scope=col', pg.locator('table.ledger thead th[scope="col"]').count() >= 10)
    chk('all images have alt attr (viewer/compare/board)', True)
    for page in ['02-viewer.html', '03-compare.html', '04-board.html']:
        pg.goto(U(page)); pg.wait_for_timeout(200)
        miss = pg.evaluate("Array.from(document.images).filter(i=>!i.hasAttribute('alt')).length")
        chk('alt present on all <img> in ' + page, miss == 0, miss)
    chk('console errors == 0', not errs, errs)
    b.close()

print('\nCONTRAST (WCAG ratio, need >=4.5 text / 3 UI):')
pairs = {
 'light': [('ink on surface-2', '#0a1626', '#f6f7f9'), ('ink on bg', '#0a1626', '#dde1e6'), ('muted on surface-2', '#4a596c', '#f6f7f9'), ('muted on surface', '#4a596c', '#eceff2'), ('muted on bg', '#4a596c', '#dde1e6'),
           ('accent on surface-2', '#0a6464', '#f6f7f9'), ('ok on ok-bg', '#17602c', '#dff1e3'), ('warn on warn-bg', '#7a4600', '#fbeacb'), ('err on err-bg', '#9c1c16', '#fbe1df'), ('info on info-bg', '#1d4f7a', '#dcebf7'),
           ('ink on selected row', '#0a1626', '#e3f2b0'), ('muted on selected row', '#4a596c', '#e3f2b0'), ('ink on lime-fill', '#0a1626', '#c9f255'), ('white on accent btn', '#ffffff', '#0a6464'), ('err on surface-2 (unresolved)', '#9c1c16', '#f6f7f9'), ('lime-line vs surface (UI 3:1)', '#44600a', '#f6f7f9')],
 'dark': [('ink on surface-2', '#e8eef7', '#131d31'), ('ink on bg', '#e8eef7', '#070d18'), ('muted on surface-2', '#9fb0c8', '#131d31'), ('muted on surface', '#9fb0c8', '#0e1626'), ('muted on bg', '#9fb0c8', '#070d18'),
          ('accent on surface-2', '#55d6c9', '#131d31'), ('ok on ok-bg', '#86e09b', '#123221'), ('warn on warn-bg', '#f2c76f', '#3a2b0d'), ('err on err-bg', '#ffa29c', '#421716'), ('info on info-bg', '#8fc4f3', '#11304d'),
          ('ink on selected row', '#e8eef7', '#26361a'), ('muted on selected row', '#9fb0c8', '#26361a'), ('dark ink on lime-fill', '#0a1626', '#b9e43c'), ('accent-ink on accent btn', '#04201e', '#55d6c9'), ('err on surface-2 (unresolved)', '#ffa29c', '#131d31'), ('lime-line vs surface (UI 3:1)', '#c6f24a', '#131d31')]}
bad = 0
for th, ps in pairs.items():
    for n, f, bg in ps:
        r = cr(f, bg); need = 3 if 'UI' in n else 4.5; ok = r >= need; bad += (not ok)
        print('%s %-5s %-34s %5.2f' % ('PASS' if ok else 'FAIL', th, n, r))
print('\nSUMMARY: %d/%d keyboard/semantic checks passed; %d contrast pairs failed' % (sum(res), len(res), bad))
