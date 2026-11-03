/* Vesqira prototype, direction A. Disposable. No network, no dependencies.
   Mock values are generated deterministically; nothing here reads pixels from the images. */
(function () {
  'use strict';
  var $ = function (s, r) { return (r || document).querySelector(s); };
  var $$ = function (s, r) { return Array.prototype.slice.call((r || document).querySelectorAll(s)); };
  var root = document.documentElement;
  var q = new URLSearchParams(location.search);
  var page = document.body.dataset.page;
  var state = q.get('state') || document.body.dataset.defaultState || 'ready';
  root.dataset.state = state;

  /* ---------- theme ---------- */
  var stored = null;
  try { stored = localStorage.getItem('va-theme'); } catch (e) { /* storage may be blocked */ }
  var theme = q.get('theme') || stored || 'light';
  root.dataset.theme = theme;
  var tbtn = $('#themeBtn');
  function syncTheme() {
    if (!tbtn) return;
    tbtn.setAttribute('aria-pressed', String(root.dataset.theme === 'dark'));
    tbtn.setAttribute('aria-label', root.dataset.theme === 'dark' ? 'Dark theme on. Switch to light' : 'Light theme on. Switch to dark');
    tbtn.title = tbtn.getAttribute('aria-label');
  }
  syncTheme();
  if (tbtn) tbtn.addEventListener('click', function () {
    var t = root.dataset.theme === 'dark' ? 'light' : 'dark';
    root.dataset.theme = t;
    try { localStorage.setItem('va-theme', t); } catch (e) { /* ignore */ }
    syncTheme();
  });

  /* ---------- prototype state: data-only / data-hide ---------- */
  function applyState() {
    $$('[data-only]').forEach(function (el) { el.hidden = el.dataset.only.split(' ').indexOf(state) < 0; });
    $$('[data-hide]').forEach(function (el) { if (el.dataset.hide.split(' ').indexOf(state) >= 0) el.hidden = true; });
    $$('.proto nav a').forEach(function (a) {
      a.setAttribute('aria-current', String(a.dataset.state === state));
    });
  }
  applyState();

  /* ---------- command search ---------- */
  var search = $('#cmd');
  document.addEventListener('keydown', function (e) {
    var t = e.target, typing = t.matches && t.matches('input[type=text],input[type=search],input[type=number],input[type=date],textarea,select');
    if (e.key === '/' && !typing && search) {
      e.preventDefault();
      var wrap = search.closest('.search');
      if (wrap) wrap.classList.add('open');
      search.focus();
    }
  });
  var sbtn = $('#searchBtn');
  if (sbtn) sbtn.addEventListener('click', function () {
    var w = $('.search');
    w.classList.toggle('open');
    sbtn.setAttribute('aria-expanded', String(w.classList.contains('open')));
    if (w.classList.contains('open')) search.focus();
  });

  /* ---------- plate ticks ---------- */
  function pickStep(span, steps, target) {
    for (var i = 0; i < steps.length; i++) if (span / steps[i] <= target) return steps[i];
    return steps[steps.length - 1];
  }
  var P2 = function (n) { return String(n).padStart(2, '0'); };
  function fmtRA(deg, dp) {
    var s = ((deg % 360) + 360) % 360 / 15 * 3600;
    var h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), sec = s - h * 3600 - m * 60;
    return h + 'h ' + P2(m) + 'm ' + (sec < 10 ? '0' : '') + sec.toFixed(dp) + 's';
  }
  function fmtDec(deg, dp) {
    var sg = deg < 0 ? '−' : '+', a = Math.abs(deg), d = Math.floor(a), m = Math.floor((a - d) * 60), s = (a - d - m / 60) * 3600;
    return sg + P2(d) + '° ' + P2(m) + '′ ' + (s < 10 ? '0' : '') + s.toFixed(dp) + '″';
  }
  function buildTicks(p) {
    var X = $('.plate__x', p), Y = $('.plate__y', p);
    X.textContent = ''; Y.textContent = '';
    var N = +p.dataset.n, nt = +(p.dataset.ticks || 5);
    function add(host, cls, pct, text) {
      var e = document.createElement('span'); e.className = cls; e.textContent = text;
      if (cls === 'tx') e.style.left = pct + '%'; else e.style.top = pct + '%';
      host.appendChild(e);
    }
    if (p.dataset.mode === 'pix') {
      var st = pickStep(N, [50, 100, 200, 250, 500, 1000], nt);
      for (var v = st; v < N; v += st) { add(X, 'tx', v / N * 100, String(v)); add(Y, 'ty', (1 - v / N) * 100, String(v)); }
      return;
    }
    var cd = +p.dataset.cdelt, ra0 = +p.dataset.ra0, dec0 = +p.dataset.dec0, cosd = Math.cos(dec0 * Math.PI / 180);
    var wRa = N * cd / cosd, raS = wRa * 240, c0 = ra0 * 240;
    var step = pickStep(raS, [1, 2, 5, 10, 15, 20, 30, 60, 120, 180, 300, 600, 900, 1200, 1800, 3600], nt);
    for (var t = Math.ceil((c0 - raS / 2) / step) * step; t <= c0 + raS / 2; t += step) {
      var f = 0.5 + (c0 - t) / raS; if (f < 0.06 || f > 0.94) continue;
      var h = Math.floor(t / 3600), mm = Math.floor((t % 3600) / 60), ss = Math.round(t % 60);
      add(X, 'tx', f * 100, h + 'h ' + P2(mm) + 'm' + (step < 60 ? ' ' + P2(ss) + 's' : ''));
    }
    var wD = N * cd * 60, d0 = dec0 * 60;
    var ds = pickStep(wD, [1, 2, 5, 10, 15, 20, 30, 60, 120, 300], nt);
    for (var d = Math.ceil((d0 - wD / 2) / ds) * ds; d <= d0 + wD / 2; d += ds) {
      var fy = 0.5 - (d - d0) / wD; if (fy < 0.06 || fy > 0.94) continue;
      var a = Math.abs(d), deg = Math.floor(a / 60 + 1e-9), mi = Math.round(a - deg * 60);
      add(Y, 'ty', fy * 100, (d < 0 ? '−' : '+') + P2(deg) + '°' + (ds < 60 ? ' ' + P2(mi) + '′' : ''));
    }
  }
  var hatchN = 0;
  function addHatch(frame) {
    var n = ++hatchN;
    frame.insertAdjacentHTML('beforeend',
      '<svg class="hatch" viewBox="0 0 512 512" preserveAspectRatio="none" aria-hidden="true"><defs>' +
      '<pattern id="hp' + n + '" width="8" height="8" patternUnits="userSpaceOnUse" patternTransform="rotate(45)">' +
      '<line x1="0" y1="0" x2="0" y2="8" stroke="#fff" stroke-width="2"/></pattern>' +
      '<mask id="hm' + n + '" style="mask-type:alpha"><image href="../_shared/assets/vgt_invalid_mask.png" width="512" height="512"/></mask></defs>' +
      '<rect width="512" height="512" fill="url(#hp' + n + ')" mask="url(#hm' + n + ')"/></svg>');
  }
  $$('[data-plate]').forEach(function (p) {
    if (state === 'blocked' && p.dataset.blockedCdelt) { p.dataset.cdelt = p.dataset.blockedCdelt; p.dataset.ra0 = p.dataset.blockedRa0; }
    if (p.hasAttribute('data-wcs-aware') && state === 'nowcs') p.dataset.mode = 'pix';
    buildTicks(p);
    $$('[data-hatch]', p).forEach(addHatch);
  });

  /* ---------- shared helpers ---------- */
  function hash(x, y, c) {
    var h = (Math.imul(x, 374761393) + Math.imul(y, 668265263) + Math.imul(c, 1274126177)) | 0;
    h = Math.imul(h ^ (h >>> 13), 1274126177); h ^= h >>> 16;
    return (h >>> 0) / 4294967296;
  }
  var fmtV = function (v) { return (v < 0 ? '−' : '+') + Math.abs(v).toFixed(2); };

  /* ---------- viewer ---------- */
  if (page === 'viewer') initViewer();
  function initViewer() {
    var SPEC = [-0.27, 1.06, -0.51, -0.89, -1.49, 0.31, 0.46, 0.16, -0.18, 0.24, 1.07, -1.21, 0.24, 0.32, 0.91, -0.55, 0.35, 0.73, -0.12, -0.51, -0.52, -0.62, -0.4, 0.42, -0.32, -0.35, -0.11, -1.03, -0.65, -0.64, -0.31, 0.39, -0.11, 0.47, 1.31, -0.41, 0.22, 0.51, 0.81, 0.49, -0.47, -0.31, -0.74, 0.58, 0.04, 0.2, 0.69, -0.3, 1.39, 0.56, 0.68, 0.54, 0.63, -0.12, -0.26, 1.79, 1.62, 2.23, 0.85, 1.72, 1.65, 2.51, 2.3, 1.73, 3.35, 4.31, 2.89, 4.21, 4.14, 5.37, 5.93, 6.29, 7.02, 7.3, 6.83, 9.33, 9.01, 9.66, 10.52, 10.73, 11.25, 12.98, 11.94, 13.23, 13.96, 15.16, 15.27, 16.59, 16.48, 16.31, 16.93, 18.06, 17.65, 18.0, 18.56, 19.07, 18.94, 18.53, 18.38, 19.09, 17.87, 18.31, 17.41, 17.51, 18.16, 16.45, 16.09, 16.51, 15.67, 14.93, 14.92, 13.34, 13.02, 14.01, 12.24, 11.76, 12.62, 10.59, 10.37, 10.87, 10.02, 11.37, 9.66, 9.18, 8.94, 9.88, 9.03, 8.28, 7.85, 8.64, 8.54, 9.13, 8.94, 8.77, 8.84, 7.6, 7.87, 8.43, 8.25, 8.87, 8.82, 7.99, 8.1, 10.15, 9.52, 9.72, 8.62, 8.54, 9.65, 8.1, 9.11, 8.97, 9.5, 8.84, 9.09, 8.72, 10.08, 7.58, 8.11, 8.48, 8.07, 7.12, 6.72, 7.25, 6.54, 6.19, 7.45, 6.54, 5.67, 5.47, 4.53, 5.34, 4.29, 5.3, 3.59, 3.46, 3.16, 3.82, 3.69, 3.46, 2.72, 3.44, 2.73, 2.01, 1.46, 0.75, 1.4, 2.24, 1.02, 1.97, 0.38, 1.2, 1.68, 0.91, 0.25, 1.02, -0.31, 1.06, 0.42, 0.72, -0.29, 0.75, 0.24, 0.02, 0.2, 1.04, 0.79, 0.35, 0.39, -0.88, 0.96, -0.12, 0.71, 0.42, -0.01, 0.72, -0.73, -0.02, 0.91, -0.12, -0.55, 0.3, 0.35, 0.0, -0.13, -0.7, 0.19, -0.25, -0.37, -0.22, -0.42, -0.08, 0.5, -0.69, -0.48, 0.02, 0.34, 0.54, -0.22, -0.57, 0.02];
    var NCH = 256, V0 = -32, DV = 0.25, NAT = 2316, CD = 0.0022222, RA0 = 187.5, DEC0 = 12.0;
    var frame = $('#frame'), chan = $('#chan'), chanNum = $('#chanNum');
    var noSpec = state === 'nospectral', noWcs = state === 'nowcs';
    var cur = { x: 0.61, y: 0.43 }, ch = 128;
    if (state === 'loading') ch = 128;
    var planes = [-12, -4, 0, 4, 12];
    var vOf = function (i) { return V0 + DV * i; };
    function planeFor(i) {
      var v = vOf(i), best = planes[0];
      planes.forEach(function (p) { if (Math.abs(p - v) < Math.abs(best - v)) best = p; });
      return best;
    }
    function setText(id, t) { var e = document.getElementById(id); if (e) e.textContent = t; }
    function setNA(id, t) { var e = document.getElementById(id); if (e) { e.textContent = t; e.className = 'na'; } }
    function setVal(id, t) { var e = document.getElementById(id); if (e) { e.textContent = t; e.className = ''; } }

    function drawSpectrum() {
      var svg = $('#specSvg'), W = 360, H = 176, L = 44, R = 8, T = 8, B = 36;
      var xmin = noSpec ? 0 : V0, xmax = noSpec ? NCH - 1 : V0 + DV * (NCH - 1);
      var ymin = -2, ymax = 20;
      var X = function (x) { return L + (x - xmin) / (xmax - xmin) * (W - L - R); };
      var Y = function (y) { return T + (1 - (y - ymin) / (ymax - ymin)) * (H - T - B); };
      var s = '';
      [0, 5, 10, 15, 20].forEach(function (y) {
        s += '<line class="grid" x1="' + L + '" x2="' + (W - R) + '" y1="' + Y(y) + '" y2="' + Y(y) + '"/><text x="' + (L - 6) + '" y="' + (Y(y) + 4) + '" text-anchor="end">' + y + '</text>';
      });
      var xt = noSpec ? [0, 64, 128, 192, 255] : [-30, -15, 0, 15, 30];
      xt.forEach(function (x) {
        s += '<line class="axis" x1="' + X(x) + '" x2="' + X(x) + '" y1="' + (H - B) + '" y2="' + (H - B + 4) + '"/><text x="' + X(x) + '" y="' + (H - B + 17) + '" text-anchor="middle">' + (noSpec ? x : (x < 0 ? '−' + Math.abs(x) : x)) + '</text>';
      });
      s += '<line class="axis" x1="' + L + '" x2="' + L + '" y1="' + T + '" y2="' + (H - B) + '"/><line class="axis" x1="' + L + '" x2="' + (W - R) + '" y1="' + (H - B) + '" y2="' + (H - B) + '"/>';
      s += '<text x="' + (L + (W - L - R) / 2) + '" y="' + (H - 4) + '" text-anchor="middle">' + (noSpec ? 'channel index (velocity unavailable)' : 'velocity (km/s)') + '</text>';
      s += '<text x="10" y="' + (T + 6) + '" >K</text>';
      var gapLo = state === 'error' ? 120 : -1, gapHi = state === 'error' ? 135 : -1;
      var pts = [], segs = [];
      for (var k = 0; k < SPEC.length; k++) {
        var i = 8 + k, x = noSpec ? i : vOf(i);
        if (i >= gapLo && i <= gapHi) { if (pts.length) segs.push(pts); pts = []; continue; }
        pts.push(X(x).toFixed(1) + ',' + Y(SPEC[k]).toFixed(1));
      }
      if (pts.length) segs.push(pts);
      segs.forEach(function (sg) { s += '<polyline class="line" points="' + sg.join(' ') + '"/>'; });
      if (gapLo >= 0) {
        var gx = X(noSpec ? gapLo : vOf(gapLo)), gw = X(noSpec ? gapHi : vOf(gapHi)) - gx;
        s += '<rect x="' + gx + '" y="' + T + '" width="' + gw + '" height="' + (H - T - B) + '" fill="url(#gaphatch)" stroke="var(--bad)" stroke-dasharray="3 2"/>';
        s += '<text x="' + (gx + gw / 2) + '" y="' + (H - B - 8) + '" text-anchor="middle" style="fill:var(--bad)">not fetched</text>';
      }
      s += '<line class="mark" id="specMark" x1="0" x2="0" y1="' + T + '" y2="' + (H - B) + '"/>';
      svg.innerHTML = '<defs><pattern id="gaphatch" width="6" height="6" patternUnits="userSpaceOnUse" patternTransform="rotate(45)"><line x1="0" y1="0" x2="0" y2="6" stroke="var(--bad)" stroke-width="1"/></pattern></defs>' + s;
      svg._X = X; svg._xmin = xmin;
    }
    function buildTable() {
      var th = '<tr><th scope="col">' + (noSpec ? 'channel' : 'v (km/s)') + '</th><th scope="col">T (K)</th></tr>', rows = '';
      for (var k = 0; k < SPEC.length; k += 10) {
        var i = 8 + k;
        if (state === 'error' && i >= 120 && i <= 135) { rows += '<tr><td>' + (noSpec ? i : vOf(i).toFixed(2)) + '</td><td>not fetched</td></tr>'; continue; }
        rows += '<tr><td>' + (noSpec ? i : vOf(i).toFixed(2)) + '</td><td>' + SPEC[k].toFixed(2) + '</td></tr>';
      }
      $('#specTable').innerHTML = '<thead>' + th + '</thead><tbody>' + rows + '</tbody>';
    }
    function mockVal(fx, fy) {
      var x = Math.floor(fx * NAT) + 1, y = Math.floor((1 - fy) * NAT) + 1;
      if (hash(x, y, ch + 7) < 0.012) return { nan: true, x: x, y: y };
      var base = 9 * (0.5 + 0.5 * Math.sin(x / 190 + ch / 21)) * (0.5 + 0.5 * Math.cos(y / 260));
      var v = base + 5 * hash(x >> 4, y >> 4, ch) - 0.9 + (hash(x, y, ch) - 0.5) * 0.8;
      return { v: Math.round(v * 100) / 100, x: x, y: y };
    }
    function readout() {
      var m = mockVal(cur.x, cur.y), fx = cur.x, fy = cur.y;
      frame.style.setProperty('--cx', (fx * 100) + '%'); frame.style.setProperty('--cy', (fy * 100) + '%');
      setVal('rdPix', m.x + ', ' + m.y + '  (of ' + NAT + ' × ' + NAT + ')');
      if (noWcs) {
        setNA('rdRa', 'unavailable (no valid WCS)'); setNA('rdDec', 'unavailable (no valid WCS)');
        setNA('rdRaD', 'unavailable'); setNA('rdDecD', 'unavailable');
      } else {
        var ra = RA0 - (fx * NAT - NAT / 2) * CD / Math.cos(DEC0 * Math.PI / 180), dec = DEC0 + (NAT / 2 - fy * NAT) * CD;
        setVal('rdRa', fmtRA(ra, 2)); setVal('rdDec', fmtDec(dec, 1));
        setVal('rdRaD', ra.toFixed(5) + '°'); setVal('rdDecD', (dec < 0 ? '−' : '+') + Math.abs(dec).toFixed(5) + '°');
      }
      if (state === 'loading') { setNA('rdVal', 'pending (plane not yet fetched)'); setNA('rdMask', 'pending'); }
      else if (state === 'error') { setNA('rdVal', 'unavailable (not 0)'); setNA('rdMask', 'unknown: full-resolution plane missing'); }
      else if (m.nan) { setNA('rdVal', 'NaN (blank pixel, not 0)'); setNA('rdMask', 'invalid: blank'); }
      else {
        setVal('rdVal', m.v.toFixed(2) + ' K  (mock)');
        if (m.v < 0.8) setNA('rdMask', 'masked: below S/N threshold'); else setVal('rdMask', 'valid');
      }
      var mr = document.getElementById('miniRead');
      if (mr) mr.textContent = 'px ' + m.x + ', ' + m.y + ' · ' + (noWcs ? 'sky n/a' : document.getElementById('rdRa').textContent + ' ' + document.getElementById('rdDec').textContent) + ' · ' + document.getElementById('rdVal').textContent;
      var plateLabel = noWcs ? 'pixel ' + m.x + ', ' + m.y : 'cursor ' + m.x + ', ' + m.y;
      frame.setAttribute('aria-label', 'H I channel image plate, ' + plateLabel + '. Left and right arrows change channel. Control plus arrows move the cursor.');
    }
    function setChan(i, fromUser) {
      ch = Math.max(0, Math.min(NCH - 1, i));
      chan.value = ch; chanNum.value = ch;
      var v = vOf(ch), p = planeFor(ch);
      var vTxt = noSpec ? 'unavailable' : fmtV(v) + ' km/s';
      if (noSpec) setNA('rdVel', 'unavailable (missing spectral reference)'); else setVal('rdVel', vTxt);
      setText('velBig', noSpec ? '—' : fmtV(v));
      setText('velUnit', noSpec ? 'velocity unavailable' : 'km/s');
      setText('chBig', 'ch ' + ch);
      setText('chOf', 'of 0–' + (NCH - 1));
      chan.setAttribute('aria-valuetext', 'channel ' + ch + (noSpec ? ', velocity unavailable' : ', velocity ' + v.toFixed(2) + ' kilometres per second'));
      var src = '../_shared/assets/chan_' + (p < 0 ? '-' : '+') + Math.abs(p) + 'kms.png';
      var img = $('#imgBase');
      if (img.getAttribute('src') !== src) img.setAttribute('src', src);
      setText('planeNote', 'Mock: nearest of five synthetic planes (−12, −4, 0, +4, +12 km/s) is drawn; this one is ' + (p < 0 ? '−' : p > 0 ? '+' : '') + Math.abs(p) + ' km/s.');
      var mk = $('#specMark'), svg = $('#specSvg');
      if (mk && svg._X) { var x = svg._X(noSpec ? ch : v); mk.setAttribute('x1', x); mk.setAttribute('x2', x); mk.style.display = (noSpec || (v >= -30 && v <= 30)) ? '' : 'none'; }
      setText('sumText', 'ch ' + ch + ' · ' + (noSpec ? 'v n/a' : fmtV(v) + ' km/s') + ' \u00b7 \u26a0 3 notices');
      readout();
    }
    drawSpectrum(); buildTable();
    chan.addEventListener('input', function () { setChan(+chan.value); });
    chanNum.addEventListener('change', function () { setChan(+chanNum.value || 0); });
    frame.addEventListener('mousemove', function (e) {
      var r = frame.getBoundingClientRect();
      cur.x = Math.min(0.999, Math.max(0, (e.clientX - r.left) / r.width)); cur.y = Math.min(0.999, Math.max(0, (e.clientY - r.top) / r.height));
      readout();
    });
    frame.addEventListener('click', function () { frame.focus(); });

    /* layers */
    var map = { lyMom: '#imgMom', lyMask: '#imgMask', lyHatch: '#hatchWrap' };
    Object.keys(map).forEach(function (id) {
      var cb = document.getElementById(id), el = $(map[id]);
      cb.addEventListener('change', function () { el.hidden = !cb.checked; });
      el.hidden = !cb.checked;
    });
    $('#opMom').addEventListener('input', function (e) { $('#imgMom').style.opacity = e.target.value / 100; });
    $('#opMask').addEventListener('input', function (e) { $('#imgMask').style.opacity = e.target.value / 100; $('#hatchWrap').style.opacity = Math.min(1, e.target.value / 60); });
    /* display */
    function disp() {
      var lo = +$('#vmin').value, hi = +$('#vmax').value, st = $('input[name=stretch]:checked').value;
      setText('cbLo', lo + ' K'); setText('cbHi', hi + ' K'); setText('cbMid', st === 'asinh' ? 'asinh stretch' : 'linear stretch');
    }
    $$('#vmin,#vmax,input[name=stretch]').forEach(function (e) { e.addEventListener('input', disp); e.addEventListener('change', disp); });
    $('#cmap').addEventListener('change', function (e) { $('#cbImg').src = '../_shared/assets/bar_' + e.target.value + '.png'; setText('cmapName', e.target.value); });
    disp();
    /* ROI */
    var roi = $('#roi');
    /* states */
    if (state === 'error') {
      var cv = $('#cached'), im = $('#imgBase'), c2 = cv.getContext('2d');
      var draw = function () { c2.imageSmoothingEnabled = true; c2.drawImage(im, 0, 0, 64, 64); };
      if (im.complete) draw(); else im.addEventListener('load', draw, { once: true });
    }
    var cancel = $('#cancelFetch');
    if (cancel) cancel.addEventListener('click', function () {
      $('#fetchPanel').hidden = true; setText('liveMsg', 'Fetch cancelled. Showing the previously cached plane; no data were changed.');
      $('#liveBox').hidden = false;
    });
    var retry = $('#retryFetch');
    if (retry) retry.addEventListener('click', function () { setText('liveMsg', 'Retrying the byte range (attempt 4 of 4). In this prototype the request is not sent.'); });

    /* keyboard */
    var help = $('#help');
    function toggleRoi(force) { var on = force === undefined ? roi.hidden : force; roi.hidden = !on; $('#roiOn').checked = on; }
    $('#roiOn').addEventListener('change', function (e) { roi.hidden = !e.target.checked; });
    document.addEventListener('keydown', function (e) {
      var t = e.target;
      if (t.matches('input[type=text],input[type=number],input[type=search],input[type=date],textarea,select')) return;
      if (e.metaKey || e.altKey) return;
      var k = e.key, big = e.shiftKey ? 10 : 1;
      if (e.ctrlKey && k.indexOf('Arrow') === 0) {
        var st = big / NAT * 4.5;
        if (k === 'ArrowLeft') cur.x = Math.max(0, cur.x - st); if (k === 'ArrowRight') cur.x = Math.min(0.999, cur.x + st);
        if (k === 'ArrowUp') cur.y = Math.max(0, cur.y - st); if (k === 'ArrowDown') cur.y = Math.min(0.999, cur.y + st);
        readout(); e.preventDefault(); return;
      }
      if (e.ctrlKey) return;
      if (k === 'ArrowLeft') { setChan(ch - big); e.preventDefault(); }
      else if (k === 'ArrowRight') { setChan(ch + big); e.preventDefault(); }
      else if (k === 'Home') { setChan(0); e.preventDefault(); }
      else if (k === 'End') { setChan(NCH - 1); e.preventDefault(); }
      else if (k === 'r' || k === 'R') toggleRoi();
      else if (k === 'm' || k === 'M') { var cb = $('#lyMask'); cb.checked = !cb.checked; cb.dispatchEvent(new Event('change')); }
      else if (k === '?') { if (help && !help.open) help.showModal(); }
      else if (k === 'Escape') { if (document.activeElement === frame) frame.blur(); document.body.classList.remove('drawer-open'); }
    });
    $('#helpBtn').addEventListener('click', function () { help.showModal(); });
    $('#helpClose').addEventListener('click', function () { help.close(); });
    setChan(ch);
  }

  /* ---------- bottom drawer (mobile) ---------- */
  var db = $('.drawerbtn');
  if (db) db.addEventListener('click', function () {
    var open = document.body.classList.toggle('drawer-open');
    db.setAttribute('aria-expanded', String(open));
    $('.dlabel', db).textContent = open ? 'Close inspector' : 'Inspector';
  });

  /* ---------- compare ---------- */
  if (page === 'compare') {
    var frames = $$('.plate__frame[data-link]');
    if (state !== 'blocked') frames.forEach(function (f) {
      f.addEventListener('mousemove', function (e) {
        var r = f.getBoundingClientRect(), x = (e.clientX - r.left) / r.width * 100, y = (e.clientY - r.top) / r.height * 100;
        frames.forEach(function (g) { g.style.setProperty('--cx', x + '%'); g.style.setProperty('--cy', y + '%'); $('.xhair', g).hidden = false; });
      });
    });
    var rb = $('#resampleBtn');
    if (rb) rb.addEventListener('click', function () { $('#policy').hidden = false; $('#policy').scrollIntoView({ block: 'nearest' }); });
  }

  /* ---------- board ---------- */
  if (page === 'board') {
    var role = state === 'owner' ? 'owner' : 'reviewer';
    function setRole(r) {
      role = r; root.dataset.role = r;
      $$('[data-roleBtn]').forEach(function (b) { b.setAttribute('aria-pressed', String(b.dataset.rolebtn === r)); });
    }
    setRole(role);
    $$('[data-roleBtn]').forEach(function (b) { b.addEventListener('click', function () { setRole(b.dataset.rolebtn); }); });
    var dlg = $('#denied');
    $$('.rawlink').forEach(function (b) { b.addEventListener('click', function (e) { e.preventDefault(); dlg.showModal(); }); });
    $$('.closeDlg').forEach(function (b) { b.addEventListener('click', function () { dlg.close(); }); });
    if (state === 'denied') { try { dlg.showModal(); } catch (e) { dlg.setAttribute('open', ''); } }
    var rq = $('#reqBtn'), rf = $('#reqForm');
    if (rq) rq.addEventListener('click', function () { rf.hidden = !rf.hidden; rq.setAttribute('aria-expanded', String(!rf.hidden)); if (!rf.hidden) $('select', rf).focus(); });
    var cf = $('#commentForm');
    if (cf) cf.addEventListener('submit', function (e) {
      e.preventDefault();
      var t = $('textarea', cf); if (!t.value.trim()) { t.focus(); return; }
      var li = document.createElement('li');
      li.innerHTML = '<div class="who"><span>You (' + role + ')</span><span>draft, not saved in prototype</span></div>';
      var p = document.createElement('p'); p.textContent = t.value; li.appendChild(p);
      $('#thread').appendChild(li); t.value = '';
    });
    var rfSend = $('#reqSend');
    if (rfSend) rfSend.addEventListener('click', function (e) { e.preventDefault(); $('#reqMsg').textContent = 'Request recorded for the owner (prototype: nothing is sent). No compute runs on your behalf.'; });
  }

  /* ---------- explorer ---------- */
  if (page === 'explorer') {
    var D = {
      r1: { dims: '2316 × 2316 × 256, BITPIX −32', ax: 'RA---TAN, DEC--TAN, VELO-LSR', un: 'BUNIT K; CUNIT3 m/s (shown as km/s)', warn: ['SPECSYS LSRK declared, not independently checked.', 'BMAJ/BMIN absent: beam-dependent unit conversion unavailable.'], open: true },
      r2: { dims: '2316 × 2316 × 244, BITPIX −32', ax: 'RA---TAN, DEC--TAN, VELO-LSR', un: 'BUNIT K', warn: ['Superseded by v2 (re-gridded). Kept for provenance.'], open: true },
      r3: { dims: '512 × 512, BITPIX −64', ax: 'RA---TAN, DEC--TAN', un: 'BUNIT deg (axial, 0 ≡ 180)', warn: [], open: true },
      r4: { dims: '512 × 512, BITPIX −64', ax: 'RA---TAN, DEC--TAN', un: 'BUNIT deg (axial, 0 ≡ 180)', warn: ['Config hash declared by run manifest, not recomputed.'], open: true },
      r5: { dims: '512 × 512, BITPIX 8', ax: 'RA---TAN, DEC--TAN', un: 'flag (1 = valid)', warn: ['Mask rule inferred from sibling files; no recorded threshold.'], open: true },
      r6: { dims: '2316 × 2316, BITPIX −32', ax: 'RA---TAN, DEC--TAN', un: 'BUNIT K km/s', warn: ['Integration range inferred from filename, not from header.'], open: true },
      r7: { dims: '1024 × 1024 × 180, BITPIX −32', ax: 'no CTYPE keywords', un: 'BUNIT K', warn: ['No celestial or spectral WCS. The viewer shows pixels and channel index only.', 'Provenance unresolved: no manifest found.'], open: true },
      r8: { dims: 'last known: 4096 × 4096 × 700', ax: 'cached catalog entry only', un: 'cached: K', warn: ['Source host unreachable since 2026-10-12. Header values come from the catalog cache and are unverified.'], open: false },
      r9: { dims: 'expected 2316 × 2316 × 256; found truncated', ax: 'header readable, data unreadable', un: 'BUNIT K', warn: ['Corrupt: END card present but HDU data length is 2.0 GiB, header expects 5.1 GiB.', 'Not openable. Re-register from source or restore from backup.'], open: false },
      r10: { dims: '512 × 512, BITPIX −64', ax: 'RA---TAN, DEC--TAN', un: 'BUNIT deg (axial, 0 ≡ 180)', warn: ['Older run; a newer baseline exists (2026-10-28).'], open: true },
      r11: { dims: '1024 × 1024, BITPIX −32', ax: 'RA---TAN, DEC--TAN (not checked)', un: 'BUNIT K km/s', warn: ['CRVAL/CDELT present but reference pixel not validated against a trusted library.'], open: true },
      r12: { dims: '512 × 512, BITPIX −64', ax: 'RA---TAN, DEC--TAN', un: 'BUNIT deg (0–90, smallest axial angle)', warn: ['Derived product; inputs are rows 3 and 4.'], open: true }
    };
    var rows = $$('tbody tr[data-id]');
    function sel(id) {
      rows.forEach(function (r) { r.setAttribute('aria-selected', String(r.dataset.id === id)); });
      var r = rows.filter(function (x) { return x.dataset.id === id; })[0]; if (!r) return;
      var d = D[id], m = $('#detail');
      $('#dName', m).textContent = r.dataset.name; $('#dPath', m).textContent = r.dataset.path; $('#dPath', m).title = r.dataset.path;
      $('#dDims', m).textContent = d.dims; $('#dAx', m).textContent = d.ax; $('#dUn', m).textContent = d.un;
      $('#dSrc', m).textContent = r.dataset.srcLabel;
      var w = $('#dWarn', m); w.innerHTML = '';
      d.warn.forEach(function (t) { var li = document.createElement('li'); li.textContent = t; w.appendChild(li); });
      $('#dNone', m).hidden = d.warn.length > 0;
      var o = $('#dOpen', m); o.setAttribute('aria-disabled', String(!d.open));
      o.setAttribute('href', d.open ? '02-viewer.html' + (id === 'r7' ? '?state=nowcs' : '') : '#');
      o.textContent = d.open ? 'Open in viewer' : 'Cannot open: ' + (id === 'r9' ? 'file is corrupt' : 'source offline');
    }
    rows.forEach(function (r) { $('.nm', r).addEventListener('click', function () { sel(r.dataset.id); }); });
    $$('#dOpen').forEach(function (a) { a.addEventListener('click', function (e) { if (a.getAttribute('aria-disabled') === 'true') e.preventDefault(); }); });
    sel('r1');
    var chips = $$('.chip'), box = $('#find');
    function apply() {
      var act = {};
      chips.forEach(function (c) { if (c.getAttribute('aria-pressed') === 'true') (act[c.dataset.facet] = act[c.dataset.facet] || []).push(c.dataset.val); });
      var text = box.value.trim().toLowerCase(), shown = 0;
      rows.forEach(function (r) {
        var ok = Object.keys(act).every(function (f) { return act[f].indexOf(r.dataset[f]) >= 0; });
        if (ok && text) ok = (r.dataset.name + ' ' + r.dataset.path).toLowerCase().indexOf(text) >= 0;
        r.hidden = !ok; if (ok) shown++;
      });
      $('#count').textContent = shown + ' of ' + rows.length + ' files shown';
      $('#nohit').hidden = shown > 0;
      $('#clear').hidden = !(Object.keys(act).length || text);
    }
    chips.forEach(function (c) {
      c.querySelector('.ct').textContent = rows.filter(function (r) { return r.dataset[c.dataset.facet] === c.dataset.val; }).length;
      c.addEventListener('click', function () { c.setAttribute('aria-pressed', String(c.getAttribute('aria-pressed') !== 'true')); apply(); });
    });
    box.addEventListener('input', apply);
    $('#clear').addEventListener('click', function () { chips.forEach(function (c) { c.setAttribute('aria-pressed', 'false'); }); box.value = ''; apply(); });
    $('#clear2').addEventListener('click', function () { $('#clear').click(); });
    var preset = q.get('wcs'); if (preset) { chips.forEach(function (c) { if (c.dataset.facet === 'wcs' && c.dataset.val === preset) c.setAttribute('aria-pressed', 'true'); }); }
    apply();
  }
})();
