#!/usr/bin/env python3
"""Screenshots (webp), console errors, horizontal scroll, keyboard and contrast checks for reports-a.
Usage: python3 capture.py [shots|keys|contrast|all]
Requests to fonts.googleapis.com / gstatic are ignored (offline fallback fonts are used).
"""
import io
import os
import sys
from pathlib import Path
from playwright.sync_api import sync_playwright
from PIL import Image

HERE = Path(__file__).resolve().parent
SHOTS = HERE / 'screenshots'
SHOTS.mkdir(exist_ok=True)
URL = lambda page, qs='': 'file://%s/%s%s' % (HERE, page, ('?' + qs) if qs else '')
V = {'1440x900': (1440, 900), '1280x800': (1280, 800), '390x844': (390, 844)}

JOBS = []
for p in ['01-owner-editor', '02-reader']:
    JOBS += [(p, p, '', '1440x900', 'light'), (p, p, '', '1440x900', 'dark'), (p, p, '', '1280x800', 'light'), (p, p, '', '390x844', 'light')]
for st in ['empty', 'revoked', 'offline']:
    JOBS += [('02-reader-' + st, '02-reader', 'state=' + st, '1440x900', 'light'), ('02-reader-' + st, '02-reader', 'state=' + st, '390x844', 'light')]
JOBS += [('01-owner-preflight', '01-owner-editor', 'state=preflight', '1440x900', 'light'), ('01-owner-preflight', '01-owner-editor', 'state=preflight', '1440x900', 'dark'),
         ('01-owner-preflight', '01-owner-editor', 'state=preflight', '390x844', 'light')]

CONTRAST_JS = r"""
() => {
  const parse = c => { const m = c.match(/rgba?\(([^)]+)\)/); if (!m) return null; const p = m[1].split(/[ ,\/]+/).filter(Boolean).map(Number); return {r:p[0],g:p[1],b:p[2],a:p.length>3?p[3]:1}; };
  const lum = c => { const f = v => { v/=255; return v<=0.03928? v/12.92 : Math.pow((v+0.055)/1.055,2.4); }; return 0.2126*f(c.r)+0.7152*f(c.g)+0.0722*f(c.b); };
  const blend = (t, b) => ({r:t.r*t.a+b.r*(1-t.a), g:t.g*t.a+b.g*(1-t.a), b:t.b*t.a+b.b*(1-t.a), a:1});
  const bgOf = el => { let layers=[]; for (let e=el; e; e=e.parentElement) { const c = parse(getComputedStyle(e).backgroundColor); if (c && c.a>0) { layers.push(c); if (c.a>=1) break; } } let base={r:255,g:255,b:255,a:1}; if (!layers.length||layers[layers.length-1].a<1) base = parse(getComputedStyle(document.body).backgroundColor); for (let i=layers.length-1;i>=0;i--) base = layers[i].a>=1? layers[i] : blend(layers[i], base); return base; };
  const out=[], seen=new Set(); const w=document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
  while (w.nextNode()) { const t=w.currentNode; if(!t.textContent.trim()) continue; const el=t.parentElement; if(seen.has(el)) continue; seen.add(el);
    const cs=getComputedStyle(el); if(cs.visibility==='hidden'||cs.display==='none') continue; const r=el.getBoundingClientRect(); if(r.width<1||r.height<1) continue;
    if (el.closest('[hidden],.sr,dialog:not([open]),.pn')) continue;
    let fg=parse(cs.color); const bg=bgOf(el); if(!fg) continue; if(fg.a<1) fg=blend(fg,bg);
    const L1=lum(fg),L2=lum(bg), ratio=(Math.max(L1,L2)+.05)/(Math.min(L1,L2)+.05);
    if (ratio<4.5 && !el.closest('[disabled]')) out.push({text:t.textContent.trim().slice(0,40), ratio:+ratio.toFixed(2), tag:el.tagName+'.'+el.className}); }
  return {checked: seen.size, fails: out};
}
"""


def launch(p):
    try:
        return p.chromium.launch()
    except Exception as e:
        for path in ('/usr/bin/google-chrome', '/usr/bin/chromium-browser', '/usr/bin/chromium'):
            if os.path.exists(path):
                return p.chromium.launch(executable_path=path)
        raise e


def new_page(browser, vp, errors, tag):
    w, h = V[vp]
    ctx = browser.new_context(viewport={'width': w, 'height': h}, reduced_motion='reduce')
    pg = ctx.new_page()
    ok = lambda u: 'fonts.googleapis.com' in u or 'fonts.gstatic.com' in u
    pg.on('console', lambda m: errors.append((tag, m.type, m.text)) if m.type in ('error', 'warning') and 'fonts.g' not in m.text and 'ERR_' not in m.text else None)
    pg.on('pageerror', lambda e: errors.append((tag, 'pageerror', str(e))))
    pg.on('requestfailed', lambda r: errors.append((tag, 'requestfailed', r.url)) if not ok(r.url) else None)
    return ctx, pg


def shots():
    errors, hscroll, count = [], [], 0
    with sync_playwright() as p:
        b = launch(p)
        for name, page, qs, vp, theme in JOBS:
            tag = '%s_%s_%s' % (name, vp, theme)
            ctx, pg = new_page(b, vp, errors, tag)
            pg.goto(URL(page + '.html', (qs + '&' if qs else '') + 'theme=' + theme))
            pg.wait_for_timeout(350)
            sw = pg.evaluate('document.documentElement.scrollWidth'); cw = pg.evaluate('document.documentElement.clientWidth')
            if sw > cw:
                hscroll.append((tag, sw, cw))
            # elements wider than viewport (not clipped by an overflow container)
            wide = pg.evaluate("""() => [...document.querySelectorAll('body *')].filter(e => { const r=e.getBoundingClientRect(); if(!r.width||getComputedStyle(e).position==='fixed') return false; let a=e.parentElement; while(a&&a!==document.body){const o=getComputedStyle(a).overflowX; if(o==='auto'||o==='hidden'||o==='scroll') return false; a=a.parentElement;} return r.right>document.documentElement.clientWidth+1; }).slice(0,3).map(e=>e.tagName+'.'+e.className)""")
            if wide:
                hscroll.append((tag, 'element past viewport', wide))
            png = pg.screenshot(full_page=True)
            Image.open(io.BytesIO(png)).save(SHOTS / (tag + '.webp'), 'WEBP', quality=82)
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
        for page, vp in [('01-owner-editor', '1440x900'), ('02-reader', '1440x900'), ('02-reader', '390x844')]:
            ctx, pg = new_page(b, vp, errors, page)
            pg.goto(URL(page + '.html', 'theme=light'))
            pg.wait_for_timeout(300)
            seen, noring = [], []
            for i in range(120):
                pg.keyboard.press('Tab')
                info = pg.evaluate("""() => { const e=document.activeElement; if(!e||e===document.body) return null; const cs=getComputedStyle(e); const again=e.dataset.tv==='1'; e.dataset.tv='1';
                  return {again, id:e.tagName+'.'+(e.className||'').toString().slice(0,20)+' '+(e.getAttribute('aria-label')||e.textContent||'').trim().slice(0,24), ring: cs.outlineStyle!=='none'&&parseFloat(cs.outlineWidth)>=2}; }""")
                if not info:
                    continue
                if info['again']:
                    break
                seen.append(info['id'])
                if not info['ring']:
                    noring.append(info['id'])
            res.append((page, vp, 'tab stops %d' % len(seen), 'without 2px outline: %s' % noring))
            ctx.close()
        # reader composer by keyboard (desktop)
        ctx, pg = new_page(b, '1440x900', errors, 'composer')
        pg.goto(URL('02-reader.html', 'theme=light')); pg.wait_for_timeout(300)
        pg.focus('#b-res .rb'); pg.keyboard.press('Enter'); pg.wait_for_timeout(100)
        focused = pg.evaluate("document.activeElement.tagName")
        pg.keyboard.type('Test comment via keyboard'); pg.keyboard.press('Control+Enter'); pg.wait_for_timeout(100)
        added = pg.evaluate("document.querySelectorAll('#margin .thr').length")
        res.append(('composer: focus lands in', focused, 'threads after post', added, '(expect 5)'))
        # offset check: margin cards must not overlap
        ov = pg.evaluate("""() => { const c=[...document.querySelectorAll('#margin .thr')].map(e=>e.getBoundingClientRect()); let n=0; for(let i=1;i<c.length;i++) if(c[i].top<c[i-1].bottom-1) n++; return n; }""")
        res.append(('margin overlaps', ov))
        ctx.close()
        # mobile sheet
        ctx, pg = new_page(b, '390x844', errors, 'sheet')
        pg.goto(URL('02-reader.html', 'theme=light')); pg.wait_for_timeout(300)
        pg.click('#b-interp .rb'); pg.wait_for_timeout(100)
        res.append(('mobile sheet open', pg.evaluate("!!document.querySelector('.sheet')"), 'threads in sheet', pg.evaluate("document.querySelectorAll('.sheet .thr').length")))
        pg.screenshot(path=str(SHOTS / '02-reader-sheet_390x844_light.png'))
        Image.open(SHOTS / '02-reader-sheet_390x844_light.png').save(SHOTS / '02-reader-sheet_390x844_light.webp', 'WEBP', quality=82)
        (SHOTS / '02-reader-sheet_390x844_light.png').unlink()
        ctx.close()
        b.close()
    for r in res:
        print(*r, sep=' | ')
    print('console errors/warnings during keyboard pass:', len(errors))
    for e in errors:
        print('  ', e)


def contrast():
    errors, tf, tc = [], 0, 0
    combos = [('01-owner-editor', ''), ('01-owner-editor', 'state=preflight'), ('02-reader', ''), ('02-reader', 'state=offline'), ('02-reader', 'state=empty')]
    with sync_playwright() as p:
        b = launch(p)
        for theme in ('light', 'dark'):
            for pg_, qs in combos:
                ctx, pg = new_page(b, '1440x900', errors, pg_)
                pg.goto(URL(pg_ + '.html', (qs + '&' if qs else '') + 'theme=' + theme)); pg.wait_for_timeout(300)
                r = pg.evaluate(CONTRAST_JS); tc += r['checked']; tf += len(r['fails'])
                print('%s %s %s: %d checked, %d below 4.5:1' % (pg_, qs or 'default', theme, r['checked'], len(r['fails'])))
                for f in r['fails'][:10]:
                    print('    ', f)
                ctx.close()
        b.close()
    print('TOTAL checked', tc, 'fails', tf)


if __name__ == '__main__':
    mode = sys.argv[1] if len(sys.argv) > 1 else 'all'
    if mode in ('shots', 'all'):
        shots()
    if mode in ('keys', 'all'):
        keys()
    if mode in ('contrast', 'all'):
        contrast()
