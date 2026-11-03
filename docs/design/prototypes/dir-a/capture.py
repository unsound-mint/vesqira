#!/usr/bin/env python3
"""Capture screenshots, console errors, horizontal-scroll, keyboard and contrast checks for dir-a.
Usage: python3 capture.py [shots|keys|contrast|all]
"""
import json
import os
import sys
from pathlib import Path
from playwright.sync_api import sync_playwright

HERE = Path(__file__).resolve().parent
SHOTS = HERE / 'screenshots'
SHOTS.mkdir(exist_ok=True)
URL = lambda page, qs='': 'file://%s/%s%s' % (HERE, page, ('?' + qs) if qs else '')

PAGES = ['01-explorer', '02-viewer', '03-compare', '04-board']
V = {'1440x900': (1440, 900), '1280x800': (1280, 800), '390x844': (390, 844)}

# (name suffix, page, query, viewport, theme)
JOBS = []
for p in PAGES:
    JOBS += [(p, p, '', '1440x900', 'light'), (p, p, '', '1440x900', 'dark'), (p, p, '', '1280x800', 'light'), (p, p, '', '390x844', 'light')]
for st in ['loading', 'error', 'nowcs', 'nospectral']:
    JOBS.append(('02-viewer-' + st, '02-viewer', 'state=' + st, '1280x800', 'light'))
JOBS += [
    ('02-viewer-drawer', '02-viewer', 'drawer=1', '390x844', 'light'),
    ('02-viewer-nowcs', '02-viewer', 'state=nowcs', '390x844', 'light'),
    ('01-explorer-empty', '01-explorer', 'state=empty', '1440x900', 'light'),
    ('01-explorer-empty', '01-explorer', 'state=empty', '390x844', 'light'),
    ('03-compare-blocked', '03-compare', 'state=blocked', '1440x900', 'light'),
    ('03-compare-blocked', '03-compare', 'state=blocked', '390x844', 'light'),
    ('04-board-reviewer', '04-board', 'state=reviewer', '1440x900', 'light'),
    ('04-board-reviewer', '04-board', 'state=reviewer', '390x844', 'light'),
    ('04-board-denied', '04-board', 'state=denied', '1440x900', 'light'),
    ('04-board-denied', '04-board', 'state=denied', '390x844', 'light'),
    ('02-viewer-error', '02-viewer', 'state=error', '1440x900', 'dark'),
    ('03-compare-blocked', '03-compare', 'state=blocked', '1440x900', 'dark'),
    ('04-board-reviewer', '04-board', 'state=reviewer', '1440x900', 'dark'),
]

CONTRAST_JS = r"""
() => {
  const parse = c => { const m = c.match(/rgba?\(([^)]+)\)/); if (!m) return null; const p = m[1].split(/[ ,\/]+/).filter(Boolean).map(Number); return {r:p[0],g:p[1],b:p[2],a:p.length>3?p[3]:1}; };
  const lum = c => { const f = v => { v/=255; return v<=0.03928? v/12.92 : Math.pow((v+0.055)/1.055,2.4); }; return 0.2126*f(c.r)+0.7152*f(c.g)+0.0722*f(c.b); };
  const blend = (top, bot) => ({r: top.r*top.a+bot.r*(1-top.a), g: top.g*top.a+bot.g*(1-top.a), b: top.b*top.a+bot.b*(1-top.a), a:1});
  const bgOf = el => { let layers=[]; for (let e=el; e; e=e.parentElement) { const c = parse(getComputedStyle(e).backgroundColor); if (c && c.a>0) { layers.push(c); if (c.a>=1) break; } } let base={r:255,g:255,b:255,a:1}; if (!layers.length||layers[layers.length-1].a<1) base = parse(getComputedStyle(document.body).backgroundColor); for (let i=layers.length-1;i>=0;i--) base = layers[i].a>=1? layers[i] : blend(layers[i], base); return base; };
  const out = []; const seen = new Set();
  const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
  while (walker.nextNode()) {
    const t = walker.currentNode; if (!t.textContent.trim()) continue;
    const el = t.parentElement; if (seen.has(el)) continue; seen.add(el);
    const cs = getComputedStyle(el); if (cs.visibility==='hidden' || cs.display==='none') continue;
    const r = el.getBoundingClientRect(); if (r.width<1 || r.height<1) continue;
    if (el.closest('[hidden],.sr-only,dialog:not([open]),.plate-note,.scalebar,.roi,.plate-tag,.pinmark')) continue;
    let fg = parse(cs.color); const bg = bgOf(el); if (!fg) continue;
    if (fg.a<1) fg = blend(fg,bg);
    const L1 = lum(fg), L2 = lum(bg); const ratio = (Math.max(L1,L2)+0.05)/(Math.min(L1,L2)+0.05);
    const size = parseFloat(cs.fontSize), bold = parseInt(cs.fontWeight)>=700;
    const large = size>=24 || (size>=18.66 && bold);
    const need = large?3:4.5;
    const disabled = el.closest('[disabled],[aria-disabled=true]');
    if (ratio < need && !disabled) out.push({text: t.textContent.trim().slice(0,40), ratio: +ratio.toFixed(2), need, fg: cs.color, bg: `rgb(${Math.round(bg.r)},${Math.round(bg.g)},${Math.round(bg.b)})`, tag: el.tagName+'.'+el.className});
  }
  return {checked: seen.size, fails: out};
}
"""


def launch(p):
    try:
        return p.chromium.launch()
    except Exception as e:  # fall back to system browsers
        for path in ('/usr/bin/google-chrome', '/usr/bin/chromium-browser', '/usr/bin/chromium'):
            if os.path.exists(path):
                return p.chromium.launch(executable_path=path)
        raise e


def new_page(browser, vp, theme, errors, tag):
    w, h = V[vp]
    ctx = browser.new_context(viewport={'width': w, 'height': h}, reduced_motion='reduce')
    pg = ctx.new_page()
    pg.on('console', lambda m: errors.append((tag, m.type, m.text)) if m.type in ('error', 'warning') else None)
    pg.on('pageerror', lambda e: errors.append((tag, 'pageerror', str(e))))
    pg.on('requestfailed', lambda r: errors.append((tag, 'requestfailed', r.url)))
    return ctx, pg


def shots():
    errors, hscroll, count = [], [], 0
    with sync_playwright() as p:
        b = launch(p)
        for name, page, qs, vp, theme in JOBS:
            tag = '%s_%s_%s' % (name, vp, theme)
            ctx, pg = new_page(b, vp, theme, errors, tag)
            q = (qs + '&' if qs else '') + 'theme=' + theme
            pg.goto(URL(page + '.html', q.replace('drawer=1&', '')))
            pg.wait_for_timeout(250)
            if 'drawer=1' in qs:
                pg.click('.drawerbtn')
                pg.wait_for_timeout(100)
            sw = pg.evaluate('document.documentElement.scrollWidth'); cw = pg.evaluate('document.documentElement.clientWidth')
            if sw > cw:
                hscroll.append((tag, sw, cw))
            pg.screenshot(path=str(SHOTS / (tag + '.png')), full_page=not (page == '02-viewer' and vp == '390x844'))
            count += 1
            ctx.close()
        b.close()
    print('screenshots:', count)
    print('console errors/warnings:', len(errors))
    for e in errors:
        print('  ', e)
    print('horizontal scroll cases:', len(hscroll))
    for h in hscroll:
        print('  ', h)


def keys():
    errors, res = [], []
    with sync_playwright() as p:
        b = launch(p)
        for page, vp in [('01-explorer', '1440x900'), ('02-viewer', '1440x900'), ('03-compare', '1440x900'), ('04-board', '1440x900'), ('02-viewer', '390x844')]:
            ctx, pg = new_page(b, vp, 'light', errors, page)
            pg.goto(URL(page + '.html'))
            seen, noring = [], []
            total = pg.evaluate("""() => [...document.querySelectorAll('a[href],button,input,select,textarea,summary,[tabindex="0"]')].filter(e => { const r=e.getBoundingClientRect(); const cs=getComputedStyle(e); return !e.disabled && cs.visibility!=='hidden' && cs.display!=='none' && r.width>0 && !e.closest('[hidden]') && !(e.matches('input[type=checkbox][disabled]')); }).length""")
            for i in range(total + 6):
                pg.keyboard.press('Tab')
                info = pg.evaluate("""() => { const e=document.activeElement; if(!e||e===document.body) return null; const cs=getComputedStyle(e); const r=e.getBoundingClientRect(); const again = e.dataset.tv==='1'; e.dataset.tv='1';
                  return {again, id: e.tagName+'#'+(e.id||'')+'.'+(e.className||'').toString().slice(0,24)+' '+(e.getAttribute('aria-label')||e.textContent||'').trim().slice(0,28), ring: cs.outlineStyle!=='none' && parseFloat(cs.outlineWidth)>=2, vis: r.width>0 && r.height>0}; }""")
                if not info:
                    continue
                if info['again']:
                    break
                seen.append(info['id'])
                if not info['ring']:
                    noring.append(info['id'])
            res.append((page, vp, 'focusable expected~%d' % total, 'tab stops reached %d' % len(seen), 'without 2px outline: %s' % noring))
            if page == '02-viewer' and vp == '1440x900':
                pg.goto(URL('02-viewer.html', 'theme=light'))
                pg.wait_for_timeout(200)
                def chv():
                    return pg.evaluate("[document.getElementById('chan').value, document.getElementById('velBig').textContent]")
                a = chv()
                pg.focus('#frame'); pg.keyboard.press('ArrowRight'); r1 = chv()
                pg.keyboard.press('Shift+ArrowRight'); r2 = chv()
                pg.keyboard.press('ArrowLeft'); r3 = chv()
                pg.keyboard.press('Home'); r4 = chv(); pg.keyboard.press('End'); r5 = chv()
                pg.focus('#chan'); pg.keyboard.press('Home'); pg.keyboard.press('ArrowRight'); r6 = chv()
                pg.keyboard.press('Control+ArrowRight'); px = pg.evaluate("document.getElementById('rdPix').textContent")
                pg.keyboard.press('r'); roi_hidden = pg.evaluate("document.getElementById('roi').hidden")
                pg.keyboard.press('?'); dlg = pg.evaluate("document.getElementById('help').open"); pg.keyboard.press('Escape')
                res.append(('viewer keys', 'start', a, 'plate Right', r1, 'Shift+Right', r2, 'Left', r3, 'Home', r4, 'End', r5, 'slider Home,Right', r6, 'Ctrl+Right px', px, 'R toggles ROI hidden=', roi_hidden, '? opens help=', dlg))
            ctx.close()
        b.close()
    for r in res:
        print(*r, sep=' | ')
    print('console errors/warnings during keyboard pass:', len(errors))
    for e in errors:
        print('  ', e)


def contrast():
    errors, total_fail, total_checked = [], 0, 0
    combos = [(pg_, qs) for pg_, qs in [('01-explorer', ''), ('01-explorer', 'state=empty'), ('02-viewer', ''), ('02-viewer', 'state=nowcs'), ('02-viewer', 'state=error'), ('02-viewer', 'state=loading'), ('03-compare', ''), ('03-compare', 'state=blocked'), ('04-board', ''), ('04-board', 'state=reviewer')]]
    with sync_playwright() as p:
        b = launch(p)
        for theme in ('light', 'dark'):
            for pg_, qs in combos:
                ctx, pg = new_page(b, '1440x900', theme, errors, pg_)
                pg.goto(URL(pg_ + '.html', (qs + '&' if qs else '') + 'theme=' + theme))
                pg.wait_for_timeout(200)
                r = pg.evaluate(CONTRAST_JS)
                total_checked += r['checked']; total_fail += len(r['fails'])
                print('%s %s %s: %d text elements checked, %d below threshold' % (pg_, qs or 'default', theme, r['checked'], len(r['fails'])))
                for f in r['fails'][:12]:
                    print('    ', f)
                ctx.close()
        b.close()
    print('TOTAL checked', total_checked, 'fails', total_fail)


if __name__ == '__main__':
    mode = sys.argv[1] if len(sys.argv) > 1 else 'all'
    if mode in ('shots', 'all'):
        shots()
    if mode in ('keys', 'all'):
        keys()
    if mode in ('contrast', 'all'):
        contrast()
