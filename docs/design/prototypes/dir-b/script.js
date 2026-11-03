/* Vesqira prototype B. Disposable. Theme, state switching, panels, command palette, viewer/compare/board/explorer behaviour. */
(function () {
  'use strict';
  var D = window.VQ_DATA || {};
  var SPEC = D.spec || { v: [], t: [] };
  var NCH = 256, V0 = -32, DV = 0.25, geo = null, cur = 128, SAMPLE = { 80: -12, 112: -4, 128: 0, 144: 4, 176: 12 };
  var $ = function (s, r) { return (r || document).querySelector(s); };
  var $$ = function (s, r) { return Array.prototype.slice.call((r || document).querySelectorAll(s)); };
  var body = document.body;
  var page = body.dataset.page;
  var params = new URLSearchParams(location.search);
  function store(k, v) { try { if (v === undefined) return localStorage.getItem(k); localStorage.setItem(k, v); } catch (e) { return null; } }
  var isMobile = function () { return window.matchMedia('(max-width: 599px)').matches; };

  /* ---------- theme ---------- */
  function setTheme(t) {
    document.documentElement.dataset.theme = t; store('vq-b-theme', t);
    var b = $('#theme-toggle'); if (b) { b.setAttribute('aria-pressed', t === 'light' ? 'true' : 'false'); b.title = 'Theme: ' + t + ' (T)'; }
  }
  function toggleTheme() { setTheme(document.documentElement.dataset.theme === 'light' ? 'dark' : 'light'); }
  setTheme(params.get('theme') || store('vq-b-theme') || 'dark');
  var tb = $('#theme-toggle'); if (tb) tb.addEventListener('click', toggleTheme);

  /* ---------- state switching (prototype switcher + ?state=) ---------- */
  var state = params.get('state') || body.dataset.state || 'default';
  body.dataset.state = state;
  var sel = $('#state-select');
  if (sel) {
    sel.value = state;
    sel.addEventListener('change', function () {
      var p = new URLSearchParams(location.search); p.set('state', sel.value);
      location.search = p.toString();
    });
  }
  function applyWhen() {
    $$('[data-when]').forEach(function (e) { e.hidden = e.dataset.when.split(' ').indexOf(body.dataset.state) < 0; });
    $$('[data-unless]').forEach(function (e) { e.hidden = e.dataset.unless.split(' ').indexOf(body.dataset.state) >= 0; });
  }
  applyWhen();

  /* ---------- panels / drawers ---------- */
  function setPanelDefaults() {
    if (isMobile()) { delete body.dataset.drawer; return; }
    body.dataset.explorer = window.innerWidth >= 1360 && body.dataset.explorerDefault !== 'closed' ? 'open' : 'closed';
    body.dataset.inspector = body.dataset.inspector || 'open';
    body.dataset.dock = body.dataset.dock || 'open';
  }
  setPanelDefaults();
  var lastW = window.innerWidth;
  window.addEventListener('resize', function () { if ((lastW >= 1360) !== (window.innerWidth >= 1360) || isMobile() !== (lastW < 600)) setPanelDefaults(); lastW = window.innerWidth; });
  function togglePanel(name) {
    if (isMobile()) { body.dataset.drawer === name ? delete body.dataset.drawer : (body.dataset.drawer = name); syncToggles(); return; }
    var cur = body.dataset[name];
    body.dataset[name] = cur === 'open' ? 'closed' : 'open';
    syncToggles();
    if (page === 'viewer') drawSpectrum();
  }
  function syncToggles() {
    $$('[data-toggle]').forEach(function (b) {
      var n = b.dataset.toggle, open = isMobile() ? body.dataset.drawer === n : body.dataset[n] === 'open';
      b.setAttribute('aria-expanded', open ? 'true' : 'false');
    });
  }
  $$('[data-toggle]').forEach(function (b) { b.addEventListener('click', function () { togglePanel(b.dataset.toggle); }); });
  var scrim = $('.scrim'); if (scrim) scrim.addEventListener('click', function () { delete body.dataset.drawer; syncToggles(); });
  syncToggles();

  /* ---------- tabs ---------- */
  $$('[role="tablist"]').forEach(function (tl) {
    var tabs = $$('[role="tab"]', tl);
    function pick(t, focus) {
      tabs.forEach(function (x) {
        var on = x === t; x.setAttribute('aria-selected', on ? 'true' : 'false'); x.tabIndex = on ? 0 : -1;
        var p = document.getElementById(x.getAttribute('aria-controls')); if (p) { if (p.classList.contains('dpane')) p.classList.toggle('on', on); else p.hidden = !on; }
      });
      if (focus) t.focus();
      if (page === 'viewer') drawSpectrum();
    }
    tabs.forEach(function (t, i) {
      t.addEventListener('click', function () { pick(t); });
      t.addEventListener('keydown', function (e) {
        var k = e.key, n = null;
        if (k === 'ArrowRight') n = tabs[(i + 1) % tabs.length]; else if (k === 'ArrowLeft') n = tabs[(i - 1 + tabs.length) % tabs.length];
        else if (k === 'Home') n = tabs[0]; else if (k === 'End') n = tabs[tabs.length - 1];
        if (n) { e.preventDefault(); e.stopPropagation(); pick(n, true); }
      });
    });
    var init = tabs.filter(function (t) { return t.getAttribute('aria-selected') === 'true'; })[0] || tabs[0];
    pick(init);
  });

  /* ---------- helpers ---------- */
  function f(n, d) { return Number(n).toFixed(d); }
  function sgn(n, d) { return (n >= 0 ? '+' : '−') + f(Math.abs(n), d); }
  function sexRA(deg) { var h = deg / 15, hh = Math.floor(h), m = (h - hh) * 60, mm = Math.floor(m), s = (m - mm) * 60; return pad(hh) + 'h ' + pad(mm) + 'm ' + f(s, 2).padStart(5, '0') + 's'; }
  function sexDec(deg) { var a = Math.abs(deg), d = Math.floor(a), m = (a - d) * 60, mm = Math.floor(m), s = (m - mm) * 60; return (deg < 0 ? '−' : '+') + pad(d) + '° ' + pad(mm) + '′ ' + f(s, 1).padStart(4, '0') + '″'; }
  function pad(n) { return String(n).padStart(2, '0'); }
  var NPIX = 2304, G = D.grid || 64;
  /* Fictional SIN WCS: CRPIX 1152.5, CDELT 10 arcsec, CRVAL (187.5, +12.5). Mock only. */
  function pix2sky(px, py) {
    var cd = 10 / 3600, r = Math.PI / 180, l = (px - 1152.5) * -cd * r, m = (py - 1152.5) * cd * r;
    var d0 = 12.5 * r, n = Math.sqrt(1 - l * l - m * m);
    var dec = Math.asin(m * Math.cos(d0) + n * Math.sin(d0));
    var ra = 187.5 * r + Math.atan2(l, n * Math.cos(d0) - m * Math.sin(d0));
    return { ra: (ra / r + 360) % 360, dec: dec / r };
  }

  /* ---------- viewer ---------- */

  function FLAG(i) { return i < 8 || i > 247 || (i >= 120 && i <= 124) || (i >= 196 && i <= 199); }
  var noSpec = state === 'no-spectral', noWcs = state === 'no-wcs';
  var chRO = null;
  function vel(i) { return V0 + DV * i; }
  function nearestSample(i) { var best = 128, bd = 1e9; Object.keys(SAMPLE).forEach(function (k) { var d = Math.abs(k - i); if (d < bd) { bd = d; best = +k; } }); return best; }
  function chanLabel(i) { return noSpec ? 'ch ' + i : 'ch ' + i + ' · ' + sgn(vel(i), 2) + ' km/s'; }
  function buildTimeline() {
    var host = $('#tl'); if (!host) return;
    var s = '<svg viewBox="0 0 256 36" preserveAspectRatio="none" aria-hidden="true" focusable="false">';
    for (var i = 0; i < NCH; i++) {
      if (FLAG(i)) s += '<rect x="' + i + '" y="29" width="1" height="5" fill="var(--warn)"/>';
      else s += '<rect x="' + i + '" y="6" width="0.8" height="22" fill="var(--cyan)" opacity=".8"/>';
    }
    Object.keys(SAMPLE).forEach(function (k) { s += '<rect x="' + (+k - 0.6) + '" y="1" width="2.2" height="4" fill="var(--copper)"/>'; });
    host.innerHTML = s + '</svg><div class="play" id="play"></div>';
    var drag = false;
    function at(e) { var r = host.getBoundingClientRect(); setChannel(Math.round(Math.min(1, Math.max(0, (e.clientX - r.left) / r.width)) * (NCH - 1))); }
    host.addEventListener('pointerdown', function (e) { drag = true; host.setPointerCapture(e.pointerId); at(e); host.focus(); });
    host.addEventListener('pointermove', function (e) { if (drag) at(e); });
    host.addEventListener('pointerup', function () { drag = false; });
    $$('[data-step]').forEach(function (b) { b.addEventListener('click', function () { setChannel(cur + (+b.dataset.step)); }); });
  }
  function setChannel(i) {
    cur = Math.max(0, Math.min(NCH - 1, i));
    var tl = $('#tl'); if (!tl) return;
    $('#play').style.left = (cur / (NCH - 1) * 100) + '%';
    var flagged = FLAG(cur), samp = nearestSample(cur), sv = SAMPLE[samp];
    tl.setAttribute('aria-valuenow', cur);
    tl.setAttribute('aria-valuetext', 'Channel ' + cur + ' of ' + NCH + (noSpec ? '' : ', ' + sgn(vel(cur), 2) + ' km/s') + (flagged ? ', flagged invalid' : ', valid'));
    var el = $('#tl-cur'); if (el) el.textContent = noSpec ? 'ch ' + cur + ' / ' + (NCH - 1) : 'ch ' + cur + ' / ' + (NCH - 1) + ' · ' + sgn(vel(cur), 2) + ' km/s';
    var fl = $('#tl-flag'); if (fl) { fl.textContent = flagged ? 'flagged (invalid)' : 'valid'; fl.className = 'badge ' + (flagged ? 'b-warn' : 'b-ok'); }
    var img = $('#plane-img'); if (img) img.src = '../_shared/assets/chan_' + (sv >= 0 ? '+' : '-') + Math.abs(sv) + 'kms.png';
    var st = $('.stage'); if (st) st.classList.toggle('flagged', flagged);
    var h = $('#h-plane'); if (h) h.textContent = chanLabel(cur);
    var hv = $('#h-vel'); if (hv) { if (noSpec) { hv.innerHTML = '<em class="na">unavailable</em>'; } else hv.textContent = sgn(vel(cur), 2) + ' km/s'; }
    var hs = $('#h-sample'); if (hs) hs.textContent = (samp === cur ? 'sample plane' : 'nearest sample plane') + (noSpec ? ' (ch ' + samp + ')' : ' (' + sgn(sv, 0) + ' km/s)');
    var fh = $('#flag-msg'); if (fh) fh.hidden = !flagged;
    drawSpectrumCursor();
    var rows = $$('#spec-table tbody tr'); rows.forEach(function (r) { r.classList.toggle('sel', +r.dataset.ch === cur); });
    var sr = $('#spec-table tbody tr.sel'); if (sr) { var box = sr.closest('.dpane'); if (box && box.offsetParent) { var top = sr.offsetTop; box.scrollTop = Math.max(0, top - box.clientHeight / 2); } }
    if (chRO) updateReadout(chRO.fx, chRO.fy, true);
  }
  var lastFx = 0.5, lastFy = 0.5;
  function updateReadout(fx, fy, keep) {
    lastFx = fx; lastFy = fy; chRO = { fx: fx, fy: fy };
    var px = Math.min(NPIX, Math.floor(fx * NPIX) + 1), py = Math.min(NPIX, Math.floor((1 - fy) * NPIX) + 1);
    var gx = Math.min(G - 1, Math.floor(fx * G)), gy = Math.min(G - 1, Math.floor(fy * G));
    var samp = nearestSample(cur), grid = D['c' + SAMPLE[samp]];
    var set = function (id, t) { var e = document.getElementById(id); if (e) e.textContent = t; };
    set('r-x', String(px)); set('r-y', String(py));
    var flagged = FLAG(cur), invalid = D.invalid ? D.invalid[gy * G + gx] === 1 : false;
    var K = grid ? (-0.3 + 18.6 * grid[gy * G + gx] / 255) : 0;
    var valTxt, maskTxt;
    if (state === 'loading') { valTxt = 'pending'; maskTxt = 'pending'; }
    else if (flagged) { valTxt = 'flagged'; maskTxt = 'channel flagged'; }
    else if (invalid) { valTxt = 'blank (not 0)'; maskTxt = 'masked invalid'; }
    else { valTxt = f(K, 2) + ' K'; maskTxt = 'valid'; }
    if (state === 'error') { valTxt += ' (cached)'; }
    set('r-val', valTxt); set('r-mask', maskTxt);
    var sky = null;
    if (!noWcs) {
      sky = pix2sky(px, py);
      set('r-ra', sexRA(sky.ra)); set('r-dec', sexDec(sky.dec)); set('r-radeg', f(sky.ra, 4) + '°'); set('r-decdeg', sgn(sky.dec, 4) + '°');
    }
    var sb = document.getElementById('st-readout');
    if (sb) sb.textContent = 'x ' + px + ' y ' + py + ' · ' + (noWcs ? 'sky n/a' : sexRA(sky.ra).replace(/ /g, '') + ' ' + sexDec(sky.dec).replace(/ /g, '')) + ' · ' + valTxt + ' (mock)';
    var xh = $('.xhair'); if (xh) { xh.style.setProperty('--cx', (fx * 100) + '%'); xh.style.setProperty('--cy', (fy * 100) + '%'); }
  }
  function initViewer() {
    buildTimeline();
    var st = $('.stage');
    if (state === 'loading') st.classList.add('stale');
    if (state === 'error') st.classList.add('cached');
    st.addEventListener('pointermove', function (e) { var r = st.getBoundingClientRect(); st.classList.add('hot'); updateReadout((e.clientX - r.left) / r.width, (e.clientY - r.top) / r.height); });
    st.addEventListener('pointerleave', function () { if (!st.classList.contains('pinned')) st.classList.remove('hot'); });
    st.addEventListener('click', function () { st.classList.toggle('pinned'); var p = $('#pin-state'); if (p) p.textContent = st.classList.contains('pinned') ? 'pinned' : 'live'; });
    updateReadout(0.5, 0.5); $('#pin-state') && ($('#pin-state').textContent = 'live');
    setChannel(cur);
    buildSpectrum();
    var cm = $('#cm'); if (cm) cm.addEventListener('change', function () {
      var b = cm.value === 'cividis' ? 'bar_cividis.png' : 'bar_magma.png';
      $$('.cbar img, #insp-bar').forEach(function (i) { i.src = '../_shared/assets/' + b; });
      var hc = $('#h-cmap'); if (hc) hc.textContent = cm.value;
    });
    $$('.layer[data-target]').forEach(function (L) {
      var t = $(L.dataset.target), eye = $('.eye', L), rg = $('input[type=range]', L), op = $('.op', L);
      if (!t) return;
      eye.addEventListener('click', function () { var on = eye.getAttribute('aria-pressed') !== 'true'; eye.setAttribute('aria-pressed', on); t.style.display = on ? '' : 'none'; });
      rg.addEventListener('input', function () { t.style.opacity = rg.value / 100; op.textContent = rg.value + '%'; });
    });
    $$('[data-act="cancel"]').forEach(function (b) { b.addEventListener('click', function () { var m = b.closest('.overlay-msg'); m.innerHTML = '<div class="row"><b>Load cancelled.</b> <span class="dim">Showing the cached low-resolution preview; no further bytes requested.</span> <button class="btn sm" type="button">Retry</button></div>'; }); });
    $$('[data-act]').forEach(function (b) { b.addEventListener('click', function () { act(b.dataset.act, b); }); });
    window.addEventListener('resize', drawSpectrum);
    if (window.ResizeObserver) new ResizeObserver(drawSpectrum).observe($('#spec-host'));
  }
  function act(name, el) {
    if (name === 'roi') { body.dataset.roi = body.dataset.roi === 'off' ? 'on' : 'off'; $$('[data-act="roi"]').forEach(function (b) { b.setAttribute('aria-pressed', body.dataset.roi === 'off' ? 'false' : 'true'); }); }
    if (name === 'grid') { var g = $('.stage .grid'); if (g) { g.hidden = !g.hidden; $$('[data-act="grid"]').forEach(function (b) { b.setAttribute('aria-pressed', g.hidden ? 'false' : 'true'); }); } }
    if (name === 'stretch-linear' || name === 'stretch-asinh') {
      var asinh = name === 'stretch-asinh'; $$('[data-act^="stretch-"]').forEach(function (b) { b.setAttribute('aria-checked', b.dataset.act === name ? 'true' : 'false'); });
      var hs = $('#h-stretch'); if (hs) hs.textContent = asinh ? 'asinh' : 'linear';
    }
    if (name === 'dismiss') { var s = el.closest('.strip'); if (s) s.hidden = true; }
  }
  function specX(i) { return noSpec ? i : vel(i); }
  function specIndex(k) { return Math.round((SPEC.v[k] - V0) / DV); }
  function buildSpectrum() {
    var tb = $('#spec-table tbody'); if (!tb) return;
    var h = '';
    for (var k = 0; k < SPEC.v.length; k++) {
      var i = specIndex(k);
      h += '<tr data-ch="' + i + '"><td class="r">' + i + '</td>' + (noSpec ? '<td class="r">n/a</td>' : '<td class="r">' + f(SPEC.v[k], 2) + '</td>') + '<td class="r">' + f(SPEC.t[k], 2) + '</td><td>' + (FLAG(i) ? 'flagged' : 'valid') + '</td></tr>';
    }
    tb.innerHTML = h;
    var mx = 0; for (var q = 1; q < SPEC.t.length; q++) if (SPEC.t[q] > SPEC.t[mx]) mx = q;
    var svg = $('#spec-svg'); if (svg) svg.setAttribute('aria-label', 'Region-mean spectrum for ROI 1, mock data. Brightness temperature in K against ' + (noSpec ? 'channel index' : 'LSR velocity in km/s') + '. Maximum ' + f(SPEC.t[mx], 1) + ' K at ' + (noSpec ? 'channel ' + specIndex(mx) : sgn(SPEC.v[mx], 2) + ' km/s') + '. A data table is available in the Data table tab.');
    drawSpectrum();
  }
  function drawSpectrum() {
    var svg = $('#spec-svg'); if (!svg || !SPEC.v.length) return;
    var host = $('#spec-host'); var W = host.clientWidth, H = host.clientHeight;
    if (W < 40 || H < 40) return;
    var m = { l: 40, r: 10, t: 8, b: 30 }, pw = W - m.l - m.r, ph = H - m.t - m.b;
    var x0 = noSpec ? 0 : -32, x1 = noSpec ? 255 : 32, y0 = -2, y1 = 20;
    var X = function (x) { return m.l + (x - x0) / (x1 - x0) * pw; }, Y = function (y) { return m.t + (1 - (y - y0) / (y1 - y0)) * ph; };
    var s = '';
    var xt = noSpec ? [0, 64, 128, 192, 255] : [-30, -20, -10, 0, 10, 20, 30];
    xt.forEach(function (t) { s += '<line class="gr" x1="' + X(t) + '" x2="' + X(t) + '" y1="' + m.t + '" y2="' + (m.t + ph) + '"/><text x="' + X(t) + '" y="' + (H - 14) + '" text-anchor="middle">' + (noSpec ? t : (t > 0 ? '+' : t < 0 ? '−' : '') + Math.abs(t)) + '</text>'; });
    [0, 5, 10, 15, 20].forEach(function (t) { s += '<line class="gr" x1="' + m.l + '" x2="' + (m.l + pw) + '" y1="' + Y(t) + '" y2="' + Y(t) + '"/><text x="' + (m.l - 6) + '" y="' + (Y(t) + 4) + '" text-anchor="end">' + t + '</text>'; });
    for (var i = 0; i < NCH; i++) if (FLAG(i) && i >= 8 && i <= 247) { var xv = specX(i); s += '<rect class="band" x="' + (X(xv) - 0.5) + '" y="' + m.t + '" width="' + Math.max(1.5, pw / NCH) + '" height="' + ph + '"/>'; }
    var pts = SPEC.v.map(function (v, k) { return X(specX(specIndex(k))).toFixed(1) + ',' + Y(SPEC.t[k]).toFixed(1); }).join(' ');
    s += '<line class="ax" x1="' + m.l + '" x2="' + (m.l + pw) + '" y1="' + (m.t + ph) + '" y2="' + (m.t + ph) + '"/><line class="ax" x1="' + m.l + '" x2="' + m.l + '" y1="' + m.t + '" y2="' + (m.t + ph) + '"/>';
    s += '<polyline class="ln" points="' + pts + '"/>';
    s += '<text x="' + (m.l + pw / 2) + '" y="' + (H - 1) + '" text-anchor="middle">' + (noSpec ? 'channel index (velocity unavailable)' : 'v_LSR [km/s]') + '</text>';
    s += '<text x="12" y="' + (m.t + ph / 2) + '" text-anchor="middle" transform="rotate(-90 12 ' + (m.t + ph / 2) + ')">T_b [K]</text>';
    s += '<line class="cur" id="spec-cur" y1="' + m.t + '" y2="' + (m.t + ph) + '"/>';
    svg.setAttribute('viewBox', '0 0 ' + W + ' ' + H); svg.innerHTML = s; geo = { X: X };
    drawSpectrumCursor();
  }
  function drawSpectrumCursor() {
    var c = $('#spec-cur'); if (!c || !geo) return;
    var x = geo.X(specX(cur)); c.setAttribute('x1', x); c.setAttribute('x2', x);
  }

  /* ---------- compare ---------- */
  function initCompare() {
    var stages = $$('.stage[data-cmp]'); var blocked = state === 'blocked';
    var hatch = $$('.hatch'); var tg = $('#tg-hatch');
    function ang(k, g) { return D[k][g] / 10; }
    function axd(a, b) { var d = Math.abs(a - b) % 180; return Math.min(d, 180 - d); }
    stages.forEach(function (st) {
      st.addEventListener('pointermove', function (e) {
        var r = st.getBoundingClientRect(), fx = (e.clientX - r.left) / r.width, fy = (e.clientY - r.top) / r.height;
        var gx = Math.min(G - 1, Math.floor(fx * G)), gy = Math.min(G - 1, Math.floor(fy * G)), gi = gy * G + gx;
        var inv = D.invalid[gi] === 1, a = ang('base', gi), b = ang('cand', gi), d = axd(a, b);
        var px = Math.floor(fx * 512) + 1, py = Math.floor((1 - fy) * 512) + 1;
        stages.forEach(function (o) {
          var link = !blocked || o === st;
          o.classList.toggle('hot', link);
          if (link) { var xh = $('.xhair', o); xh.style.setProperty('--cx', fx * 100 + '%'); xh.style.setProperty('--cy', fy * 100 + '%'); }
        });
        var ro = function (id, t) { var e2 = document.getElementById(id); if (e2) e2.textContent = t; };
        var base = 'x ' + px + ' y ' + py + (inv ? ' · INVALID (masked)' : ' · valid');
        ro('ro-base', base + ' · θ ' + f(a, 1) + '° ≡ ' + f(a + 180, 1) + '°');
        ro('ro-cand', blocked ? 'grid differs: no linked pixel' : base + ' · θ ' + f(b, 1) + '°');
        ro('ro-diff', blocked ? 'withheld' : (inv ? 'masked: no difference (not 0)' : 'Δθ ' + f(d, 1) + '° (axial, 0–90°)'));
        var sbr = document.getElementById('st-readout'); if (sbr) sbr.textContent = 'x ' + px + ' y ' + py + ' · ' + (inv ? 'invalid' : 'θb ' + f(a, 1) + '° θc ' + f(b, 1) + '°') + ' (mock)';
      });
      st.addEventListener('pointerleave', function () { stages.forEach(function (o) { o.classList.remove('hot'); }); });
    });
    var ht = $('#tg-hatch');
    if (ht) ht.addEventListener('click', function () { var on = ht.getAttribute('aria-pressed') !== 'true'; ht.setAttribute('aria-pressed', on); hatch.forEach(function (h) { h.hidden = !on; }); });
    var stats = D.stats || {};
    var set = function (id, t) { var e = document.getElementById(id); if (e) e.textContent = t; };
    set('s-valid', f((stats.valid_fraction || 0) * 100, 0) + '%'); set('s-med', f(stats.axial_diff_median_deg, 2) + '°'); set('s-p90', f(stats.axial_diff_p90_deg, 2) + '°');
    set('s-mean', f(Math.abs(stats.axial_mean_1_179_deg || 0) < 1e-9 ? 0 : stats.axial_mean_1_179_deg, 2) + '°');
  }

  /* ---------- explorer ---------- */
  function initExplorer() {
    var rows = $$('.tree .row'), work = $('.work.explore');
    function visibleRows() { return rows.filter(function (r) { return r.offsetParent !== null; }); }
    function show(id, userClick) {
      $$('.detail').forEach(function (d) { d.hidden = d.id !== 'd-' + id; });
      rows.forEach(function (r) { var on = r.dataset.detail === id; if (r.dataset.detail) { r.setAttribute('aria-selected', on ? 'true' : 'false'); r.tabIndex = on ? 0 : -1; } });
      if (userClick && isMobile()) work.dataset.mview = 'detail';
    }
    rows.forEach(function (r) {
      r.addEventListener('click', function () {
        if (r.dataset.detail) show(r.dataset.detail, true);
        else { var li = r.parentElement, ex = li.getAttribute('data-expanded') === 'true' ? 'false' : 'true'; li.setAttribute('data-expanded', ex); r.setAttribute('aria-expanded', ex); }
      });
      r.addEventListener('keydown', function (e) {
        var v = visibleRows(), i = v.indexOf(r), li = r.parentElement;
        if (e.key === 'ArrowDown' && v[i + 1]) { e.preventDefault(); v[i + 1].focus(); }
        else if (e.key === 'ArrowUp' && v[i - 1]) { e.preventDefault(); v[i - 1].focus(); }
        else if (e.key === 'ArrowLeft' && li.hasAttribute('data-expanded')) { e.preventDefault(); li.setAttribute('data-expanded', 'false'); r.setAttribute('aria-expanded', 'false'); }
        else if (e.key === 'ArrowRight' && li.hasAttribute('data-expanded')) { e.preventDefault(); li.setAttribute('data-expanded', 'true'); r.setAttribute('aria-expanded', 'true'); }
        else if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); r.click(); }
      });
    });
    var first = params.get('item') || 'v2'; show(first);
    var b = $('.backbtn'); if (b) b.addEventListener('click', function () { work.dataset.mview = 'tree'; });
    work.dataset.mview = 'tree'; if (params.get('item') && isMobile()) work.dataset.mview = 'detail';
    var q = $('#tfilter'), chips = $$('.chip[data-f]'), flt = 'all';
    function filt() {
      var t = (q.value || '').toLowerCase();
      rows.forEach(function (r) {
        if (!r.dataset.detail) return;
        var ok = (!t || r.textContent.toLowerCase().indexOf(t) >= 0) && (flt === 'all' || r.dataset.issue === '1');
        r.parentElement.hidden = !ok;
      });
    }
    if (q) q.addEventListener('input', filt);
    chips.forEach(function (c) { c.addEventListener('click', function () { flt = c.dataset.f; chips.forEach(function (x) { x.setAttribute('aria-pressed', x === c ? 'true' : 'false'); }); filt(); }); });
  }

  /* ---------- board ---------- */
  function setRole(r) {
    body.dataset.role = r; var s = $('#state-select');
    $$('#role-toggle button').forEach(function (b) { b.setAttribute('aria-pressed', b.dataset.role === r ? 'true' : 'false'); });
    if (r === 'owner' && body.dataset.state !== 'owner') { body.dataset.state = 'owner'; }
    if (r === 'reviewer' && body.dataset.state === 'owner') { body.dataset.state = 'reviewer'; }
    if (s) s.value = body.dataset.state; applyWhen();
    $$('.rail a[data-rail="boards"]').forEach(function (a) { if (r === 'owner') a.setAttribute('aria-current', 'page'); else a.removeAttribute('aria-current'); });
    $$('.rail a[data-rail="review"]').forEach(function (a) { if (r === 'reviewer') a.setAttribute('aria-current', 'page'); else a.removeAttribute('aria-current'); });
  }
  function initBoard() {
    var role = state === 'owner' ? 'owner' : (params.get('role') || (state === 'reviewer' || state === 'denied' ? 'reviewer' : 'owner'));
    if (!params.get('state')) { body.dataset.state = role === 'owner' ? 'owner' : 'reviewer'; if (sel) sel.value = body.dataset.state; }
    setRole(role);
    $$('#role-toggle button').forEach(function (b) { b.addEventListener('click', function () { setRole(b.dataset.role); }); });
    $$('.pin').forEach(function (p) {
      p.addEventListener('click', function () {
        $$('.pin').forEach(function (o) { o.setAttribute('aria-pressed', o === p ? 'true' : 'false'); });
        $$('.thread').forEach(function (t) { var on = t.id === p.dataset.thread; if (on) t.setAttribute('aria-current', 'true'); else t.removeAttribute('aria-current'); if (on && t.offsetParent) t.scrollIntoView({ block: 'nearest' }); });
      });
    });
    $$('.thread[id]').forEach(function (t) { t.setAttribute('tabindex', '-1'); });
    $$('[data-raw-link]').forEach(function (a) { a.addEventListener('click', function (e) { e.preventDefault(); body.dataset.state = 'denied'; setRole('reviewer'); var s = $('#state-select'); if (s) s.value = 'denied'; applyWhen(); var h = $('#denied-h'); if (h) h.focus(); }); });
    $$('[data-back-review]').forEach(function (a) { a.addEventListener('click', function (e) { e.preventDefault(); body.dataset.state = 'reviewer'; var s = $('#state-select'); if (s) s.value = 'reviewer'; applyWhen(); }); });
    var rq = $('#req-cmp'), rf = $('#req-form'); if (rq) rq.addEventListener('click', function () { rf.hidden = !rf.hidden; rq.setAttribute('aria-expanded', rf.hidden ? 'false' : 'true'); });
    var send = $('#req-send'); if (send) send.addEventListener('click', function () { $('#req-msg').hidden = false; });
    var add = $('#cmt-add'); if (add) add.addEventListener('click', function () {
      var t = $('#cmt-text'); if (!t.value.trim()) { t.focus(); return; }
      var d = document.createElement('div'); d.className = 'thread';
      var who = body.dataset.role === 'owner' ? 'You (owner)' : 'You (reviewer)';
      d.innerHTML = '<div class="meta"><b></b><span>just now · on panel B · draft, prototype only</span></div><p></p>';
      d.querySelector('b').textContent = who; d.querySelector('p').textContent = t.value; $('#threads').appendChild(d); t.value = '';
    });
  }

  /* ---------- command palette ---------- */
  var pal = $('#palette-back'), pin = $('#palette-input'), plist = $('#palette-list'), cmds = [], shown = [], pidx = 0, prevFocus = null;
  function nav(href) { return function () { location.href = href; }; }
  function buildCmds() {
    cmds = [
      { g: 'Go', n: 'Go to Datasets (explorer)', h: 'datasets files tree', run: nav('01-explorer.html') },
      { g: 'Go', n: 'Go to Viewer: MOCK_HI_cube_L1234_v2.fits', h: 'viewer cube channel', run: nav('02-viewer.html') },
      { g: 'Go', n: 'Go to Compare: baseline vs candidate', h: 'compare vgt', run: nav('03-compare.html') },
      { g: 'Go', n: 'Go to Boards (owner)', h: 'board figure', run: nav('04-board.html') },
      { g: 'Go', n: 'Go to Review (reviewer view, read-only)', h: 'review supervisor', run: nav('04-board.html?state=reviewer') },
      { g: 'View', n: 'Toggle theme dark / light', h: 'theme light dark', k: 'T', run: toggleTheme },
      { g: 'View', n: 'Toggle explorer panel', h: 'panel left', k: 'E', run: function () { togglePanel('explorer'); } },
      { g: 'View', n: 'Toggle inspector panel', h: 'panel right', k: 'I', run: function () { togglePanel('inspector'); } },
      { g: 'View', n: 'Toggle bottom dock', h: 'panel spectrum', k: 'D', run: function () { togglePanel('dock'); } }
    ];
    if (page === 'viewer') {
      cmds.push(
        { g: 'Channel', n: 'Next channel', h: 'step forward right', k: '→ / K', run: function () { setChannel(cur + 1); } },
        { g: 'Channel', n: 'Previous channel', h: 'step back left', k: '← / J', run: function () { setChannel(cur - 1); } },
        { g: 'Channel', n: 'Jump +10 channels', h: 'step forward', k: 'Shift →', run: function () { setChannel(cur + 10); } },
        { g: 'Channel', n: 'Jump to first channel', h: 'home', k: 'Home', run: function () { setChannel(0); } },
        { g: 'Channel', n: 'Jump to last channel', h: 'end', k: 'End', run: function () { setChannel(NCH - 1); } },
        { g: 'Channel', n: 'Jump to channel 128 (v = 0 km/s)', h: 'zero velocity', run: function () { setChannel(128); } },
        { g: 'Display', n: 'Stretch: asinh', h: 'stretch contrast', run: function () { act('stretch-asinh'); } },
        { g: 'Display', n: 'Stretch: linear', h: 'stretch contrast', run: function () { act('stretch-linear'); } },
        { g: 'Region', n: 'Toggle ROI rectangle', h: 'region', k: 'R', run: function () { act('roi'); } },
        { g: 'Cursor', n: 'Copy cursor coordinates', h: 'copy pixel ra dec', run: function () { var t = (document.getElementById('st-readout') || {}).textContent || ''; try { navigator.clipboard.writeText(t); } catch (e) {} } }
      );
    }
    if (page === 'explorer') cmds.push({ g: 'Dataset', n: 'Register existing FITS file or folder…', h: 'register add', run: function () {} }, { g: 'Dataset', n: 'Open selected dataset in viewer', h: 'open', run: nav('02-viewer.html') });
    if (page === 'compare') cmds.push({ g: 'Compare', n: 'Toggle invalid-pixel hatch overlay', h: 'hatch reliability mask', run: function () { var b = $('#tg-hatch'); if (b) b.click(); } }, { g: 'Compare', n: 'Show blocked-comparison state', h: 'incompatible wcs', run: nav('03-compare.html?state=blocked') });
    if (page === 'board') cmds.push({ g: 'Board', n: 'Switch to reviewer view', h: 'role', run: function () { setRole('reviewer'); } }, { g: 'Board', n: 'Switch to owner view', h: 'role', run: function () { setRole('owner'); } });
  }
  function renderPalette() {
    var q = pin.value.toLowerCase().split(/\s+/).filter(Boolean);
    shown = cmds.filter(function (c) { var t = (c.g + ' ' + c.n + ' ' + (c.h || '')).toLowerCase(); return q.every(function (w) { return t.indexOf(w) >= 0; }); });
    pidx = Math.min(pidx, Math.max(0, shown.length - 1));
    plist.innerHTML = shown.length ? '' : '<li class="none" role="presentation">No matching command. Try “channel”, “theme” or “compare”.</li>';
    shown.forEach(function (c, i) {
      var li = document.createElement('li'); li.id = 'pc-' + i; li.setAttribute('role', 'option'); li.setAttribute('aria-selected', i === pidx ? 'true' : 'false');
      li.innerHTML = '<span class="grp"></span><span class="nm"></span>' + (c.k ? '<kbd></kbd>' : '');
      li.children[0].textContent = c.g; li.children[1].textContent = c.n; if (c.k) li.children[2].textContent = c.k;
      li.addEventListener('click', function () { runCmd(i); }); li.addEventListener('pointermove', function () { if (pidx !== i) { pidx = i; markSel(); } });
      plist.appendChild(li);
    });
    pin.setAttribute('aria-activedescendant', shown.length ? 'pc-' + pidx : '');
  }
  function markSel() { $$('li[role=option]', plist).forEach(function (l, i) { l.setAttribute('aria-selected', i === pidx ? 'true' : 'false'); }); pin.setAttribute('aria-activedescendant', 'pc-' + pidx); var a = $('#pc-' + pidx); if (a) a.scrollIntoView({ block: 'nearest' }); }
  function openPalette() { if (!pal) return; prevFocus = document.activeElement; buildCmds(); pal.classList.add('open'); pin.value = ''; pidx = 0; renderPalette(); pin.focus(); }
  function closePalette() { if (!pal || !pal.classList.contains('open')) return false; pal.classList.remove('open'); if (prevFocus && prevFocus.focus) prevFocus.focus(); return true; }
  function runCmd(i) { var c = shown[i]; if (!c) return; closePalette(); c.run(); }
  if (pal) {
    pin.addEventListener('input', function () { pidx = 0; renderPalette(); });
    pal.addEventListener('mousedown', function (e) { if (e.target === pal) closePalette(); });
    pal.addEventListener('keydown', function (e) {
      if (e.key === 'Escape') { e.preventDefault(); closePalette(); }
      else if (e.key === 'ArrowDown') { e.preventDefault(); if (shown.length) { pidx = (pidx + 1) % shown.length; markSel(); } }
      else if (e.key === 'ArrowUp') { e.preventDefault(); if (shown.length) { pidx = (pidx - 1 + shown.length) % shown.length; markSel(); } }
      else if (e.key === 'Enter') { e.preventDefault(); runCmd(pidx); }
      else if (e.key === 'Tab') { e.preventDefault(); pin.focus(); }
    });
  }
  $$('[data-open-palette]').forEach(function (b) { b.addEventListener('click', openPalette); });

  /* ---------- global keys ---------- */
  document.addEventListener('keydown', function (e) {
    if (e.key === 'Escape') { if (closePalette()) return; if (body.dataset.drawer) { delete body.dataset.drawer; syncToggles(); } return; }
    if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'k') { e.preventDefault(); pal.classList.contains('open') ? closePalette() : openPalette(); return; }
    if (pal && pal.classList.contains('open')) return;
    var t = e.target, tag = (t.tagName || '').toLowerCase();
    if (tag === 'input' || tag === 'select' || tag === 'textarea' || t.isContentEditable) return;
    if (e.ctrlKey || e.metaKey || e.altKey) return;
    var role = t.getAttribute && t.getAttribute('role');
    if (e.key === '/') { e.preventDefault(); openPalette(); return; }
    if (page === 'viewer' && role !== 'tab' && role !== 'treeitem') {
      var step = e.shiftKey ? 10 : 1, k = e.key;
      if (k === 'ArrowRight' || k === 'K' || k === 'k') { e.preventDefault(); setChannel(cur + step); return; }
      if (k === 'ArrowLeft' || k === 'J' || k === 'j') { e.preventDefault(); setChannel(cur - step); return; }
      if (k === 'Home') { e.preventDefault(); setChannel(0); return; }
      if (k === 'End') { e.preventDefault(); setChannel(NCH - 1); return; }
    }
    if (role === 'tab' || role === 'treeitem') return;
    var key = e.key.toLowerCase();
    if (key === 't') toggleTheme();
    else if (key === 'e') togglePanel('explorer');
    else if (key === 'i') togglePanel('inspector');
    else if (key === 'd') togglePanel('dock');
    else if (key === 'r' && page === 'viewer') act('roi');
  });

  if (page === 'viewer') initViewer();
  if (page === 'compare') initCompare();
  if (page === 'explorer') initExplorer();
  if (page === 'board') initBoard();
  if (state === 'palette' && page === 'viewer') openPalette();
})();
