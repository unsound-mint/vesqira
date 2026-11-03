/* Direction C prototype behaviour: theme, ledger (sort/filter/keyboard), drawer, viewer, compare, board.
   Plain ES5-ish, no dependencies, works from file://. All data is fictional. */
(function () {
  'use strict';
  var doc = document, body = doc.body, page = body.getAttribute('data-page');
  var params = new URLSearchParams(location.search);
  function $(s, r) { return (r || doc).querySelector(s); }
  function $$(s, r) { return Array.prototype.slice.call((r || doc).querySelectorAll(s)); }
  function esc(s) { return String(s).replace(/[&<>"]/g, function (c) { return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]; }); }
  function store(k, v) { try { if (v === undefined) return localStorage.getItem(k); localStorage.setItem(k, v); } catch (e) { return null; } }
  function isTyping(t) { return t && (/^(INPUT|SELECT|TEXTAREA)$/.test(t.tagName) || t.isContentEditable); }
  var root = doc.documentElement;

  /* ---------- theme ---------- */
  (function theme() {
    var t = params.get('theme') || store('vq.theme') || (matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light');
    function apply(v) { root.setAttribute('data-theme', v); var b = $('#theme-toggle'); if (b) { b.setAttribute('aria-pressed', v === 'dark'); b.querySelector('.lbl').textContent = v === 'dark' ? 'Dark' : 'Light'; } }
    apply(t);
    var b = $('#theme-toggle');
    if (b) b.addEventListener('click', function () { t = root.getAttribute('data-theme') === 'dark' ? 'light' : 'dark'; store('vq.theme', t); apply(t); });
  })();

  /* ---------- chips ---------- */
  var chips = {
    wcs: function (w) { var m = { valid: ['ok', '✓', 'valid'], missing: ['err', '✕', 'missing'], unverified: ['warn', '?', 'unverified'], 'n/a': ['neutral', '–', 'n/a'] }[w]; return '<span class="chip ' + m[0] + '"><i aria-hidden="true">' + m[1] + '</i>' + m[2] + '</span>'; },
    src: function (s) { if (s === 'local') return '<span class="chip neutral"><i aria-hidden="true">⌂</i>local</span>'; if (s === 'remote-available') return '<span class="chip info"><i aria-hidden="true">↗</i>remote-available</span>'; return '<span class="chip off"><i aria-hidden="true">⊘</i>remote-OFFLINE</span>'; },
    prov: function (p) { var g = { verified: '●', declared: '◐', inferred: '◇', unresolved: '○' }[p]; return '<span class="prov ' + p + '"><i aria-hidden="true">' + g + '</i>' + p + '</span>'; },
    health: function (r) { return r.status === 'corrupt' ? '<span class="chip err"><i aria-hidden="true">✕</i>corrupt</span>' : ''; }
  };
  function relText(r) { if (r.type !== 'VGT run') return '<span class="muted" title="Not applicable to this product type">n/a</span>'; if (r.rel == null) return '<span class="muted" title="Unknown: no reliability data recorded. This is not 0 %."><em>unknown</em></span>'; return r.rel + ' %'; }
  function pathCell(p) { return '<span class="pth" title="' + esc(p) + '"><bdi>' + esc(p) + '</bdi></span>'; }

  /* ---------- pair (baseline / candidate) ---------- */
  var pair = { b: 'r1', c: 'r2' };
  (function loadPair() { try { var s = JSON.parse(store('vq.pair') || 'null'); if (s) pair = s; } catch (e) {} if (params.get('b')) pair.b = params.get('b'); if (params.get('c')) pair.c = params.get('c'); })();
  function savePair() { store('vq.pair', JSON.stringify(pair)); }

  /* ---------- ledger ---------- */
  var DEFS = {
    name: { k: 'name', label: 'Name / path', cls: 'name', w: '', sort: function (r) { return r.name.toLowerCase(); }, cell: function (r, o) {
      var b = (pair.b === r.id ? '<b>B</b>' : '') + (pair.c === r.id ? '<b class="c">C</b>' : '');
      return '<span class="nmrow"><span class="nm" title="' + esc(r.name) + '">' + esc(r.name) + '</span>' + chips.health(r) + (b ? '<span class="bc" aria-label="' + (pair.b === r.id ? 'baseline' : 'candidate') + '">' + b + '</span>' : '') + '</span>' + (o && o.sub === 'cfg' ? '<span class="pth" style="direction:ltr" title="' + esc(r.cfg || '') + '">' + esc(r.cfg || '') + '</span>' : pathCell(r.path)); } },
    type: { k: 'type', label: 'Type', w: 104, cls: 't-type', sort: function (r) { return r.type; }, cell: function (r) { return esc(r.type); } },
    axes: { k: 'axes', label: 'Axes', w: 112, cls: 'pri2', sort: function (r) { return r.axes; }, cell: function (r) { return '<span class="mono">' + esc(r.axes) + '</span>'; } },
    units: { k: 'units', label: 'Units', w: 84, cls: 'pri2', sort: function (r) { return r.units; }, cell: function (r) { return '<span class="mono">' + esc(r.units) + '</span>'; } },
    wcs: { k: 'wcs', label: 'WCS', w: 104, cls: 't-wcs', sort: function (r) { return r.wcs; }, cell: function (r) { return chips.wcs(r.wcs); }, data: 'WCS' },
    size: { k: 'size', label: 'Size', w: 76, num: true, cls: 'pri3', sort: function (r) { return r.sizeB; }, cell: function (r) { return r.size; } },
    src: { k: 'src', label: 'Source', w: 150, cls: 't-src', sort: function (r) { return r.src; }, cell: function (r) { return chips.src(r.src); }, data: 'Source' },
    prov: { k: 'prov', label: 'Provenance', w: 108, cls: 't-prov', sort: function (r) { return r.prov; }, cell: function (r) { return chips.prov(r.prov); }, data: 'Provenance' },
    rel: { k: 'rel', label: 'Rel. cov.', w: 76, num: true, cls: 'pri3', title: 'Reliability coverage: fraction of pixels flagged reliable', sort: function (r) { return r.rel == null ? -1 : r.rel; }, cell: relText },
    mod: { k: 'mod', label: 'Modified', w: 92, cls: 'pri3', sort: function (r) { return r.mod; }, cell: function (r) { return '<span class="mono">' + r.mod + '</span>'; } },
    // runs variant
    input: { k: 'input', label: 'Input cube', w: 150, cls: 'pri2', sort: function (r) { return r.input || ''; }, cell: function (r) { return '<span class="mono" title="' + esc(r.input) + '" style="display:block;overflow:hidden;text-overflow:ellipsis">' + esc(r.input) + '</span>'; } },
    mask: { k: 'mask', label: 'Mask', w: 104, cls: 't-type', data: 'Mask', sort: function (r) { return r.mask || ''; }, cell: function (r) { return r.mask === 'unknown' ? '<span class="chip warn"><i aria-hidden="true">?</i>unknown</span>' : '<span class="chip neutral">' + esc(r.mask) + '</span>'; } },
    thr: { k: 'thr', label: 'Rel. min', w: 76, num: true, cls: 'pri3', title: 'Reliability threshold used by the run', sort: function (r) { return r.thr == null ? -1 : r.thr; }, cell: function (r) { return r.thr == null ? '<em class="muted">unknown</em>' : r.thr.toFixed(2); } },
    valid: { k: 'valid', label: 'Valid px', w: 80, num: true, cls: 'pri3', title: 'Share of pixels valid under the run’s own mask. Not comparable across different masks.', sort: function (r) { return r.valid == null ? -1 : r.valid; }, cell: function (r) { return r.valid == null ? '<em class="muted">unknown</em>' : r.valid + ' %'; } },
    grid: { k: 'grid', label: 'Grid · pixel', w: 124, cls: 't-wcs', data: 'Grid', sort: function (r) { return r.grid || ''; }, cell: function (r) { return '<span class="mono">' + esc(r.grid) + '</span>'; } },
    hash: { k: 'hash', label: 'Config hash', w: 100, cls: 'pri2', sort: function (r) { return r.hash || ''; }, cell: function (r) { return '<span class="mono">' + esc(r.hash) + '</span>'; } },
    meta: { k: 'meta', label: 'Product', cls: 'name', sort: function (r) { return r.name.toLowerCase(); }, cell: null }
  };
  var COLSETS = {
    products: ['name', 'type', 'axes', 'units', 'wcs', 'size', 'src', 'prov', 'rel', 'mod'],
    runs: ['name', 'input', 'mask', 'thr', 'valid', 'grid', 'hash', 'prov', 'mod'],
    compact: ['meta']
  };
  var SORTS = { mod: 'Modified', name: 'Name', type: 'Type', src: 'Source', prov: 'Provenance', wcs: 'WCS' };

  function Ledger(o) {
    var self = this;
    this.o = o; this.table = o.table; this.rows = o.rows; this.cols = o.cols.slice();
    this.sort = { k: null, dir: 1 }; this.q = ''; this.type = o.type || 'all'; this.view = o.view || null; this.sel = o.sel || null; this.hidden = {};
    this.render();
    this.table.addEventListener('keydown', function (e) { self.key(e); });
  }
  Ledger.prototype.visibleRows = function () {
    var q = this.q.toLowerCase(), self = this;
    var list = this.rows.filter(function (r) {
      if (self.type !== 'all' && self.type.split('|').indexOf(r.type) < 0) return false;
      if (self.view && !self.view.fn(r)) return false;
      if (q) { var hay = [r.name, r.path, r.type, r.axes, r.units, r.wcs, r.src, r.prov, r.mask, r.hash, r.cfg, r.input].join(' ').toLowerCase(); return q.split(/\s+/).every(function (t) { return hay.indexOf(t) >= 0; }); }
      return true;
    });
    if (this.sort.k) { var d = DEFS[this.sort.k], dir = this.sort.dir; list.sort(function (a, b) { var x = d.sort(a), y = d.sort(b); return (x < y ? -1 : x > y ? 1 : 0) * dir; }); }
    return list;
  };
  Ledger.prototype.setSort = function (k, dir) { this.sort = { k: k, dir: dir || 1 }; this.render(); };
  Ledger.prototype.render = function () {
    var self = this, o = this.o, rows = this.visibleRows(), compact = !!o.compact;
    var cols = this.cols.filter(function (k) { return !self.hidden[k]; });
    var h = '<colgroup>' + cols.map(function (k) { var d = DEFS[k]; return '<col class="' + (d.cls || '') + '"' + (d.w ? ' style="width:' + d.w + 'px"' : '') + '>'; }).join('') + '</colgroup>';
    h += '<thead><tr role="row">' + cols.map(function (k) {
      var d = DEFS[k], s = self.sort.k === k ? (self.sort.dir > 0 ? 'ascending' : 'descending') : 'none';
      return '<th scope="col" role="columnheader" class="' + (d.cls || '') + (d.num ? ' num' : '') + '" aria-sort="' + s + '"' + (d.title ? ' title="' + esc(d.title) + '"' : '') + '><button class="sortbtn" type="button" data-sort="' + k + '"><span>' + d.label + '</span><span class="ar" aria-hidden="true"></span></button></th>';
    }).join('') + '</tr></thead><tbody>';
    rows.forEach(function (r) {
      h += '<tr role="row" tabindex="-1" data-id="' + r.id + '" aria-selected="' + (self.sel === r.id) + '"' + (r.status === 'corrupt' ? ' class="corrupt"' : '') + '>';
      cols.forEach(function (k) {
        var d = DEFS[k], inner;
        if (k === 'meta') inner = self.metaCell(r); else inner = d.cell(r, o);
        h += '<td role="cell" class="' + (d.cls || '') + (d.num ? ' num' : '') + '"' + (d.data ? ' data-k="' + d.data + '"' : '') + '>' + inner + '</td>';
      });
      h += '</tr>';
    });
    h += '</tbody>';
    this.table.innerHTML = h;
    this.table.setAttribute('role', 'table');
    $$('tbody, thead', this.table).forEach(function (n) { n.setAttribute('role', 'rowgroup'); });
    this.count = rows.length;
    var live = $(o.status || '#lg-status'); if (live) live.textContent = rows.length + ' of ' + this.rows.length + ' rows' + (this.q ? ' match “' + this.q + '”' : '');
    this.tbody = $('tbody', this.table);
    $$('td', this.tbody).forEach(function (td) { if (td.scrollWidth > td.clientWidth + 1 && !td.title) td.title = td.textContent; });
    var trs = $$('tr', this.tbody), cur = null;
    trs.forEach(function (tr) { if (tr.getAttribute('data-id') === self.sel) cur = tr; });
    if (!cur && trs.length) { cur = trs[0]; if (o.autoSelect) { this.sel = cur.getAttribute('data-id'); cur.setAttribute('aria-selected', 'true'); } }
    if (cur) cur.setAttribute('tabindex', '0');
    $$('.sortbtn', this.table).forEach(function (b) { b.addEventListener('click', function () { var k = b.getAttribute('data-sort'); self.setSort(k, self.sort.k === k ? -self.sort.dir : 1); var nb = $('[data-sort="' + k + '"]', self.table); if (nb) nb.focus(); if (o.onSort) o.onSort(); }); });
    trs.forEach(function (tr) {
      tr.addEventListener('click', function (e) { var bt = e.target.closest('[data-set]'); if (bt) { self.assign(tr.getAttribute('data-id'), bt.getAttribute('data-set')); return; } self.select(tr, true); });
      tr.addEventListener('dblclick', function () { self.open(tr.getAttribute('data-id')); });
      tr.addEventListener('focus', function () { if (self.sel !== tr.getAttribute('data-id')) self.select(tr, false); });
    });
    if (o.onRender) o.onRender(this);
    if (o.onSelect && this.sel && !o.noInitial) { var rr = this.rowById(this.sel); if (rr && o.autoSelect) o.onSelect(rr, true); }
  };
  Ledger.prototype.metaCell = function (r) {
    var o = this.o, b = (pair.b === r.id ? '<b>B</b>' : '') + (pair.c === r.id ? '<b class="c">C</b>' : '');
    var meta = esc(r.type) + ' · ' + (o.hideWcs ? '' : 'WCS ' + r.wcs + ' · ') + (o.reviewer ? esc(r.rev || '') : esc(r.src));
    var acts = o.assign && r.type === 'VGT run' ? '<span class="drawer-act">' + ['b', 'c'].map(function (s) { return '<button class="btn" tabindex="-1" type="button" data-set="' + s + '" aria-label="Set ' + r.name + ' as ' + (s === 'b' ? 'baseline' : 'candidate') + '" style="height:22px;padding:0 6px;margin-right:4px">' + s.toUpperCase() + '</button>'; }).join('') + '</span>' : '';
    return '<span class="nmrow"><span class="nm" title="' + esc(r.name) + '">' + esc(r.name) + '</span>' + chips.health(r) + (b ? '<span class="bc">' + b + '</span>' : '') + '</span><span class="pth" style="direction:ltr;font-family:var(--font)">' + meta + (r.status === 'offline' ? ' <b style="color:var(--err)">OFFLINE</b>' : '') + '</span>' + acts;
  };
  Ledger.prototype.rowById = function (id) { for (var i = 0; i < this.rows.length; i++) if (this.rows[i].id === id) return this.rows[i]; return null; };
  Ledger.prototype.select = function (tr, focus) {
    var self = this;
    $$('tr', this.tbody).forEach(function (t) { var on = t === tr; t.setAttribute('aria-selected', on); t.setAttribute('tabindex', on ? '0' : '-1'); });
    this.sel = tr.getAttribute('data-id');
    if (focus && doc.activeElement !== tr) tr.focus({ preventScroll: true });
    if (this.o.onSelect) this.o.onSelect(this.rowById(this.sel), false);
  };
  Ledger.prototype.assign = function (id, which) {
    if (which === 'b') pair.b = id; else pair.c = id; savePair(); this.render(); this.focusRow(id);
    if (this.o.onAssign) this.o.onAssign();
  };
  Ledger.prototype.focusRow = function (id) { var tr = $('tr[data-id="' + id + '"]', this.tbody); if (tr) { tr.setAttribute('tabindex', '0'); tr.focus(); } };
  Ledger.prototype.open = function (id) { if (this.o.onOpen) this.o.onOpen(this.rowById(id)); };
  Ledger.prototype.key = function (e) {
    if (e.target.closest && e.target.closest('.sortbtn')) return;
    var tr = e.target.closest('tr'); if (!tr || !this.tbody.contains(tr)) return;
    var trs = $$('tr', this.tbody), i = trs.indexOf(tr), n = i, k = e.key;
    if (k === 'ArrowDown') n = Math.min(trs.length - 1, i + 1);
    else if (k === 'ArrowUp') n = Math.max(0, i - 1);
    else if (k === 'Home') n = 0; else if (k === 'End') n = trs.length - 1;
    else if (k === 'PageDown') n = Math.min(trs.length - 1, i + 8); else if (k === 'PageUp') n = Math.max(0, i - 8);
    else if (k === 'Enter') { e.preventDefault(); this.open(tr.getAttribute('data-id')); return; }
    else if (k === ' ') { e.preventDefault(); this.select(tr, false); return; }
    else if ((k === 'b' || k === 'B' || k === 'c' || k === 'C') && !e.ctrlKey && !e.metaKey && !e.altKey) { var r = this.rowById(tr.getAttribute('data-id')); if (r.type === 'VGT run') { e.preventDefault(); this.assign(r.id, k.toLowerCase()); } return; }
    else return;
    e.preventDefault();
    var t = trs[n]; trs.forEach(function (x) { x.setAttribute('tabindex', '-1'); }); t.setAttribute('tabindex', '0'); t.focus(); // focus handler selects (selection follows focus)
    t.scrollIntoView({ block: 'nearest' });
  };

  /* ---------- global chrome: drawer, keys ---------- */
  var drawerState = { open: false, pinned: store('vq.pinned') !== '0' };
  function wide() { return innerWidth >= 1360; }
  function applyDrawer() {
    var sh = $('.shell'); if (!sh || !$('#drawer')) return;
    var mode = wide() && drawerState.pinned ? 'pinned' : (drawerState.open ? 'overlay' : 'hidden');
    sh.setAttribute('data-drawer', mode);
    var t = $('#drawer-toggle'); if (t) { t.setAttribute('aria-expanded', mode !== 'hidden'); }
    var p = $('#drawer-pin'); if (p) { p.setAttribute('aria-pressed', mode === 'pinned'); p.textContent = mode === 'pinned' ? 'Unpin' : 'Pin'; p.hidden = !wide(); }
    var c = $('#drawer-close'); if (c) c.hidden = mode === 'pinned';
    $('#drawer').setAttribute('aria-hidden', mode === 'hidden');
  }
  function toggleDrawer(force) {
    if (wide()) { drawerState.pinned = force !== undefined ? force : !drawerState.pinned; store('vq.pinned', drawerState.pinned ? '1' : '0'); drawerState.open = false; }
    else drawerState.open = force !== undefined ? force : !drawerState.open;
    applyDrawer();
    if (drawerState.open || (wide() && drawerState.pinned)) { var f = $('#lg-filter'); if (!wide()) { var tr = $('#drawer tr[tabindex="0"]'); (tr || f) && (tr || f).focus(); } }
  }
  addEventListener('resize', applyDrawer);
  doc.addEventListener('DOMContentLoaded', function () {
    var t = $('#drawer-toggle'); if (t) t.addEventListener('click', function () { if (wide()) toggleDrawer(); else toggleDrawer(!drawerState.open); });
    var p = $('#drawer-pin'); if (p) p.addEventListener('click', function () { toggleDrawer(); });
    var c = $('#drawer-close'); if (c) c.addEventListener('click', function () { drawerState.open = false; applyDrawer(); if (t) t.focus(); });
    var s = $('.scrim'); if (s) s.addEventListener('click', function () { drawerState.open = false; applyDrawer(); });
    applyDrawer();
  });
  doc.addEventListener('keydown', function (e) {
    if (e.key === 'Escape') {
      var f = $('#lg-filter');
      if (doc.activeElement === f && f.value) { f.value = ''; f.dispatchEvent(new Event('input')); return; }
      if (doc.activeElement === f) { var tr = $('.ledger tr[tabindex="0"]'); if (tr) tr.focus(); return; }
      if (drawerState.open) { drawerState.open = false; applyDrawer(); var t = $('#drawer-toggle'); if (t) t.focus(); }
      return;
    }
    if (isTyping(e.target) || e.ctrlKey || e.metaKey) return;
    if (e.key === '/') { var ff = $('#lg-filter'); if (ff) { if (!wide() && $('#drawer') && !drawerState.open && !$('#lg-filter').offsetParent) { drawerState.open = true; applyDrawer(); } e.preventDefault(); ff.focus(); ff.select(); } }
    else if (e.key === '[' && $('#drawer')) { e.preventDefault(); if (wide()) toggleDrawer(); else toggleDrawer(!drawerState.open); }
  });

  function openRow(r) {
    if (!r) return;
    if (r.type === 'VGT run') { if (pair.b !== r.id && pair.c !== r.id) { pair.c = r.id; savePair(); } location.href = '03-compare.html?b=' + pair.b + '&c=' + pair.c; }
    else if (r.type === 'Figure') location.href = '04-board.html';
    else location.href = '02-viewer.html?row=' + r.id;
  }

  /* ---------- drawer ledger (pages 02-04) ---------- */
  var drawerLedger = null;
  function initDrawer(opts) {
    var t = $('#drawer-table'); if (!t) return;
    var rows = opts.rows || window.ROWS;
    drawerLedger = new Ledger({ table: t, rows: rows, cols: COLSETS.compact, compact: true, type: opts.type || 'all', sel: opts.sel, autoSelect: !!opts.autoSelect, hideWcs: opts.hideWcs, reviewer: opts.reviewer, assign: opts.assign, status: '#lg-status', onSelect: opts.onSelect, onOpen: opts.onOpen || function (r) { drawerState.open = false; applyDrawer(); openRow(r); }, onAssign: opts.onAssign, noInitial: opts.noInitial });
    var f = $('#lg-filter'); if (f) f.addEventListener('input', function () { drawerLedger.q = f.value; drawerLedger.render(); });
    var ty = $('#lg-type'); if (ty) { ty.value = opts.type || 'all'; ty.addEventListener('change', function () { drawerLedger.type = ty.value; drawerLedger.render(); }); }
    var so = $('#lg-sort'); if (so) { so.innerHTML = Object.keys(SORTS).map(function (k) { return '<option value="' + k + '">Sort: ' + SORTS[k] + '</option>'; }).join(''); so.addEventListener('change', function () { drawerLedger.setSort(so.value, so.value === 'mod' ? -1 : 1); }); }
    return drawerLedger;
  }

  /* =====================================================================
     01 explorer
     ===================================================================== */
  function initExplorer() {
    var runs = params.get('view') === 'runs', empty = params.get('state') === 'empty';
    var rows = empty ? [] : (runs ? window.ROWS.filter(function (r) { return r.type === 'VGT run'; }) : window.ROWS);
    var cols = runs ? COLSETS.runs : COLSETS.products;
    var tableEl = $('#lg-table'), detail = $('#detail');
    var views = runs ? [
      { id: 'all', label: 'All runs', fn: function () { return true; } },
      { id: 'std', label: 'Standard mask', fn: function (r) { return r.mask === 'standard'; } },
      { id: 'strict', label: 'Strict mask', fn: function (r) { return r.mask === 'strict'; } },
      { id: 'unres', label: 'Unresolved provenance', fn: function (r) { return r.prov === 'unresolved'; } },
      { id: 'off', label: 'Remote-OFFLINE', fn: function (r) { return r.src === 'remote-OFFLINE'; } }
    ] : [
      { id: 'all', label: 'All products', fn: function () { return true; } },
      { id: 'att', label: 'Needs attention', fn: function (r) { return r.wcs !== 'valid' && r.wcs !== 'n/a' || r.src === 'remote-OFFLINE' || r.status === 'corrupt' || r.prov === 'unresolved'; } },
      { id: 'cubes', label: 'HI cubes', fn: function (r) { return r.type === 'HI cube'; } },
      { id: 'vgt', label: 'VGT runs', fn: function (r) { return r.type === 'VGT run'; } },
      { id: 'remote', label: 'Remote', fn: function (r) { return r.src.indexOf('remote') === 0; } }
    ];
    var vw = $('#lg-views'), cur = views[0];
    var presel = params.get('view') === 'attention' ? 'att' : params.get('saved');
    if (presel) views.forEach(function (v) { if (v.id === presel) cur = v; });
    function drawViews() {
      vw.innerHTML = views.map(function (v) { var n = rows.filter(v.fn).length; return '<button type="button" data-v="' + v.id + '" aria-pressed="' + (v === cur) + '">' + v.label + ' <span class="mono">' + n + '</span></button>'; }).join('') +
        (runs ? '<a href="01-explorer.html">← All products</a>' : '<a href="01-explorer.html?view=runs">Experiment runs (dense) →</a>');
    }
    drawViews();
    var mobile = innerWidth <= 760;
    var L = new Ledger({ table: tableEl, rows: rows, cols: cols, sub: runs ? 'cfg' : null, view: cur, autoSelect: !mobile && !empty, status: '#lg-status', onSelect: showDetail, onOpen: openRow, onRender: function (l) { var c = $('#lg-count'); if (c) c.textContent = l.count + ' of ' + rows.length + ' shown'; renderEmpty(l); } });
    function renderEmpty(l) {
      var sc = $('#lg-scroll'), ex = $('#lg-empty'); if (ex) ex.remove();
      if (l.count === 0) {
        var d = doc.createElement('div'); d.id = 'lg-empty'; d.className = 'ledger-empty'; d.setAttribute('role', 'status');
        d.innerHTML = empty ?
          '<h2>This project has no registered datasets yet</h2><p>Register existing FITS cubes or VGT outputs by reference. Vesqira does not copy or modify them unless you ask for a managed copy.</p><p class="muted">Registering reads headers only; no scientific values are interpreted until you open an item.</p><div style="display:flex;gap:8px;flex-wrap:wrap"><button class="btn primary" type="button">Register local files…</button><button class="btn" type="button">Add remote reference…</button><button class="btn" type="button">Open a sample project (mock)</button></div>' :
          '<h2>No rows match this view and filter</h2><p>Clear the filter or choose another saved view. Nothing was hidden by an automatic rule other than your filter.</p><button class="btn" type="button" id="clearf">Clear filter and view</button>';
        sc.appendChild(d);
        var cb = $('#clearf'); if (cb) cb.addEventListener('click', function () { $('#lg-filter').value = ''; L.q = ''; cur = views[0]; L.view = cur; L.type = 'all'; $('#lg-type').value = 'all'; drawViews(); L.render(); });
      }
    }
    vw.addEventListener('click', function (e) { var b = e.target.closest('button[data-v]'); if (!b) return; views.forEach(function (v) { if (v.id === b.getAttribute('data-v')) cur = v; }); L.view = cur; drawViews(); L.render(); var nb = $('[data-v="' + cur.id + '"]', vw); if (nb) nb.focus(); });
    var f = $('#lg-filter'); f.addEventListener('input', function () { L.q = f.value; L.render(); });
    f.addEventListener('keydown', function (e) { if (e.key === 'ArrowDown') { var tr = $('tr[tabindex="0"]', L.tbody); if (tr) { e.preventDefault(); tr.focus(); } } });
    var ty = $('#lg-type'); ty.addEventListener('change', function () { L.type = ty.value; L.render(); });
    var so = $('#lg-sort'); so.innerHTML = Object.keys(SORTS).map(function (k) { return '<option value="' + k + '">' + SORTS[k] + '</option>'; }).join('');
    so.addEventListener('change', function () { L.setSort(so.value, so.value === 'mod' ? -1 : 1); });
    // column configuration
    var cm = $('#colpop'), cbtn = $('#lg-cols');
    cm.innerHTML = '<strong style="display:block;margin-bottom:4px">Columns</strong>' + cols.filter(function (k) { return k !== 'name'; }).map(function (k) { return '<label><input type="checkbox" data-col="' + k + '" checked> ' + DEFS[k].label + '</label>'; }).join('');
    cbtn.addEventListener('click', function () { var h = cm.hidden; cm.hidden = !h; cbtn.setAttribute('aria-expanded', h); if (h) cm.querySelector('input').focus(); });
    cm.addEventListener('change', function (e) { L.hidden[e.target.getAttribute('data-col')] = !e.target.checked; L.render(); });
    cm.addEventListener('keydown', function (e) { if (e.key === 'Escape') { cm.hidden = true; cbtn.setAttribute('aria-expanded', 'false'); cbtn.focus(); e.stopPropagation(); } });
    if (empty) { $('#lg-views').setAttribute('hidden', ''); }
    // runs: mask honesty banner
    function showDetail(r) {
      if (!r) { detail.setAttribute('data-empty', '1'); return; }
      detail.removeAttribute('data-empty');
      var h = '<div style="display:flex;gap:8px;align-items:flex-start"><h2 style="flex:1">' + esc(r.name) + '</h2><button class="btn icon only-mobile" type="button" id="dclose" aria-label="Close detail">✕</button></div>' + chips.health(r);
      h += '<dl><dt>Type</dt><dd>' + esc(r.type) + '</dd><dt>Axes</dt><dd class="mono">' + esc(r.axes) + '</dd><dt>Units</dt><dd class="mono">' + esc(r.units) + '</dd><dt>WCS</dt><dd>' + chips.wcs(r.wcs) + '</dd><dt>Size</dt><dd>' + r.size + '</dd><dt>Source</dt><dd>' + chips.src(r.src) + '</dd><dt>Provenance</dt><dd>' + chips.prov(r.prov) + '</dd>';
      h += '<dt>Reliability</dt><dd>' + relText(r) + '</dd>';
      if (r.type === 'VGT run') h += '<dt>Mask policy</dt><dd>' + esc(r.mask) + '</dd><dt>Grid</dt><dd class="mono">' + esc(r.grid) + '</dd><dt>Config hash</dt><dd class="mono">' + esc(r.hash) + '</dd><dt>Input</dt><dd class="mono">' + esc(r.input) + '</dd>';
      if (r.rev) h += '<dt>Revision</dt><dd>' + esc(r.rev) + '</dd>';
      h += '<dt>Modified</dt><dd class="mono">' + r.mod + '</dd></dl>';
      h += '<h3>Full path</h3><p class="full-path">' + esc(r.path) + '</p>';
      if (r.cfg) h += '<h3 style="margin-top:12px">Configuration</h3><p class="full-path">' + esc(r.cfg) + '</p>';
      h += '<p class="note">' + esc(r.note) + '</p>';
      h += '<div class="acts"><button class="btn primary" type="button" id="dopen"' + (r.status ? ' aria-disabled="true"' : '') + '>Open</button>';
      if (r.type === 'VGT run') h += '<button class="btn" type="button" data-set="b">Set as baseline <kbd>B</kbd></button><button class="btn" type="button" data-set="c">Set as candidate <kbd>C</kbd></button>';
      h += '</div>';
      if (r.status === 'corrupt') h += '<p class="muted">Open is disabled: the file is truncated. Re-register after re-transfer.</p>';
      if (r.status === 'offline') h += '<p class="muted">Open is disabled while the remote source is offline. Cached metadata above is stale.</p>';
      detail.innerHTML = h;
      var o = $('#dopen'); if (o && !r.status) o.addEventListener('click', function () { openRow(r); });
      var dc = $('#dclose'); if (dc) dc.addEventListener('click', function () { detail.setAttribute('data-empty', '1'); var tr = $('tr[tabindex="0"]', L.tbody); if (tr) tr.focus(); });
      $$('[data-set]', detail).forEach(function (b) { b.addEventListener('click', function () { L.assign(r.id, b.getAttribute('data-set')); showDetail(r); updateRunsNote(); }); });
    }
    L.o.onAssign = updateRunsNote;
    function updateRunsNote() {
      var n = $('#runs-note'); if (!n) return;
      var b = window.byId(pair.b), c = window.byId(pair.c);
      var same = b && c && b.mask === c.mask;
      n.className = 'banner ' + (same ? 'info' : 'warn');
      n.innerHTML = '<span><b>Baseline B</b> ' + esc(b.name) + ' (' + esc(b.mask) + ' mask) · <b>Candidate C</b> ' + esc(c.name) + ' (' + esc(c.mask) + ' mask). ' + (same ? 'Masks match by name; open Compare to check WCS grid, beam and units before reading any statistic.' : 'Different mask policies: valid-pixel counts and statistics are not equivalent. Compare shows them side by side with this disclosed, never as a ranking.') + '</span><a class="btn" href="03-compare.html?b=' + pair.b + '&c=' + pair.c + '">Open comparison</a>';
    }
    if (runs) { $('#runs-note').hidden = false; updateRunsNote(); }
    if (!L.sel && !mobile) detail.setAttribute('data-empty', '1');
    if (mobile) detail.setAttribute('data-empty', '1');
    if (empty) { detail.innerHTML = '<p class="muted">Select a row to see its full path, provenance and actions.</p>'; detail.removeAttribute('data-empty'); }
    window.__L = L;
  }

  /* =====================================================================
     02 viewer
     ===================================================================== */
  function initViewer() {
    var S = window.SPECTRUM, N = S.velocity_kms.length, MAXI = N - 1;
    var row = window.byId(params.get('row') || 'c1') || window.byId('c1');
    var state = params.get('state') || row.vs || 'default';
    var ch = parseInt(params.get('ch') || '120', 10); // 120 -> 0 km/s
    var cur = { x: 300, y: 220 }; // 1-based pixel
    var roi = { x0: 180, y0: 150, x1: 300, y1: 250 };
    var PLANES = [-12, -4, 0, 4, 12];
    var img = $('#plane'), stage = $('#stage'), xh = $('#xh'), roiEl = $('#roi');
    var nospec = false, nowcs = false, mono = false;
    function planeFor(i) { var f; if (nospec) f = PLANES[Math.round(i / MAXI * 4)]; else { var v = S.velocity_kms[i], best = 1e9; PLANES.forEach(function (p) { if (Math.abs(p - v) < best) { best = Math.abs(p - v); f = p; } }); } return '../_shared/assets/chan_' + (f >= 0 ? '+' : '-') + Math.abs(f) + 'kms.png'; }
    function fmtV(v) { return (v >= 0 ? '+' : '−') + Math.abs(v).toFixed(2); }
    function hms(deg) { var h = deg / 15, H = Math.floor(h), m = (h - H) * 60, M = Math.floor(m), s = (m - M) * 60; return pad(H) + 'h ' + pad(M) + 'm ' + (s < 10 ? '0' : '') + s.toFixed(2) + 's'; }
    function dms(deg) { var sg = deg < 0 ? '−' : '+', a = Math.abs(deg), D = Math.floor(a), m = (a - D) * 60, M = Math.floor(m), s = (m - M) * 60; return sg + pad(D) + '° ' + pad(M) + '′ ' + (s < 10 ? '0' : '') + s.toFixed(1) + '″'; }
    function pad(n) { return (n < 10 ? '0' : '') + n; }
    function mock(x, y, i) { var a = Math.sin(x * 0.071 + i * 0.13) * Math.cos(y * 0.057 - i * 0.07); var n = Math.sin(x * 12.9898 + y * 78.233 + i * 3.1) * 43758.5453; n -= Math.floor(n); return { v: 2.6 * a * a + 0.5 * (n - 0.5), masked: Math.sin(x * 0.031) * Math.sin(y * 0.043 + 1) > 0.82 }; }
    function setReadout() {
      var set = function (id, t, na) { var e = $(id); e.textContent = t; e.className = 'v' + (na ? ' na' : ''); };
      set('#r-px', cur.x + ', ' + cur.y);
      var m = mock(cur.x, cur.y, ch);
      if (nowcs) { set('#r-ra', 'unavailable', true); set('#r-dec', 'unavailable', true); }
      else { var cd = 20 / 3600, dec = 2.2 + (cur.y - 256.5) * cd, ra = 150.125 - (cur.x - 256.5) * cd / Math.cos(dec * Math.PI / 180); set('#r-ra', hms(ra) + ' · ' + ra.toFixed(4) + '°'); set('#r-dec', dms(dec) + ' · ' + dec.toFixed(4) + '°'); }
      if (state === 'error') { set('#r-val', 'not sampled (cached preview)', true); set('#r-mask', 'unknown', true); }
      else { set('#r-val', m.masked ? 'no value (masked)' : m.v.toFixed(2) + ' K'); set('#r-mask', m.masked ? 'invalid (masked)' : 'valid'); }
      xh.style.left = (cur.x - 0.5) / 512 * 100 + '%'; xh.style.top = (512 - cur.y + 0.5) / 512 * 100 + '%';
    }
    function setChannel(i, quiet) {
      ch = Math.max(0, Math.min(MAXI, i));
      $('#ch-input').value = ch;
      $('#ch-vel').textContent = nospec ? 'channel ' + ch + ' of ' + N + ' · velocity unavailable' : 'v = ' + fmtV(S.velocity_kms[ch]) + ' km/s · ch ' + ch + '/' + MAXI;
      img.src = planeFor(ch);
      img.alt = nospec ? 'Channel ' + ch + ' preview (nearest of five mock planes)' : 'HI brightness temperature preview near ' + fmtV(S.velocity_kms[ch]) + ' km/s (nearest of five mock planes)';
      $('#plane-note').textContent = 'Preview: nearest of 5 pre-rendered mock planes (−12…+12 km/s), not a re-read of the cube. Cursor values are mock.';
      setReadout(); plotCursor(); tableCursor();
      var s = $('#sr-chan'); if (s) s.textContent = nospec ? 'Channel ' + ch : 'Channel ' + ch + ', ' + fmtV(S.velocity_kms[ch]) + ' kilometres per second';
    }
    /* plot */
    var svg = $('#plot'), PW = 0, PH = 0, M = { l: 48, r: 22, t: 20, b: 30 }, ymin = 0, ymax = 1;
    S.T_K.forEach(function (t) { ymin = Math.min(ymin, t); ymax = Math.max(ymax, t); });
    ymin = Math.floor(ymin - 0.5); ymax = Math.ceil(ymax + 0.5);
    function px(i) { return M.l + i / MAXI * (PW - M.l - M.r); }
    function py(t) { return M.t + (ymax - t) / (ymax - ymin) * (PH - M.t - M.b); }
    function drawPlot() {
      var r = svg.getBoundingClientRect(); PW = r.width; PH = r.height; if (PW < 10) return;
      var g = '', k, xl = nospec ? 'channel index' : 'velocity (km/s)';
      for (var t = Math.ceil(ymin); t <= ymax; t += 2) g += '<line x1="' + M.l + '" x2="' + (PW - M.r) + '" y1="' + py(t) + '" y2="' + py(t) + '" stroke="var(--line)" stroke-width="1"/><text x="' + (M.l - 6) + '" y="' + (py(t) + 4) + '" text-anchor="end">' + t + '</text>';
      var step = PW < 500 ? 20 : 10;
      if (nospec) { for (k = 0; k <= MAXI; k += (PW < 500 ? 80 : 40)) g += '<line x1="' + px(k) + '" x2="' + px(k) + '" y1="' + M.t + '" y2="' + (PH - M.b) + '" stroke="var(--line)"/><text x="' + px(k) + '" y="' + (PH - M.b + 14) + '" text-anchor="middle">' + k + '</text>'; }
      else for (var v = -30; v <= 30; v += step) { var i = (v + 30) / 0.25; g += '<line x1="' + px(i) + '" x2="' + px(i) + '" y1="' + M.t + '" y2="' + (PH - M.b) + '" stroke="var(--line)"/><text x="' + px(i) + '" y="' + (PH - M.b + 14) + '" text-anchor="middle">' + (v > 0 ? '+' : v < 0 ? '−' : '') + Math.abs(v) + '</text>'; }
      g += '<line x1="' + M.l + '" x2="' + (PW - M.r) + '" y1="' + py(0) + '" y2="' + py(0) + '" stroke="var(--line-strong)" stroke-dasharray="4 3"/>';
      var d = S.T_K.map(function (t, i) { return (i ? 'L' : 'M') + px(i).toFixed(1) + ' ' + py(t).toFixed(1); }).join('');
      g += '<path d="' + d + '" fill="none" stroke="var(--ink)" stroke-width="1.5" stroke-linejoin="round"/>';
      // ROI band marker (illustrative) and cursor
      g += '<text x="' + (M.l + 4) + '" y="' + (M.t - 6) + '">Mean T<tspan baseline-shift="sub" font-size="9">B</tspan> in ROI A (K) · shared mock series, not recomputed per ROI</text>';
      g += '<text x="' + ((PW + M.l) / 2) + '" y="' + (PH - 2) + '" text-anchor="middle">' + xl + '</text>';
      g += '<g id="pcur"><line stroke="var(--accent)" stroke-width="2" y1="' + M.t + '" y2="' + (PH - M.b) + '"/><circle r="4" fill="var(--accent)" stroke="var(--surface-2)" stroke-width="2"/></g>';
      svg.innerHTML = '<title>Mock region spectrum</title><desc>Synthetic brightness temperature in kelvin versus ' + (nospec ? 'channel index' : 'velocity') + '; a table view is available.</desc>' + g;
      plotCursor();
    }
    function plotCursor() { var c = $('#pcur'); if (!c || !PW) return; var x = px(ch); $('line', c).setAttribute('x1', x); $('line', c).setAttribute('x2', x); var ci = $('circle', c); ci.setAttribute('cx', x); ci.setAttribute('cy', py(S.T_K[ch])); }
    function buildTable() {
      var h = '<table class="simple"><caption class="sr-only">Mock region spectrum values</caption><thead><tr><th scope="col" class="num">Channel</th><th scope="col" class="num">' + (nospec ? 'Velocity' : 'v (km/s)') + '</th><th scope="col" class="num">T (K)</th></tr></thead><tbody>';
      S.T_K.forEach(function (t, i) { h += '<tr data-i="' + i + '"><td class="num">' + i + '</td><td class="num">' + (nospec ? 'unavailable' : S.velocity_kms[i].toFixed(2)) + '</td><td class="num">' + t.toFixed(2) + '</td></tr>'; });
      $('#tbl-wrap').innerHTML = h + '</tbody></table>';
    }
    function tableCursor() { var w = $('#tbl-wrap'); if (!w || w.hidden) return; var o = $('tr.cur', w); if (o) o.classList.remove('cur'); var tr = $('tr[data-i="' + ch + '"]', w); if (tr) { tr.classList.add('cur'); var t = tr.offsetTop - 28; if (tr.offsetTop < w.scrollTop + 28 || tr.offsetTop > w.scrollTop + w.clientHeight - 32) w.scrollTop = Math.max(0, t - w.clientHeight / 2); } }
    new ResizeObserver(drawPlot).observe($('.plot-body'));
    function plotPointer(e) { var r = svg.getBoundingClientRect(); var f = (e.clientX - r.left - M.l) / (r.width - M.l - M.r); setChannel(Math.round(f * MAXI)); }
    svg.addEventListener('pointerdown', function (e) { plotPointer(e); svg.setPointerCapture(e.pointerId); svg.onpointermove = plotPointer; });
    svg.addEventListener('pointerup', function () { svg.onpointermove = null; });
    $('#plot-mode').addEventListener('click', function (e) { var b = e.target.closest('button'); if (!b) return; var tbl = b.getAttribute('data-m') === 'table'; $$('#plot-mode button').forEach(function (x) { x.setAttribute('aria-pressed', x === b); }); $('#tbl-wrap').hidden = !tbl; svg.hidden = tbl; if (tbl) { tableCursor(); } else drawPlot(); });
    /* pointer on image */
    function pixFromEvent(e) { var r = stage.getBoundingClientRect(); return { x: Math.min(512, Math.max(1, Math.floor((e.clientX - r.left) / r.width * 512) + 1)), y: Math.min(512, Math.max(1, 512 - Math.floor((e.clientY - r.top) / r.height * 512))) }; }
    var tool = false, drag = null;
    stage.addEventListener('pointermove', function (e) { if (drag) { var p = pixFromEvent(e); roi = { x0: Math.min(drag.x, p.x), y0: Math.min(drag.y, p.y), x1: Math.max(drag.x, p.x), y1: Math.max(drag.y, p.y) }; drawRoi(); } if (e.buttons === 0 || tool === false) { var q = pixFromEvent(e); cur = q; setReadout(); } });
    stage.addEventListener('pointerdown', function (e) { var p = pixFromEvent(e); if (tool) { drag = p; stage.setPointerCapture(e.pointerId); } else { cur = p; setReadout(); } });
    stage.addEventListener('pointerup', function () { drag = null; });
    function drawRoi() {
      roiEl.style.left = (roi.x0 - 1) / 512 * 100 + '%'; roiEl.style.width = (roi.x1 - roi.x0 + 1) / 512 * 100 + '%';
      roiEl.style.top = (512 - roi.y1) / 512 * 100 + '%'; roiEl.style.height = (roi.y1 - roi.y0 + 1) / 512 * 100 + '%';
      ['x0', 'y0', 'x1', 'y1'].forEach(function (k) { var i = $('#roi-' + k); if (i && doc.activeElement !== i) i.value = roi[k]; });
    }
    ['x0', 'y0', 'x1', 'y1'].forEach(function (k) { $('#roi-' + k).addEventListener('change', function (e) { var v = Math.max(1, Math.min(512, parseInt(e.target.value, 10) || 1)); roi[k] = v; if (roi.x0 > roi.x1) { var t = roi.x0; roi.x0 = roi.x1; roi.x1 = t; } if (roi.y0 > roi.y1) { var u = roi.y0; roi.y0 = roi.y1; roi.y1 = u; } drawRoi(); }); });
    var rb = $('#tool-roi'); function setTool(v) { tool = v; rb.setAttribute('aria-pressed', v); stage.style.cursor = v ? 'cell' : 'crosshair'; }
    rb.addEventListener('click', function () { setTool(!tool); });
    stage.addEventListener('keydown', function (e) {
      if (e.altKey && /^Arrow/.test(e.key)) { var d = e.shiftKey ? 10 : 1; if (e.key === 'ArrowLeft') cur.x = Math.max(1, cur.x - d); if (e.key === 'ArrowRight') cur.x = Math.min(512, cur.x + d); if (e.key === 'ArrowUp') cur.y = Math.min(512, cur.y + d); if (e.key === 'ArrowDown') cur.y = Math.max(1, cur.y - d); e.preventDefault(); setReadout(); }
    });
    /* channel stepper + global keys */
    $('#ch-first').addEventListener('click', function () { setChannel(0); }); $('#ch-prev').addEventListener('click', function () { setChannel(ch - 1); });
    $('#ch-next').addEventListener('click', function () { setChannel(ch + 1); }); $('#ch-last').addEventListener('click', function () { setChannel(MAXI); });
    $('#ch-input').addEventListener('change', function (e) { setChannel(parseInt(e.target.value, 10) || 0); });
    doc.addEventListener('keydown', function (e) {
      if (isTyping(e.target) || e.ctrlKey || e.metaKey || e.altKey) return;
      if (e.target.closest && e.target.closest('.ledger, .sheet, .tbl-wrap, [role=slider]')) return;
      var d = e.shiftKey ? 10 : 1;
      if (state === 'loading' && false) return;
      if (e.key === 'ArrowRight') { setChannel(ch + d); e.preventDefault(); } else if (e.key === 'ArrowLeft') { setChannel(ch - d); e.preventDefault(); }
      else if (e.key === 'Home') { setChannel(0); e.preventDefault(); } else if (e.key === 'End') { setChannel(MAXI); e.preventDefault(); }
      else if (e.key === 'r' || e.key === 'R') setTool(!tool);
    });
    /* display controls (CSS approximation; labelled as such) */
    function applyDisplay() {
      var c = +$('#ctl-contrast').value, st = $('#ctl-stretch').value, cm = $('#ctl-cmap').value;
      var f = 'contrast(' + (0.6 + c / 100 * 1.2).toFixed(2) + ') brightness(' + (st === 'asinh' ? 1.35 : 1) + ')' + (cm === 'gray' ? ' grayscale(1)' : '');
      img.style.filter = f; $('#cbar-img').style.filter = cm === 'gray' ? 'grayscale(1)' : 'none';
      $('#cbar-mid').textContent = st === 'asinh' ? 'asinh stretch' : 'linear';
    }
    ['#ctl-contrast', '#ctl-stretch', '#ctl-cmap'].forEach(function (s) { $(s).addEventListener('input', applyDisplay); });
    $$('.layers li').forEach(function (li) { var cb = $('input[type=checkbox]', li), rg = $('input[type=range]', li), tgt = li.getAttribute('data-layer'); if (!tgt) return; function up() { var e = $('#' + tgt); e.style.display = cb.checked ? '' : 'none'; e.style.opacity = rg.value / 100; } cb.addEventListener('change', up); rg.addEventListener('input', up); up(); });
    /* sheet (mobile) */
    var sh = $('.sheet'), hd = $('#sheet-toggle');
    hd.addEventListener('click', function () { var o = !sh.classList.contains('open'); sh.classList.toggle('open', o); hd.setAttribute('aria-expanded', o); });
    /* state */
    function setProps() {
      var p = $('#props'); var rows = [
        ['Shape', nowcs && nospec ? '512 × 512 × 241' : '512 × 512 × 241 (x, y, spectral)'],
        ['BUNIT', 'K'],
        ['Rest ref.', nospec ? '<span class="chip warn"><i>?</i>unavailable</span>' : 'declared in header'],
        ['Beam', '<span class="chip warn"><i>?</i>unverified</span> 60″ (header)'],
        ['Provenance', chips.prov(row.prov)]
      ];
      p.innerHTML = rows.map(function (r) { return '<dt>' + r[0] + '</dt><dd>' + r[1] + '</dd>'; }).join('');
      var w = $('#wcs-props');
      w.innerHTML = nowcs ? '<dt>Status</dt><dd><span class="chip err"><i>✕</i>missing</span></dd><dt>Sky coords</dt><dd>unavailable</dd><dt>Pixel scale</dt><dd>unknown (not 1″)</dd>' :
        '<dt>Status</dt><dd>' + chips.wcs(row.wcs === 'unverified' ? 'unverified' : 'valid') + '</dd><dt>CTYPE1/2</dt><dd>RA---TAN / DEC--TAN</dd><dt>Pixel scale</dt><dd>20.0″ (header CDELT)</dd><dt>Frame</dt><dd>ICRS (declared)</dd><dt>CTYPE3</dt><dd>' + (nospec ? 'CHAN (no spectral ref.)' : 'VELO-LSR') + '</dd>';
      $('#sheet-title').textContent = row.name;
      $('#tab-viewer-name').textContent = row.name.replace('.fits', '');
    }
    function applyState(s) {
      state = s; nowcs = s === 'no-wcs'; nospec = s === 'no-spectral';
      var ban = $('#banner'), pend = $('#pending'), st = $('#stamp'), sb = $('#scalebar'), cancel = '';
      pend.hidden = true; st.hidden = true; sb.hidden = nowcs; ban.hidden = false; img.style.clipPath = '';
      ban.className = 'banner'; ban.removeAttribute('role');
      var msg = {
        default: null,
        loading: ['info', 'Loading plane tiles 128/256 · 4 MiB of 5.1 GiB remote cube. Only the selected plane is requested, never the whole cube.', '<progress value="128" max="256" aria-label="Plane tiles loaded, 128 of 256"></progress><button class="btn" type="button">Cancel</button>', 'status'],
        error: ['err', '<b>Partial range request failed</b> at byte 4,194,304 of 5.1 GiB (connection reset). Showing the cached preview from 2026-10-28; values are not sampled from the source.', '<button class="btn" type="button">Retry range</button><button class="btn" type="button">Details</button>', 'alert'],
        'no-wcs': ['warn', '<b>No valid WCS: sky coordinates unavailable.</b> Showing pixel coordinates only. No angular scale bar is drawn and pixel scale is unknown, not 1.', '', 'status'],
        'no-spectral': ['warn', '<b>Spectral axis unavailable:</b> velocity cannot be computed (no valid spectral WCS). Channel index only; sky coordinates are still valid.', '', 'status']
      }[s];
      if (!msg) ban.hidden = true; else { ban.classList.add(msg[0]); ban.setAttribute('role', msg[3]); ban.innerHTML = '<span>' + msg[1] + '</span>' + msg[2]; }
      if (s === 'loading') { pend.hidden = false; img.style.clipPath = 'inset(0 0 50% 0)'; }
      if (s === 'error' || (row.status === 'corrupt' && s === 'error')) { st.hidden = false; }
      var sel = $('#state-sel'); if (sel) sel.value = s;
      $('#ch-vel').closest('output').setAttribute('aria-live', 'off');
      setProps();
      $('#cbar-img').parentNode.hidden = false;
      var sbt = $('#scalebar span'); if (sbt) sbt.textContent = '30′ (header WCS)';
      $('#plot-sub').textContent = nospec ? 'x axis: channel index' : 'x axis: velocity, km/s';
      buildTable(); drawPlot(); setChannel(ch);
      try { var q = new URLSearchParams(location.search); q.set('state', s); history.replaceState(null, '', '?' + q.toString()); } catch (e) {}
    }
    // custom message for offline/corrupt rows reaching the viewer through the ledger
    var origApply = applyState;
    applyState = function (s) { origApply(s); if (s === 'error' && row.status === 'corrupt') $('#banner span').innerHTML = '<b>File is truncated (corrupt):</b> data ends near 61% of the declared size. Showing the cached preview only; nothing was repaired.'; if (s === 'error' && row.status === 'offline') $('#banner span').innerHTML = '<b>Remote source is OFFLINE</b> (last seen 2026-10-19). Showing the cached preview; values are not sampled from the source.'; };
    $('#state-sel').addEventListener('change', function (e) { applyState(e.target.value); });
    drawRoi(); applyDisplay();
    // master-detail: selecting a cube in the ledger drawer switches the workspace
    initDrawer({ type: 'HI cube|Moment map|Mask', sel: row.id, hideWcs: false, noInitial: true, onSelect: function (r, init) { if (!r || r.type === 'VGT run' || r.type === 'Figure') return; row = r; var s = r.vs === 'offline' || r.vs === 'corrupt' ? 'error' : r.vs; params.delete('state'); applyState(s); } });
    applyState(state);
    drawPlot();
  }

  /* =====================================================================
     03 compare
     ===================================================================== */
  function initCompare() {
    var ST = window.STATS, forced = params.get('state') === 'blocked';
    if (forced && window.byId(pair.c).grid === window.byId(pair.b).grid) pair.c = 'r4';
    function fmt(x, d) { return Number(x).toFixed(d == null ? 2 : d); }
    function stc(s, label) { var m = { pass: ['ok', '✓', 'pass'], fail: ['err', '✕', 'fail'], unverified: ['warn', '?', 'unverified'], differs: ['warn', '≠', 'differs'] }[s]; return '<span class="chip ' + m[0] + '"><i aria-hidden="true">' + m[1] + '</i>' + (label || m[2]) + '</span>'; }
    var hid = 0;
    function addHatch() {
      $$('.hatch').forEach(function (h) { h.parentNode.removeChild(h); });
      $$('.stack[data-hatch]').forEach(function (st) {
        var n = ++hid, ns = 'http://www.w3.org/2000/svg', v = doc.createElementNS(ns, 'svg');
        v.setAttribute('class', 'hatch'); v.setAttribute('viewBox', '0 0 512 512'); v.setAttribute('preserveAspectRatio', 'none'); v.setAttribute('aria-hidden', 'true'); v.style.cssText = 'position:absolute;inset:0;width:100%;height:100%;pointer-events:none';
        v.innerHTML = '<defs><pattern id="hp' + n + '" width="10" height="10" patternUnits="userSpaceOnUse" patternTransform="rotate(45)"><rect width="10" height="10" fill="#000" fill-opacity=".6"/><rect width="3" height="10" fill="#fff"/></pattern><mask id="mk' + n + '" style="mask-type:alpha"><image href="../_shared/assets/vgt_invalid_mask.png" width="512" height="512"/></mask></defs><rect width="512" height="512" fill="url(#hp' + n + ')" mask="url(#mk' + n + ')"/>';
        st.appendChild(v);
      });
      var mt = $('#mask-toggle'); $$('.hatch').forEach(function (h) { h.style.display = mt.checked ? '' : 'none'; });
    }
    function render() {
      var B = window.byId(pair.b), C = window.byId(pair.c);
      $('#slot-b span').textContent = B.name; $('#slot-b span').title = B.path; $('#slot-c span').textContent = C.name; $('#slot-c span').title = C.path;
      $('#tab-cmp-name').textContent = B.name.replace('vgt_run_', '').slice(0, 13) + ' ⇄ ' + C.name.replace('vgt_run_', '').slice(0, 13);
      var gridState = (B.grid === 'unknown' || C.grid === 'unknown') ? 'unverified' : (B.grid === C.grid ? 'pass' : 'fail');
      var wcsOk = B.wcs === 'valid' && C.wcs === 'valid';
      if (!wcsOk && gridState === 'pass') gridState = 'unverified';
      var blocked = gridState !== 'pass';
      var conv = (B.units === C.units && B.units.indexOf('?') < 0) ? 'pass' : 'unverified';
      var maskSame = B.mask === C.mask && B.mask !== 'unknown';
      var checks = [
        ['WCS grid', gridState, gridState === 'pass' ? 'Both 512×512 at 20.0″, same projection and reference pixel (header values)' : gridState === 'fail' ? 'Baseline ' + B.grid + ' vs candidate ' + C.grid + ': pixels do not correspond' : 'A WCS or grid is unverified or unknown; correspondence cannot be shown'],
        ['Resolution / beam', 'unverified', 'Beam is declared in both headers (60″); not independently measured. Resolution match is not established'],
        ['Units / convention', conv, conv === 'pass' ? 'Both axial orientation, degrees (0°≡180°)' : 'Angle convention not recorded for one run'],
        ['Mask / reliability', maskSame ? 'pass' : 'differs', maskSame ? 'Same mask policy (' + B.mask + ')' : 'Baseline: ' + B.mask + ' · Candidate: ' + C.mask + '. Statistics use the joint valid set; not equivalent across masks']
      ];
      $('#checks').innerHTML = checks.map(function (c) { return '<tr><th scope="row">' + c[0] + '</th><td>' + stc(c[1]) + '<div class="basis">' + esc(c[2]) + '</div></td></tr>'; }).join('');
      $('#gate').innerHTML = blocked ? '<div class="banner err" role="alert"><span><b>Numeric comparison withheld.</b> ' + (gridState === 'fail' ? 'The two runs are on different WCS grids (' + esc(B.grid) + ' vs ' + esc(C.grid) + '); a pixel-wise difference would mix unrelated sky positions.' : 'Grid or WCS could not be verified for one run; correspondence between pixels is unknown.') + ' Images are shown side by side at native scale and are not aligned.</span><button class="btn" type="button" id="resample">Resample with stated policy…</button></div>' :
        (maskSame ? '' : '<div class="banner warn"><span><b>Different mask policies.</b> Valid-pixel fractions and statistics below describe the joint valid set and are not equivalent to either run alone.</span></div>');
      $('#panel-c').classList.toggle('native', gridState === 'fail');
      $('#panel-d-body').innerHTML = blocked ? '<div class="withheld"><b>Difference withheld</b><span>' + (gridState === 'fail' ? 'Incompatible WCS grids.' : 'Grid unverified.') + ' No resampling has been applied. Choose a resampling policy (method, target grid, flux handling) to continue.</span></div>' :
        '<div class="stack" data-hatch><img class="main" src="../_shared/assets/vgt_axial_diff.png" alt="Difference map: smallest axial angle between baseline and candidate, 0 to 90 degrees. Masked pixels are shown as flat dark regions and carry no value."></div>';
      $('#panel-d-cap').innerHTML = blocked ? '<b>Δ axial angle (withheld)</b>no values computed' : '<b>Δ axial angle, 0–90°</b>smallest axial angle between B and C · unit: deg';
      var rb = $('#resample'); if (rb) rb.addEventListener('click', function () { $('#resample-note').hidden = false; });
      var rows = [['Run record', chips.prov(B.prov), chips.prov(C.prov)], ['Config hash', '<span class="mono">' + esc(B.hash) + '</span> ' + chips.prov(B.hash === 'none' ? 'unresolved' : 'declared'), '<span class="mono">' + esc(C.hash) + '</span> ' + chips.prov(C.hash === 'none' ? 'unresolved' : 'declared')],
        ['Git SHA (Julia src)', '<span class="mono">not captured</span> ' + chips.prov('unresolved'), '<span class="mono">e3b7c9a1…</span> ' + chips.prov('declared')],
        ['Output checksum', B.prov === 'verified' ? '<span class="mono">sha256:4d1f…a09c</span> ' + chips.prov('verified') : '<span class="mono">none</span> ' + chips.prov('unresolved'), C.prov === 'verified' ? '<span class="mono">sha256:b72e…11d4</span> ' + chips.prov('verified') : '<span class="mono">none</span> ' + chips.prov('unresolved')],
        ['RNG seed', 'not recorded ' + chips.prov('unresolved'), 'not recorded ' + chips.prov('unresolved')],
        ['Input cube', '<span class="mono" style="word-break:break-all">' + esc(B.input) + '</span> ' + chips.prov('inferred'), '<span class="mono" style="word-break:break-all">' + esc(C.input) + '</span> ' + chips.prov('inferred')]];
      $('#prov').innerHTML = '<thead><tr><th scope="col">Item</th><th scope="col"><span class="slot" style="border:0;padding:0;background:none"><b>B</b></span> Baseline</th><th scope="col"><span class="slot" style="border:0;padding:0;background:none"><b class="c">C</b></span> Candidate</th></tr></thead><tbody>' + rows.map(function (r) { return '<tr><th scope="row">' + r[0] + '</th><td>' + r[1] + '</td><td>' + r[2] + '</td></tr>'; }).join('') + '</tbody>';
      var q;
      if (blocked) q = '<tr><th scope="row">Valid fraction</th><td class="num">withheld</td></tr><tr><th scope="row">Median Δ axial</th><td class="num">withheld</td></tr><tr><th scope="row">90th-percentile Δ</th><td class="num">withheld</td></tr>';
      else q = '<tr><th scope="row">Joint valid fraction</th><td class="num">' + fmt(ST.valid_fraction * 100, 0) + ' %</td></tr><tr><th scope="row">Median Δ axial</th><td class="num">' + fmt(ST.axial_diff_median_deg) + '°</td></tr><tr><th scope="row">90th-percentile Δ</th><td class="num">' + fmt(ST.axial_diff_p90_deg) + '°</td></tr>';
      $('#stats').innerHTML = q + '<tr><th scope="row">Self-check: axial mean of 1° and 179°</th><td class="num">≈ 0° (≡ 180°)<br><span class="muted" style="font-size:11px">numeric: ' + ST.axial_mean_1_179_deg.toExponential(1) + '°</span></td></tr>';
      $('#stats-warn').hidden = !(!blocked && !maskSame);
      $('#chk-lead').textContent = blocked ? 'Blocking check failed' : 'No blocking check failed; one is unverified';
      addHatch();
    }
    render();
    initDrawer({ type: 'VGT run', assign: true, noInitial: true, onAssign: render, onSelect: function () {}, onOpen: function (r) { drawerState.open = false; applyDrawer(); } });
    // swap
    $('#swap').addEventListener('click', function () { var t = pair.b; pair.b = pair.c; pair.c = t; savePair(); render(); if (drawerLedger) drawerLedger.render(); });
    // invalid mask hatch toggle
    $('#mask-toggle').addEventListener('change', addHatch);
  }

  /* =====================================================================
     04 board
     ===================================================================== */
  function initBoard() {
    var role = params.get('role') || (innerWidth <= 760 ? 'reviewer' : 'owner');
    var denied = params.get('state') === 'denied';
    function apply() {
      var rev = role === 'reviewer';
      $$('.ownonly').forEach(function (e) { e.hidden = rev; }); $$('.revonly').forEach(function (e) { e.hidden = !rev; });
      $$('#role-seg button').forEach(function (b) { b.setAttribute('aria-pressed', b.getAttribute('data-role') === role); });
      $('#board-body').hidden = denied && rev; $('#denied').hidden = !(denied && rev);
      $('#tab-board-name').textContent = rev ? 'Review · Orion HI (read-only)' : 'Board · Orion HI draft';
      body.setAttribute('data-role', role);
      if (drawerLedger) { drawerLedger.reviewer = rev; drawerLedger.o.reviewer = rev; drawerLedger.rows = rev ? window.ROWS.filter(function (r) { return r.type === 'Figure'; }) : window.ROWS; drawerLedger.type = rev ? 'all' : 'all'; $('#drawer-title').textContent = rev ? 'Published previews (3)' : 'Project ledger'; drawerLedger.render(); var tf = $('#lg-type'); if (tf) tf.hidden = rev; }
      try { var q = new URLSearchParams(location.search); q.set('role', role); if (!denied) q.delete('state'); history.replaceState(null, '', '?' + q.toString()); } catch (e) {}
    }
    // revisions: three figure rows on the board
    initDrawer({ type: 'all', noInitial: true, reviewer: false, hideWcs: true, onOpen: function () { drawerState.open = false; applyDrawer(); } });
    $('#role-seg').addEventListener('click', function (e) { var b = e.target.closest('button'); if (!b) return; role = b.getAttribute('data-role'); apply(); });
    apply();
    // anchored comments
    function hl(n) { $$('.pin').forEach(function (p) { p.setAttribute('aria-pressed', p.getAttribute('data-n') === n); }); $$('.thread').forEach(function (t) { t.classList.toggle('hl', t.getAttribute('data-n') === n); }); var t = $('.thread[data-n="' + n + '"]'); if (t && n) t.scrollIntoView({ block: 'nearest' }); }
    $$('.pin').forEach(function (p) { p.addEventListener('click', function () { hl(p.getAttribute('data-n')); }); });
    $$('.thread button.anchor').forEach(function (b) { b.addEventListener('click', function () { hl(b.closest('.thread').getAttribute('data-n')); var p = $('.pin[data-n="' + b.closest('.thread').getAttribute('data-n') + '"]'); if (p) p.focus(); }); });
    // board table keyboard nav + panel highlight
    var bt = $('#board-table tbody');
    if (bt) {
      var trs = $$('tr', bt); trs[0].setAttribute('tabindex', '0');
      bt.addEventListener('keydown', function (e) { var tr = e.target.closest('tr'); var i = trs.indexOf(tr), n = i; if (e.key === 'ArrowDown') n = Math.min(trs.length - 1, i + 1); else if (e.key === 'ArrowUp') n = Math.max(0, i - 1); else if (e.key === 'Home') n = 0; else if (e.key === 'End') n = trs.length - 1; else return; e.preventDefault(); trs.forEach(function (t) { t.setAttribute('tabindex', '-1'); }); trs[n].setAttribute('tabindex', '0'); trs[n].focus(); });
      trs.forEach(function (tr) { tr.addEventListener('focus', sel.bind(null, tr)); tr.addEventListener('click', sel.bind(null, tr)); });
      function sel(tr) { trs.forEach(function (t) { t.setAttribute('aria-selected', t === tr); }); var p = tr.getAttribute('data-panel'); $$('.comp figure').forEach(function (f) { f.style.outline = f.getAttribute('data-panel') === p ? '3px solid #0a6464' : ''; f.style.outlineOffset = '3px'; }); }
    }
    var rq = $('#req-send'); if (rq) rq.addEventListener('click', function () { $('#req-status').textContent = 'Request recorded for the owner (prototype: nothing is sent and no computation is run). The owner decides whether to publish a new comparison.'; });
  }

  doc.addEventListener('DOMContentLoaded', function () {
    if (page === 'explorer') initExplorer();
    else if (page === 'viewer') initViewer();
    else if (page === 'compare') initCompare();
    else if (page === 'board') initBoard();
  });
})();
