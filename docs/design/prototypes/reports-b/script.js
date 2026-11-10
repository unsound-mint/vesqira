/* Reports B: Linked pages. Disposable prototype, no framework. All data is fictional. */
(function () {
'use strict';
const Q = new URLSearchParams(location.search);
const role = document.body.dataset.role;      // owner | reader
const page = document.body.dataset.page;      // editor | reader | graph
const state = Q.get('state') || 'default';
const root = document.documentElement;
if (Q.get('theme') === 'dark' || Q.get('theme') === 'light') root.dataset.theme = Q.get('theme');
const $ = (s, e) => (e || document).querySelector(s);
const $$ = (s, e) => [...(e || document).querySelectorAll(s)];
const esc = s => String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
const narrow = () => matchMedia('(max-width:760px)').matches;

/* ---------- icons (lucide-style, 1.5px stroke) ---------- */
const ICONS = {
  file: '<path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z"/><path d="M14 3v5h5M9 13h6M9 17h6"/>',
  search: '<circle cx="11" cy="11" r="7"/><path d="m20 20-3.5-3.5"/>',
  link: '<path d="M10 14a4 4 0 0 0 5.7 0l3-3a4 4 0 0 0-5.7-5.7l-1 1"/><path d="M14 10a4 4 0 0 0-5.7 0l-3 3a4 4 0 0 0 5.7 5.7l1-1"/>',
  graph: '<circle cx="6" cy="12" r="2.5"/><circle cx="18" cy="6" r="2.5"/><circle cx="18" cy="18" r="2.5"/><path d="m8.2 10.8 7.6-3.6M8.2 13.2l7.6 3.6"/>',
  msg: '<path d="M21 12a8 8 0 0 1-11.7 7.1L4 20l1-4.6A8 8 0 1 1 21 12z"/>',
  cd: '<path d="m6 9 6 6 6-6"/>', cr: '<path d="m9 6 6 6-6 6"/>',
  x: '<path d="M6 6l12 12M18 6 6 18"/>', menu: '<path d="M4 7h16M4 12h16M4 17h16"/>',
  check: '<path d="m5 12 5 5 9-10"/>',
  image: '<rect x="3" y="4" width="18" height="16" rx="2"/><circle cx="9" cy="10" r="1.5"/><path d="m21 16-5-5-9 9"/>',
  run: '<circle cx="12" cy="12" r="9"/><path d="m10 9 5 3-5 3z"/>',
  send: '<path d="M22 2 11 13M22 2l-7 20-4-9-9-4z"/>',
  lock: '<rect x="5" y="11" width="14" height="9" rx="2"/><path d="M8 11V8a4 4 0 0 1 8 0v3"/>',
  clock: '<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>',
  warn: '<path d="M12 3 2 20h20z"/><path d="M12 10v5M12 18h.01"/>',
  plus: '<path d="M12 5v14M5 12h14"/>',
  history: '<path d="M3 12a9 9 0 1 0 3-6.7L3 8"/><path d="M3 3v5h5M12 7v5l3 2"/>',
  moon: '<path d="M20 14a8 8 0 1 1-10-10 7 7 0 0 0 10 10z"/>',
  users: '<circle cx="9" cy="8" r="3"/><path d="M3 20a6 6 0 0 1 12 0M16 5a3 3 0 0 1 0 6M21 20a5 5 0 0 0-4-4.9"/>',
  spark: '<path d="m12 3 1.8 5.2L19 10l-5.2 1.8L12 17l-1.8-5.2L5 10l5.2-1.8z"/>',
  folder: '<path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/>',
  up: '<path d="M12 16V4M7 9l5-5 5 5M4 20h16"/>',
  ban: '<circle cx="12" cy="12" r="9"/><path d="m5.6 5.6 12.8 12.8"/>',
  list: '<path d="M8 6h13M8 12h13M8 18h13M3 6h.01M3 12h.01M3 18h.01"/>',
  reply: '<path d="M9 14 4 9l5-5M4 9h10a6 6 0 0 1 6 6v3"/>',
  edit: '<path d="M4 20h4L19 9l-4-4L4 16z"/>'
};
const ic = n => '<svg class="ic" aria-hidden="true"><use href="#i-' + n + '"/></svg>';
document.body.insertAdjacentHTML('afterbegin', '<svg width="0" height="0" style="position:absolute" aria-hidden="true">' +
  Object.keys(ICONS).map(k => '<symbol id="i-' + k + '" viewBox="0 0 24 24">' + ICONS[k] + '</symbol>').join('') + '</svg>');

/* ---------- data (fictional) ---------- */
const TITLE = 'Block size 32 vs 64: gradient orientation on the LMC H I cube';
const REP = [
  { id: 'r1', t: TITLE, short: 'Block size 32 vs 64', shared: true, cur: true },
  { id: 'r2', t: 'Noise masking threshold sweep', shared: true },
  { id: 'r4', t: 'Baseline VGT on the LMC cube', shared: true },
  { id: 'r3', t: 'Weekly notes 2026-W45', shared: false },
  { id: 'r5', t: 'Weekly notes 2026-W44', shared: false }
];
const repByTitle = t => REP.find(r => r.t === t);
const curHref = role === 'owner' ? '01-owner-editor.html' : '02-reader.html';

const TH = {
  t1: { id: 't1', block: 'b-setup', quote: '−12…+12 km/s', rev: 4, resolved: true, msgs: [
    { a: 'S', n: 'Supervisor', d: '9 Nov, 16:05', x: 'Why symmetric about 0? Is the LMC at 0 km/s in this cube?', s: 'seen' },
    { a: 'D', n: 'Denis', d: '9 Nov, 18:40', x: 'The cube is shifted to the LMC frame before the run. The header declares this; I have not verified it independently. I will say so in the text.' }] },
  t2: { id: 't2', block: 'b-figs', quote: 'Figure 2, block 64, upper right of the field (58%, 36%)', rev: 4, isNew: true, msgs: [
    { a: 'S', n: 'Supervisor', d: '10 Nov, 12:20', x: 'The orientation turns by about 90° in this patch. Is that the block size, or the masking?', s: 'unseen' }] },
  t3: { id: 't3', block: 'b-res-intro', quote: 'Figures show axial orientation (0° and 180° are the same orientation).', rev: 3, stale: true, msgs: [
    { a: 'S', n: 'Supervisor', d: '7 Nov, 10:12', x: 'Which reliability threshold is the mask using?', s: 'seen' }] },
  t4: { id: 't4', block: 'b-table', quote: 'Results table: alignment measure AM, block 32 vs block 64', rev: 4, msgs: [
    { a: 'S', n: 'Supervisor', d: '10 Nov, 10:05', x: 'Is 0.41 vs 0.36 outside the uncertainty?', s: 'seen' }],
    draft: 'Not clearly. The difference is 0.05 and the combined uncertainty is also 0.05 (added in quadrature), so I would not call it separated. I proposed one sentence for Results that says this.' }
};
if (state === 'offline') { TH.t2.msgs[0].s = 'unseen'; TH.t4.msgs[0].s = 'unseen'; }

const NEW_P = 'The alignment measure is lower for block 64 than for block 32 (table below){+, but the difference of 0.05 equals the combined uncertainty of 0.05 (added in quadrature), so the two runs are not clearly separated+}. Block 64 also yields fewer samples, as expected from the larger blocks (about a quarter as many).';
const NEW_I = 'Larger blocks average over more of the field and smooth small-scale orientation changes, which {+may+} lower alignment in this cube{+. This is an interpretation, not validated against an independent estimate+}.';
const BL = [
  { id: 'b-title', k: 'h1', src: TITLE, edit: true },
  { id: 'b-meta', k: 'meta' },
  { id: 'b-hq', k: 'h2', src: 'Question' },
  { id: 'b-q', k: 'p', edit: true, src: 'Does doubling the block size from 32 to 64 pixels change the orientation of the gradient field enough to alter its alignment with the H I filaments? The choice of mask is treated separately in [[Noise masking threshold sweep]].' },
  { id: 'b-hs', k: 'h2', src: 'Setup' },
  { id: 'b-setup', k: 'p', edit: true, src: 'Both runs use the same input, `lmc_hi_cube.fits`, over the channel range −12…+12 km/s. Only the block size differs.', anchors: [{ t: 't1', text: '−12…+12 km/s' }] },
  { id: 'b-runs', k: 'runs' },
  { id: 'b-hr', k: 'h2', src: 'Results' },
  { id: 'b-res-intro', k: 'p', edit: true, src: 'Figures show axial orientation (0° and 180° are the same orientation), masked where reliability is below 0.5.', anchors: [{ t: 't3', whole: true }] },
  { id: 'b-figs', k: 'figs' },
  { id: 'b-res-p', k: 'p', edit: true, src: 'The alignment measure is lower for block 64 than for block 32 (table below). Block 64 also yields fewer samples, as expected from the larger blocks (about a quarter as many).', prop: { src: NEW_P, state: 'pending' } },
  { id: 'b-table', k: 'table' },
  { id: 'b-eq', k: 'eq' },
  { id: 'b-hi', k: 'h2', src: 'Interpretation' },
  { id: 'b-interp', k: 'p', edit: true, interp: true, src: 'Larger blocks average over more of the field and smooth small-scale orientation changes, which lowers alignment in this cube.', prop: { src: NEW_I, state: 'pending' } },
  { id: 'b-hn', k: 'h2', src: 'Next steps' },
  { id: 'b-next', k: 'ul' },
  { id: 'b-backs', k: 'backs' }
];
const blk = id => BL.find(b => b.id === id);
const pending = () => BL.filter(b => b.prop && b.prop.state === 'pending');
const showProps = role === 'owner';

/* ---------- renderers ---------- */
function wl(title) {
  const r = repByTitle(title);
  if (role === 'owner') return '<a class="wl" href="#" data-wl="' + esc(title) + '">' + esc(title) + '</a>' + (r && !r.shared ? ' <span class="pill un">' + ic('lock') + 'not shared</span>' : '');
  if (r && r.shared) return '<a class="wl" href="#" data-wl="' + esc(title) + '">' + esc(title) + '</a>';
  return '<span class="ns">not shared with you</span>';
}
function md(s) {
  let h = esc(s).replace(/`([^`]+)`/g, '<code>$1</code>').replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>');
  return h.replace(/\[\[([^\]]+)\]\]/g, (m, t) => wl(t.replace(/&amp;/g, '&')));
}
const mark = (t, inner, cls) => '<mark class="cm' + (TH[t].resolved ? ' resolved' : '') + (cls ? ' ' + cls : '') + '" tabindex="0" role="button" data-th="' + t + '" aria-label="Comment thread on this text' + (TH[t].resolved ? ', resolved' : '') + '">' + inner + '</mark>';
function withAnchors(b, html) {
  (b.anchors || []).forEach(a => {
    if (!TH[a.t]) return;
    if (a.whole) html = mark(a.t, html, 'stale');
    else html = html.replace(esc(a.text), m => mark(a.t, m));
  });
  return html;
}
const threadsOf = id => Object.values(TH).filter(t => t.block === id);
function gut(b) {
  const ts = threadsOf(b.id), open = ts.filter(t => !t.resolved).length;
  if (ts.length) return '<button class="gut has' + (open ? '' : ' allres') + '" data-gut="' + b.id + '" aria-label="' + ts.length + ' comment thread' + (ts.length > 1 ? 's' : '') + (open ? '' : ', all resolved') + ' on this block">' + ic('msg') + ts.length + '</button>';
  return '<button class="gut add" data-gut="' + b.id + '" aria-label="Add comment on this block">' + ic('plus') + '</button>';
}
const diffWords = s => esc(s).replace(/\{\+(.+?)\+\}/g, '<mark class="w">$1</mark>');
const clean = s => s.replace(/\{\+(.+?)\+\}/g, '$1');

function figHTML(n, src, cap, prov, pin) {
  return '<figure class="fig"><div class="im"><img src="../_shared/assets/' + src + '" alt="' + esc(cap) + '" width="512" height="512">' +
    '<span class="tag">' + ic('image') + (role === 'owner' ? 'PINNED FIGURE · PICTURE' : 'FROZEN PREVIEW') + '</span>' + (pin || '') + '</div>' +
    '<figcaption><strong>Figure ' + n + '.</strong> ' + cap + '<span class="prov">' + prov + '</span></figcaption></figure>';
}
function figsHTML() {
  const t2 = TH.t2;
  const pin = '<button class="pin' + (t2.resolved ? ' resolved' : '') + '" style="left:58%;top:36%" data-th="t2" aria-label="Comment thread on Figure 2 at upper right' + (t2.resolved ? ', resolved' : '') + '">' + t2.msgs.length + '</button>';
  return '<div class="figs">' +
    figHTML(1, 'vgt_baseline_axial.png', 'Block 32, axial orientation, v = −4…+4 km/s.', 'lmc_hi_cube.fits · v −4…+4 km/s · run 2026-11-08_a1f3c · header values as declared, not verified') +
    figHTML(2, 'vgt_candidate_axial.png', 'Block 64, axial orientation, v = −4…+4 km/s.', 'lmc_hi_cube.fits · v −4…+4 km/s · run 2026-11-08_9be20 · header values as declared, not verified', pin) +
    '</div><div class="legend"><img src="../_shared/assets/axial_legend.png" alt="Cyclic colour scale for axial angle, 0 to 180 degrees" width="361" height="24"><span>Colour is axial angle, cyclic: 0° and 180° are the same colour. Previews are pictures, not data.</span></div>';
}
function runsHTML() {
  const r = (id, bs) => '<div class="run">' + ic('run') + '<span class="id">' + id + '</span><span>block ' + bs + '</span><span class="t2">input <span class="mono">lmc_hi_cube.fits</span></span><span class="pill">completed</span><span class="why">Computation finished; no scientific QC has been assessed for this run.</span></div>';
  return '<div class="runs">' + r('2026-11-08_a1f3c', 32) + r('2026-11-08_9be20', 64) + '</div>';
}
function tableHTML() {
  return '<div class="tblw"><table class="tbl"><caption>Results, same input and channel range. — means not stated.</caption><thead><tr><th>Quantity</th><th>Type</th><th>Block 32</th><th>Block 64</th></tr></thead><tbody>' +
    '<tr><td>Alignment measure AM</td><td>axial, dimensionless</td><td>0.41 ± 0.03</td><td>0.36 ± 0.04</td></tr>' +
    '<tr><td>Valid samples</td><td>count</td><td>18 420</td><td>4 612</td></tr>' +
    '<tr><td>Reliability threshold</td><td>mask</td><td>0.5</td><td>0.5</td></tr>' +
    (role === 'owner' ? '<tr><td>Uncertainty method</td><td>declared</td><td>—</td><td>—</td></tr>' : '') +
    '</tbody></table></div>';
}
function eqHTML() {
  return '<div class="eqn"><math display="block" aria-label="AM equals the average of cosine of two theta r"><mrow><mi>AM</mi><mo>=</mo><mo>⟨</mo><mi>cos</mi><mo>⁡</mo><mo>(</mo><mn>2</mn><msub><mi>θ</mi><mi>r</mi></msub><mo>)</mo><mo>⟩</mo></mrow></math><span class="num">(1)</span></div>' +
    '<p class="eqcap">Mean over valid samples. θ<sub>r</sub> is the axial angle between the gradient orientation and the reference orientation, so 0° and 180° are equivalent; the average is taken of cos 2θ<sub>r</sub>, never of the angles themselves.</p>';
}
function nextHTML() {
  return '<ul><li>Rerun both with the threshold chosen in ' + md('[[Noise masking threshold sweep]]') + '.</li>' +
    '<li>Log the outcome in ' + md('[[Weekly notes 2026-W45]]') + '.</li>' +
    (role === 'owner' ? '<li>Add block 48 as a third run (same input, same channels).</li>' : '') + '</ul>';
}
function backsHTML() {
  const items = [{ r: 'r2', q: '…the 3σ cut is applied before blocking, as in [Block size 32 vs 64]…' }, { r: 'r3', q: '…compare with the block-size result before Thursday…' }];
  const li = items.map(i => REP.find(r => r.id === i.r)).map((r, n) => (role === 'owner' || r.shared) ? '<li><a href="#" data-wl="' + esc(r.t) + '">' + esc(r.t) + '</a>' + (r.shared ? '' : ' <span class="pill un">' + ic('lock') + 'not shared</span>') + '<q>' + esc(items[n].q) + '</q></li>' : '').join('');
  return '<aside class="backs" aria-labelledby="bk"><h2 id="bk">Linked from</h2><ul>' + li + '</ul></aside>';
}
function metaHTML() {
  const attr = '<span class="attr">' + ic('spark') + 'Drafted by VGT agent, edited by Denis</span>';
  if (role === 'owner') return '<div class="meta"><span class="pill acc">Draft rev 5</span><span>published rev 4 · 9 Nov 2026</span>' + attr + '</div>';
  return '<div class="meta"><span class="pill ok">Rev 4</span><span>published 9 Nov 2026</span>' + attr + '</div>';
}
function textBlock(b) {
  const live = b.prop && b.prop.state === 'pending' && showProps;
  if (live) return diffBlock(b);
  let html = withAnchors(b, md(b.src));
  let inner = '<p>' + html + '</p>';
  if (b.interp) inner = '<div class="interp"><div class="blabel"><span class="pill warn">' + ic('warn') + 'Interpretation, not validated</span><span class="attr">' + ic('spark') + 'Drafted by VGT agent, edited by Denis</span></div>' + inner + '</div>';
  return inner;
}
function diffBlock(b) {
  return '<div class="diff" data-diff="' + b.id + '"><div class="dh">' + ic('spark') + '<strong>VGT agent proposed this change</strong><span class="sp"></span><span>replaces rev 4 text</span></div>' +
    '<div class="dl del"><b aria-hidden="true">−</b><p><span class="sr-only">Removed: </span>' + esc(b.src) + '</p></div>' +
    '<div class="dl ins"><b aria-hidden="true">+</b><p><span class="sr-only">Added: </span>' + diffWords(b.prop.src) + '</p></div>' +
    '<div class="da"><button class="btn pri sm" data-acc="' + b.id + '">' + ic('check') + 'Accept</button><button class="btn sm" data-rej="' + b.id + '">Keep rev 4 text</button></div></div>';
}
function blockHTML(b) {
  const g = ['p', 'runs', 'figs', 'table', 'eq', 'ul'].includes(b.k) ? gut(b) : '';
  let c = '', cls = 'blk', attrs = ' data-b="' + b.id + '"';
  switch (b.k) {
    case 'h1': c = '<h1>' + esc(b.src) + '</h1>'; cls += ' hd'; break;
    case 'meta': c = metaHTML(); break;
    case 'h2': c = '<h2>' + esc(b.src) + '</h2>'; cls += ' hd'; break;
    case 'p': c = textBlock(b); break;
    case 'runs': c = runsHTML(); break;
    case 'figs': c = figsHTML(); break;
    case 'table': c = (role === 'owner' ? '<div class="blabel"><span class="pill acc">changed since rev 4</span></div>' : '') + tableHTML(); break;
    case 'eq': c = eqHTML(); break;
    case 'ul': c = (role === 'owner' ? '<div class="blabel"><span class="pill acc">changed since rev 4</span></div>' : '') + nextHTML(); break;
    case 'backs': return backsHTML();
  }
  const editable = role === 'owner' && b.edit && !(b.prop && b.prop.state === 'pending');
  if (editable) { cls += ' '; attrs += ' data-edit tabindex="0" aria-label="Edit this block as Markdown (Enter)"'; }
  if (b.changed) cls += ' changed';
  return '<div class="' + cls + '"' + attrs + '>' + c + (b.changed ? '<span class="edited">edited since rev 4</span>' : '') + g + '</div>';
}
function renderDoc() {
  const d = $('#doc'); if (!d) return;
  const st = $('.scroll') ? $('.scroll').scrollTop : 0;
  d.innerHTML = BL.map(blockHTML).join('');
  if ($('.scroll')) $('.scroll').scrollTop = st;
  updateBanner(); updateCounts();
}

/* ---------- threads ---------- */
function msgStatus(m) {
  if (role !== 'reader' || m.a !== 'S') return '';
  if (m.s === 'seen') return '<div class="st">' + ic('check') + 'seen by the author</div>';
  if (m.s === 'unseen') return '<div class="st wait">' + ic('clock') + 'Saved on the publish server. The author has not seen it yet' + (state === 'offline' ? ' (workspace offline)' : '') + '.</div>';
  if (m.s === 'sent') return '<div class="st wait">' + ic('clock') + 'Saved on the publish server just now. The author has not seen it yet.</div>';
  return '';
}
function msgHTML(m) {
  const cls = m.a === 'D' ? 'me' : (m.a === 'G' ? 'ag' : '');
  return '<div class="cmt"><div class="av ' + cls + '" aria-hidden="true">' + (m.a === 'S' ? 'S' : m.a === 'D' ? 'D' : 'AG') + '</div><div><div class="who"><b>' + esc(m.n) + '</b><span>' + esc(m.d) + '</span></div><p>' + esc(m.x) + '</p>' + msgStatus(m) + '</div></div>';
}
function threadHTML(t, opts) {
  opts = opts || {};
  const staleLine = t.stale ? '<span class="warnt">on revision ' + t.rev + ' · this paragraph changed since</span>' : (t.changed ? '<span class="warnt">on revision ' + t.rev + ' · this paragraph changed since</span>' : '<span>on revision ' + t.rev + '</span>');
  const head = '<div class="th-h">' + (t.resolved ? '<span class="pill ok">' + ic('check') + 'Resolved</span>' : '<span class="pill acc">Open</span>') + (t.isNew && role === 'owner' ? '<span class="pill warn">new</span>' : '') + staleLine + '<span class="sp"></span>' +
    (role === 'owner' ? '<button class="btn sm" data-resolve="' + t.id + '">' + (t.resolved ? 'Reopen' : ic('check') + 'Resolve') + '</button>' : '') +
    (opts.goto ? '<button class="btn sm ghost" data-goto="' + t.id + '">Go to text</button>' : '') + '</div>';
  const draft = (role === 'owner' && t.draft) ? '<div class="draft"><div class="attr">' + ic('spark') + 'Draft reply by VGT agent · not sent · readers cannot see it</div><p>' + esc(t.draft) + '</p><div class="da2"><button class="btn pri sm" data-send="' + t.id + '">' + ic('send') + 'Send as Denis</button><button class="btn sm" data-editdraft="' + t.id + '">' + ic('edit') + 'Edit first</button><button class="btn sm ghost" data-discard="' + t.id + '">Discard</button></div></div>' : '';
  const label = (role === 'owner' ? 'Reply as Denis' : 'Reply to this thread');
  const comp = '<form class="comp" data-comp="' + t.id + '"><label class="sr-only" for="c-' + t.id + (opts.s || '') + '">' + label + '</label><textarea id="c-' + t.id + (opts.s || '') + '" placeholder="' + label + '…"></textarea><div class="act"><span class="hint">Ctrl+Enter to send</span><button class="btn pri sm" type="submit">' + (role === 'owner' ? 'Reply' : 'Reply') + '</button></div></form>';
  return '<article class="th" data-thread="' + t.id + '" aria-label="Comment thread">' + head + '<blockquote class="q">' + esc(t.quote) + '</blockquote>' + t.msgs.map(msgHTML).join('') + draft + comp + '</article>';
}
function newThreadHTML(bid) {
  return '<article class="th" aria-label="New comment thread"><div class="th-h"><span class="pill">New thread</span><span>on this block, revision ' + (role === 'reader' ? 4 : 5) + '</span></div>' +
    '<form class="comp" data-newcomp="' + bid + '"><label class="sr-only" for="nc-' + bid + '">New comment</label><textarea id="nc-' + bid + '" placeholder="Comment on this block…"></textarea><div class="act"><span class="hint">Ctrl+Enter to send</span><button class="btn pri sm" type="submit">Comment</button></div></form></article>';
}
function blockQuote(bid) {
  const b = blk(bid); return b ? b.src ? b.src.replace(/\[\[|\]\]|`/g, '') : (bid === 'b-table' ? 'Results table' : 'Figures') : '';
}

/* popover and drawer */
let popEl, drawerEl, lastFocus = null, popFor = null;
function closePop(ret) {
  if (!popEl || popEl.hidden) return;
  popEl.hidden = true; $$('.cm.active').forEach(e => e.classList.remove('active'));
  if (ret && lastFocus && document.contains(lastFocus)) lastFocus.focus();
  popFor = null;
}
function openThreads(bid, anchor, only) {
  lastFocus = anchor;
  if (narrow()) { openDrawer({ only: only, block: bid, sheet: true }); return; }
  const ts = only ? [TH[only]] : threadsOf(bid);
  popFor = { bid, only };
  popEl.innerHTML = '<div class="dhd"><h2>' + (ts.length ? 'Comments' : 'New comment') + '</h2><button class="btn ghost ic1" data-closepop aria-label="Close comments">' + ic('x') + '</button></div>' + (ts.length ? ts.map(t => threadHTML(t, { s: 'p' })).join('') : newThreadHTML(bid));
  popEl.hidden = false;
  const wrap = $('#docwrap').getBoundingClientRect(), r = anchor.getBoundingClientRect();
  const gutterSide = anchor.classList.contains('gut');
  let left = gutterSide ? wrap.width - popEl.offsetWidth - 8 : r.left - wrap.left;
  left = Math.max(8, Math.min(left, wrap.width - popEl.offsetWidth - 8));
  popEl.style.left = left + 'px';
  popEl.style.top = (r.bottom - wrap.top + 8) + 'px';
  $$('.cm[data-th]').forEach(e => e.classList.toggle('active', only && e.dataset.th === only));
  const ta = $('textarea', popEl); popEl.setAttribute('role', 'dialog'); popEl.setAttribute('aria-label', 'Comments');
  popEl.scrollIntoView({ block: 'nearest' });
  (ta || popEl).focus({ preventScroll: true });
}
let drawerFilter = 'open';
function openDrawer(o) {
  o = o || {}; closePop(false);
  if (!o.sheet) lastFocus = document.activeElement;
  drawerEl.hidden = false; drawerEl.dataset.mode = o.sheet ? 'sheet' : 'all';
  drawerEl.dataset.only = o.only || ''; drawerEl.dataset.block = o.block || '';
  renderDrawer();
  const f = $('textarea', drawerEl) || $('.dhd button', drawerEl); (o.sheet ? f : $('.dhd button', drawerEl)).focus({ preventScroll: true });
  const b = $('[data-opencomments]'); if (b) b.setAttribute('aria-expanded', 'true');
}
function closeDrawer(ret) {
  if (!drawerEl || drawerEl.hidden) return; drawerEl.hidden = true;
  const b = $('[data-opencomments]'); if (b) b.setAttribute('aria-expanded', 'false');
  if (ret && lastFocus && document.contains(lastFocus)) lastFocus.focus();
}
function renderDrawer() {
  if (!drawerEl || drawerEl.hidden) return;
  const all = Object.values(TH), sheet = drawerEl.dataset.mode === 'sheet';
  let body, title;
  if (sheet) {
    const only = drawerEl.dataset.only, bid = drawerEl.dataset.block;
    const ts = only ? [TH[only]] : threadsOf(bid);
    title = ts.length ? 'Comments on this block' : 'New comment';
    body = (ts.length ? ts.map(t => threadHTML(t, { s: 's' })).join('') : newThreadHTML(bid)) + '<div class="dfilt"><button class="btn sm" data-allc>' + ic('list') + 'All comments (' + all.length + ')</button></div>';
  } else {
    const list = all.filter(t => drawerFilter === 'all' || (drawerFilter === 'open' ? !t.resolved : t.resolved));
    title = 'All comments';
    body = '<div class="dfilt"><div class="seg" role="group" aria-label="Filter comments">' + ['open', 'resolved', 'all'].map(f => '<button data-filt="' + f + '" aria-pressed="' + (drawerFilter === f) + '">' + f[0].toUpperCase() + f.slice(1) + ' <span class="n mono">' + all.filter(t => f === 'all' || (f === 'open' ? !t.resolved : t.resolved)).length + '</span></button>').join('') + '</div></div>' +
      (list.length ? list.map(t => threadHTML(t, { goto: true, s: 'd' })).join('') : '<p class="nocm">No ' + drawerFilter + ' comments.</p>');
  }
  drawerEl.innerHTML = '<div class="dhd"><h2>' + title + '</h2><button class="btn ghost ic1" data-closedrawer aria-label="Close comments">' + ic('x') + '</button></div><div class="dbody">' + body + '</div>';
}
function updateCounts() {
  const n = Object.values(TH).length;
  $$('[data-ccount]').forEach(e => e.textContent = n);
}
function refresh() {
  const keep = popFor; const wasOpen = popEl && !popEl.hidden;
  renderDoc(); renderDrawer();
  if (wasOpen && keep) { const a = keep.only ? $('[data-th="' + keep.only + '"]') : $('[data-gut="' + keep.bid + '"]'); if (a) openThreads(keep.bid, a, keep.only); }
}
function submit(f) {
  const ta = $('textarea', f), x = ta.value.trim(); if (!x) { ta.focus(); return; }
  const msg = { a: role === 'owner' ? 'D' : 'S', n: role === 'owner' ? 'Denis' : 'Supervisor', d: role === 'owner' ? '10 Nov, 15:40' : '10 Nov, 13:12', x: x, s: 'sent' };
  if (f.dataset.comp) { TH[f.dataset.comp].msgs.push(msg); const t = TH[f.dataset.comp]; if (role === 'owner') t.isNew = false; }
  else { const id = 't' + (Object.keys(TH).length + 1); TH[id] = { id, block: f.dataset.newcomp, quote: blockQuote(f.dataset.newcomp), rev: role === 'reader' ? 4 : 5, msgs: [msg] }; if (popFor) popFor = { bid: f.dataset.newcomp, only: null }; if (drawerEl && drawerEl.dataset.mode === 'sheet') drawerEl.dataset.block = f.dataset.newcomp; }
  refresh();
  toast(role === 'reader' ? 'Comment saved on the publish server. The author sees it when their workspace next syncs.' : 'Reply saved. Readers see it after the next publish sync.');
  const live = $('.th[data-thread="' + (f.dataset.comp || '') + '"] textarea', popEl); if (live) live.focus();
}

/* ---------- shell ---------- */
let toastT;
function toast(m) { const t = $('#toast'); t.textContent = m; t.hidden = false; clearTimeout(toastT); toastT = setTimeout(() => t.hidden = true, 3800); }

function treeItem(r) {
  const cur = r.cur && page !== 'graph';
  const lock = (role === 'owner' && !r.shared) ? '<span class="pill un" title="Not shared with any reader">' + ic('lock') + '</span>' : '';
  const dot = (role === 'reader' && r.cur) ? '<span class="dot" title="Updated since you last opened it"></span>' : '';
  const href = r.cur ? curHref : '#';
  return '<li data-title="' + esc(r.t.toLowerCase()) + '"><a href="' + href + '"' + (cur ? ' aria-current="page"' : '') + ' data-rep="' + r.id + '">' + ic('file') + '<span class="nm">' + esc(r.cur ? r.short : r.t) + '</span>' + dot + lock + '</a></li>';
}
function group(name, items, open) {
  return '<li class="grp"><button class="row" aria-expanded="' + (open !== false) + '">' + ic('cd').replace('class="ic"', 'class="ic cv"') + '<span class="nm">' + name + '</span><span class="mini">' + items.length + '</span></button><ul class="sub tree">' + items.map(treeItem).join('') + '</ul></li>';
}
function sideHTML() {
  if (state === 'empty' && role === 'reader') return '<div class="search"><label class="sr-only" for="q">Search reports</label>' + ic('search') + '<input id="q" type="search" placeholder="Search reports" disabled></div><div class="ph">Shared with you</div><p class="emptyt">No reports yet.</p>' + sideFoot();
  const shared = REP.filter(r => r.shared), notes = REP.filter(r => !r.shared);
  let t = '<ul class="tree" id="tree">' + (role === 'owner' ? group('Reports', shared) + group('Notes', notes) : group('Shared with you', shared)) + '</ul>';
  const lt = role === 'owner' ? [REP[1], REP[3]] : [REP[1]], lf = role === 'owner' ? [REP[1], REP[3]] : [REP[1]];
  const lkItem = r => '<li><a href="#" data-rep="' + r.id + '">' + ic('link') + '<span class="nm lk">' + esc(r.t) + '</span>' + ((role === 'owner' && !r.shared) ? '<span class="pill un">' + ic('lock') + '</span>' : '') + '</a></li>';
  t += '<div class="ph">Linked</div><ul class="tree">' + '<li class="grp"><button class="row" aria-expanded="true">' + ic('cd').replace('class="ic"', 'class="ic cv"') + '<span class="nm">Links to</span><span class="mini">' + lt.length + '</span></button><ul class="sub tree">' + lt.map(lkItem).join('') + '</ul></li>' +
    '<li class="grp"><button class="row" aria-expanded="true">' + ic('cd').replace('class="ic"', 'class="ic cv"') + '<span class="nm">Linked from</span><span class="mini">' + lf.length + '</span></button><ul class="sub tree">' + lf.map(lkItem).join('') + '</ul></li></ul>';
  return '<div class="search"><label class="sr-only" for="q">Search reports</label>' + ic('search') + '<input id="q" type="search" placeholder="Search reports"></div>' + t + sideFoot();
}
function sideFoot() {
  return role === 'owner'
    ? '<div class="sidefoot"><span>Space: LMC H I · 5 pages</span><span>Workspace online · publish server reachable</span></div>'
    : '<div class="sidefoot"><span>Reading as “Supervisor”, personal link</span><span>Read-only. Latest published revision of each report.</span></div>';
}
function modeSeg() {
  if (role === 'owner') return '<nav class="seg" aria-label="View"><a href="01-owner-editor.html"' + (page === 'editor' ? ' aria-current="page"' : '') + '>' + ic('file') + 'Page</a><a href="03-graph.html"' + (page === 'graph' ? ' aria-current="page"' : '') + '>' + ic('graph') + 'Graph</a></nav>';
  return '<div class="seg" role="group" aria-label="View"><button data-view="page" aria-pressed="true">' + ic('file') + 'Page</button><button data-view="graph" aria-pressed="false">' + ic('graph') + 'Graph</button></div>';
}
function ownerBar() {
  return modeSeg() + '<span class="crumb">Reports / Block size 32 vs 64</span><span class="sp"></span><div class="stat"><span class="pill acc">Draft rev 5</span><span class="pill ok">published rev 4</span><span class="pill">' + ic('users') + '2 readers</span></div>' +
    '<button class="btn" data-opencomments aria-expanded="false" aria-haspopup="dialog">' + ic('msg') + 'Comments <span class="n" data-ccount>4</span></button><button class="btn pri" data-publish>' + ic('up') + 'Publish…</button>';
}
function readerBar() {
  const ago = state === 'offline' ? '2 days ago' : '3 h ago';
  return modeSeg() + '<button class="btn" data-hist aria-expanded="false" aria-haspopup="menu">' + ic('history') + 'Rev 4 · 9 Nov ' + ic('cd') + '</button><span class="sp"></span><span class="pill ' + (state === 'offline' ? 'warn' : '') + '" id="sync">' + ic('clock') + 'Denis’s workspace last synced ' + ago + '</span>' +
    '<button class="btn" data-opencomments aria-expanded="false" aria-haspopup="dialog">' + ic('msg') + 'All comments <span class="n" data-ccount>4</span></button>';
}
function buildShell() {
  const app = $('#app');
  if (state === 'revoked' && role === 'reader') {
    document.body.classList.add('neutral');
    app.innerHTML = '<header class="top"><span class="sp"></span><span class="mockflag">ILLUSTRATIVE MOCK DATA</span></header><main class="center"><div class="msg"><h1>This link does not open anything.</h1><p>It may have been revoked, mistyped or expired. If you expected to read something, ask the person who sent it to you.</p></div></main><div class="toast" id="toast" hidden role="status"></div>';
    return false;
  }
  const mock = '<span class="mockflag">ILLUSTRATIVE MOCK DATA</span>';
  const roleP = role === 'owner' ? '<span class="pill">Owner</span>' : '<span class="pill">Reader</span>';
  const top = '<header class="top"><button class="btn ghost ic1 menubtn" data-menu aria-label="Open the page tree" aria-expanded="false">' + ic('menu') + '</button><span class="brand"><i></i>Vesqira<span class="sub t3">Reports</span></span><span class="crumb">LMC H I space</span><span class="sp"></span>' + mock + roleP + '<button class="btn ghost ic1" data-themebtn aria-label="Toggle light and dark theme">' + ic('moon') + '</button></header>';
  let main = '';
  const bar = '<div class="mbar">' + (role === 'owner' ? ownerBar() : readerBar()) + '</div>';
  if (page === 'graph') main = bar + '<div class="gview" id="gview" data-gv="graph"></div>';
  else {
    let banners = '';
    if (role === 'owner') banners = '<div class="banner" id="agentban"></div>';
    if (role === 'reader' && state === 'offline') banners = '<div class="banner warn" role="status">' + ic('warn') + '<span class="sp"><strong>The author’s workspace last synced 2 days ago.</strong> You can still comment: comments are saved on the publish server and reach the author when their workspace is next online. Replies will take at least that long.</span></div>';
    let content;
    if (role === 'reader' && state === 'empty') content = '<div class="center"><div class="msg"><h1>Nothing is shared in this space yet.</h1><p>The author has not published a report. Reports appear here as soon as they are published, always at their latest revision with its history.</p><p class="mini">Nothing has been withheld from you; there is just nothing to show.</p></div></div>';
    else content = '<div class="scroll"><div class="docwrap" id="docwrap"><article class="doc" id="doc" aria-label="Report"></article><div class="pop" id="pop" hidden tabindex="-1"></div></div></div>';
    main = bar + banners + content + (role === 'reader' ? '<div class="gview" id="gview" data-gv="graph" hidden></div>' : '');
  }
  app.innerHTML = top + '<div class="scrim" data-scrim></div><aside class="side" id="side" aria-label="Pages">' + sideHTML() + '</aside><main class="main" id="main">' + main + '<div class="drawer" id="drawer" hidden role="dialog" aria-label="Comments"></div><div class="histmenu" id="hist" hidden role="menu"></div></main><div class="toast" id="toast" hidden role="status"></div>';
  return true;
}

/* ---------- agent banner ---------- */
function updateBanner() {
  const b = $('#agentban'); if (!b) return;
  const n = pending().length;
  if (!n) { b.hidden = true; return; }
  b.hidden = false;
  b.innerHTML = ic('spark') + '<span class="sp"><strong>VGT agent proposed changes to ' + n + ' block' + (n > 1 ? 's' : '') + '</strong><span class="long"> \u00b7 review before they enter the draft. Nothing is published by the agent.</span></span><button class="btn sm" data-review>Review</button><button class="btn sm" data-accall>Accept all</button>';
}
function accept(id) { const b = blk(id); b.src = clean(b.prop.src); b.prop.state = 'accepted'; b.changed = true; renderDoc(); const nb = pending()[0]; const el = nb && $('[data-acc="' + nb.id + '"]'); if (el) el.focus(); else { const e = $('[data-b="' + id + '"]'); e && e.focus(); } }
function reject(id) { blk(id).prop.state = 'rejected'; renderDoc(); const e = $('[data-b="' + id + '"]'); e && e.focus(); }

/* ---------- inline edit ---------- */
function enterEdit(el) {
  if (el.classList.contains('editing')) return;
  const b = blk(el.dataset.b); if (!b) return;
  el.classList.add('editing');
  el.innerHTML = '<textarea class="src" aria-label="Markdown source of this block" rows="2">' + esc(b.src) + '</textarea><div class="srcnote">Markdown source for this block only. Esc or click away to render it again.</div>';
  const ta = $('textarea', el); const fit = () => { ta.style.height = 'auto'; ta.style.height = ta.scrollHeight + 2 + 'px'; };
  ta.addEventListener('input', fit); fit(); ta.focus(); ta.setSelectionRange(ta.value.length, ta.value.length);
  const done = () => {
    const v = ta.value.trim() || b.src;
    if (v !== b.src) { b.src = v; b.changed = true; b.anchors = []; threadsOf(b.id).forEach(t => t.changed = true); }
    renderDoc(); renderDrawer(); const e = $('[data-b="' + b.id + '"]'); if (e && done.key) e.focus();
  };
  ta.addEventListener('blur', () => { if (el.classList.contains('editing')) { el.classList.remove('editing'); done(); } });
  ta.addEventListener('keydown', e => { if (e.key === 'Escape') { e.preventDefault(); done.key = true; ta.blur(); } });
}

/* ---------- publish dialog ---------- */
function pfHTML() {
  return '<dialog id="pf" aria-labelledby="pf-t"><div class="dg-h">' + ic('up') + '<h2 id="pf-t" tabindex="-1">Publish rev 5 to the publish server</h2><button class="btn ghost ic1" data-pfclose aria-label="Close">' + ic('x') + '</button></div><div class="dg-b">' +
    '<div class="pf"><h3>' + ic('users') + 'Who can open it<span class="n">2 readers</span></h3><ul><li>' + ic('link') + '<span><strong>Supervisor</strong>, personal link. Last opened 10 Nov, 12:18.</span></li><li>' + ic('link') + '<span><strong>Committee member</strong>, personal link. Last opened —.</span></li></ul></div>' +
    '<div class="pf"><h3>' + ic('image') + 'Figures leave as pictures<span class="n">2</span></h3><div class="thumbs"><img src="../_shared/assets/vgt_baseline_axial.png" alt="Figure 1 preview"><img src="../_shared/assets/vgt_candidate_axial.png" alt="Figure 2 preview"></div><ul style="margin-top:8px"><li>' + ic('check') + '<span>Frozen preview images only. No arrays, no FITS data, no download of the cube. Each carries its provenance line: file name, velocity range, run id, “header values as declared, not verified”.</span></li></ul></div>' +
    '<div class="pf"><h3>' + ic('run') + 'Run lines shown to readers<span class="n">2</span></h3><ul><li>' + ic('run') + '<span><span class="mono">2026-11-08_a1f3c</span> · block 32 · input <span class="mono">lmc_hi_cube.fits</span> · completed, no scientific QC</span></li><li>' + ic('run') + '<span><span class="mono">2026-11-08_9be20</span> · block 64 · input <span class="mono">lmc_hi_cube.fits</span> · completed, no scientific QC</span></li></ul></div>' +
    '<div class="pf"><h3>' + ic('link') + 'Linked reports<span class="n">2</span></h3><ul><li>' + ic('check') + '<span><strong>Noise masking threshold sweep</strong> is shared. Readers can follow the link.</span></li><li>' + ic('lock') + '<span><strong>Weekly notes 2026-W45</strong> is not shared. Readers will see “not shared with you” in its place.</span></li></ul><label class="lbl"><input type="checkbox"> Also share “Weekly notes 2026-W45” with both readers</label></div>' +
    '<div class="pf"><h3>' + ic('history') + 'Changes from rev 4<span class="n">2 blocks</span></h3><ul><li><span class="pill ok">+</span><span>Results table: new row “Uncertainty method”, value — (not stated).</span></li><li><span class="pill ok">+</span><span>Next steps: “Add block 48 as a third run (same input, same channels).”</span></li><li><span class="pill warn">' + ic('spark') + '</span><span>2 blocks proposed by the VGT agent are <strong>not accepted</strong> and are left out of this revision.</span></li></ul></div>' +
    '<div class="pf"><h3>' + ic('ban') + 'Never included</h3><ul class="never"><li><span>File paths, host names, cluster or scheduler details, credentials, your workspace location, the agent’s drafts and unsent replies.</span></li></ul></div></div>' +
    '<div class="dg-f"><span class="hint">Readers always see the latest published revision, with history. Comments come back when your workspace is online.</span><button class="btn md" data-pfclose>Cancel</button><button class="btn pri md" data-pfgo>' + ic('up') + 'Publish rev 5</button></div></dialog>';
}

/* ---------- graph ---------- */
const GN = [
  { id: 'r5', type: 'report', label: 'Weekly notes 2026-W44', x: .55, y: .06, shared: false },
  { id: 'r3', type: 'report', label: 'Weekly notes 2026-W45', x: .16, y: .2, shared: false },
  { id: 'r2', type: 'report', label: 'Noise masking threshold sweep', x: .82, y: .2, shared: true },
  { id: 'r1', type: 'report', label: 'Block size 32 vs 64', x: .5, y: .45, shared: true, cur: true },
  { id: 'r4', type: 'report', label: 'Baseline VGT on the LMC cube', x: .17, y: .78, shared: true },
  { id: 'a1', type: 'run', label: '2026-11-08_a1f3c', meta: 'block 32 · input lmc_hi_cube.fits · computation completed, no scientific QC', x: .3, y: .5 },
  { id: 'a2', type: 'run', label: '2026-11-08_9be20', meta: 'block 64 · input lmc_hi_cube.fits · computation completed, no scientific QC', x: .72, y: .5 },
  { id: 'a3', type: 'run', label: '2026-11-06_41d7e', meta: 'threshold sweep · input lmc_hi_cube.fits · computation completed, no scientific QC', x: .88, y: .42 },
  { id: 'a4', type: 'run', label: '2026-11-03_c08a2', meta: 'baseline · input lmc_hi_cube.fits · computation completed, no scientific QC', x: .06, y: .5 },
  { id: 'f1', type: 'figure', label: 'Fig 1: block 32', meta: 'picture, not data · header values as declared, not verified', x: .36, y: .74 },
  { id: 'f2', type: 'figure', label: 'Fig 2: block 64', meta: 'picture, not data · header values as declared, not verified', x: .66, y: .74 },
  { id: 'f3', type: 'figure', label: 'Fig: baseline map', meta: 'picture, not data · header values as declared, not verified', x: .4, y: .88 }
];
const GE = [
  ['r1', 'r2', 'link', 'links both ways'], ['r1', 'r3', 'link', 'links both ways'], ['r3', 'r5', 'link', 'links to'], ['r3', 'r4', 'link', 'links to'],
  ['r1', 'f1', 'embed', 'embeds'], ['r1', 'f2', 'embed', 'embeds'], ['r4', 'f3', 'embed', 'embeds'],
  ['r1', 'a1', 'embed', 'cites run'], ['r1', 'a2', 'embed', 'cites run'], ['r2', 'a3', 'embed', 'cites run'], ['r4', 'a4', 'embed', 'cites run'],
  ['a1', 'f1', 'made', 'produced'], ['a2', 'f2', 'made', 'produced'], ['a4', 'f3', 'made', 'produced']
];
function initGraph(el, reader) {
  const show = { report: true, run: !reader, figure: !reader };
  let sel = null;
  const nodes = () => GN.filter(n => show[n.type] && !(reader && (n.type !== 'report' || !n.shared)));
  const typeName = { report: 'Report', run: 'Run', figure: 'Figure' };
  const byId = id => GN.find(n => n.id === id);
  const conns = id => GE.filter(e => e[0] === id || e[1] === id).map(e => { const o = byId(e[0] === id ? e[1] : e[0]); return { o, rel: e[0] === id ? e[3] : (e[3] === 'links both ways' ? e[3] : e[3] === 'links to' ? 'linked from' : e[3] === 'embeds' ? 'embedded in' : e[3] === 'cites run' ? 'cited by' : 'made by') }; }).filter(c => nodes().includes(c.o));
  el.innerHTML = '<div class="gtool"><span class="mini" style="color:var(--text-2)">' + (reader ? 'Reports shared with you' : 'Show') + '</span>' +
    (reader ? '' : ['report', 'run', 'figure'].map(t => '<label><input type="checkbox" data-gt="' + t + '"' + (show[t] ? ' checked' : '') + '> ' + typeName[t] + 's</label>').join('')) +
    '<span class="sp"></span><div class="seg gseg" role="group" aria-label="Graph or list"><button data-gv="graph" aria-pressed="true">' + ic('graph') + 'Graph</button><button data-gv="list" aria-pressed="false">' + ic('list') + 'List</button></div></div>' +
    '<div class="gbody"><div class="gcanvas" id="gc"><svg id="gsvg" role="group" aria-label="Link graph. The list beside it has the same content."></svg><div class="gkey" aria-hidden="true"><span><svg viewBox="0 0 24 12"><circle cx="12" cy="6" r="4.5" class="gi2"/></svg>report</span><span><svg viewBox="0 0 24 12"><rect x="7" y="1.5" width="10" height="9" rx="2" class="gi2"/></svg>run</span><span><svg viewBox="0 0 24 12"><path d="M12 .5 18 6l-6 5.500L6 6z" class="gi2"/></svg>figure</span><span><svg viewBox="0 0 24 12"><path d="M1 6h22" class="gedge link"/></svg>link</span><span><svg viewBox="0 0 24 12"><path d="M1 6h22" class="gedge embed"/></svg>embeds</span><span><svg viewBox="0 0 24 12"><path d="M1 6h22" class="gedge made"/></svg>produced</span></div><div class="gdet" id="gdet" hidden></div></div><div class="glist" id="glist"></div></div>';
  el.dataset.gv = 'graph';
  const wrapLabel = (s, n) => { const w = s.split(/(?<=[\s:_-])/), out = []; let cur = ''; w.forEach(x => { if ((cur + x).trim().length > n && cur) { out.push(cur.trim()); cur = x; } else cur += x; }); if (cur.trim()) out.push(cur.trim()); return out.slice(0, 3); };
  function draw() {
    const gc = $('#gc', el), svg = $('#gsvg', el); const W = gc.clientWidth || 600, H = gc.clientHeight || 400; if (W < 50) return;
    const padX = W < 500 ? 62 : 90, padT = 28, padB = W < 500 ? 56 : 64;
    const P = {}; GN.forEach(n => P[n.id] = [padX + n.x * (W - 2 * padX), padT + n.y * (H - padT - padB)]);
    const vis = nodes(), vid = new Set(vis.map(n => n.id)); const nb = new Set(sel ? [sel].concat(conns(sel).map(c => c.o.id)) : []);
    let s = '';
    GE.forEach(e => { if (!vid.has(e[0]) || !vid.has(e[1])) return; const a = P[e[0]], b = P[e[1]]; const hl = sel && (e[0] === sel || e[1] === sel); s += '<line class="gedge ' + e[2] + (hl ? ' hl' : '') + (sel && !hl ? ' dim' : '') + '" x1="' + a[0] + '" y1="' + a[1] + '" x2="' + b[0] + '" y2="' + b[1] + '"/>'; });
    vis.forEach(n => {
      const [x, y] = P[n.id]; let shape, r = 11;
      if (n.type === 'report') shape = '<circle class="sh" r="11"/>';
      else if (n.type === 'run') shape = '<rect class="sh" x="-10" y="-10" width="20" height="20" rx="3"/>';
      else shape = '<path class="sh" d="M0 -13 13 0 0 13 -13 0z"/>';
      const cls = 'gnode' + (n.cur ? ' cur' : '') + (n.type === 'report' && !n.shared && !reader ? ' ns' : '') + (sel === n.id ? ' sel' : '') + (sel && !nb.has(n.id) ? ' dim' : '');
      const sm = W < 500; const base = sm && n.type === 'run' ? n.label.slice(-5) : sm && n.type === 'figure' ? n.label.replace(/:.*/, '').replace('Fig', 'Fig') + (n.id === 'f3' ? ' base' : (n.id === 'f1' ? ' 1' : '')) : n.label;
      const lab = wrapLabel((n.id === 'f1' && sm ? 'Fig 1' : n.id === 'f2' && sm ? 'Fig 2' : n.id === 'f3' && sm ? 'Fig base' : base) + (n.type === 'report' && !n.shared && !reader ? (sm ? ' (private)' : ' (not shared)') : ''), sm ? 14 : 24);
      const text = '<text text-anchor="middle" class="' + (n.type === 'run' ? 'mono' : '') + '" y="' + (r + 16) + '">' + lab.map((l, i) => '<tspan x="0" dy="' + (i ? 15 : 0) + '">' + esc(l) + '</tspan>').join('') + '</text>';
      const inner = n.type === 'report' ? '<path class="gi2" d="M-3 -4h4l2 2v6h-6z"/>' : n.type === 'run' ? '<path class="gi2" d="M-2 -4 4 0-2 4z"/>' : '';
      s += '<g class="' + cls + '" transform="translate(' + x + ' ' + y + ')" tabindex="0" role="button" aria-pressed="' + (sel === n.id) + '" aria-label="' + esc(typeName[n.type] + ': ' + n.label + (n.type === 'report' && !n.shared && !reader ? ', not shared' : '')) + '" data-n="' + n.id + '">' + shape + inner + text + '</g>';
    });
    svg.setAttribute('viewBox', '0 0 ' + W + ' ' + H); svg.innerHTML = s;
    const det = $('#gdet', el);
    if (sel && !narrow()) { const n = byId(sel); det.hidden = false; det.innerHTML = '<span class="k">' + typeName[n.type] + (n.type === 'report' ? (n.shared ? ' · shared' : ' · not shared') : '') + '</span><span class="t">' + esc(n.label) + '</span>' + (n.meta ? '<span class="k">' + esc(n.meta) + '</span>' : '') + (n.type === 'report' ? '<div style="margin-top:8px"><button class="btn sm" data-gopen="' + n.id + '">Open page</button></div>' : ''); } else det.hidden = true;
    list();
  }
  function list() {
    const L = $('#glist', el); let h = '';
    ['report', 'run', 'figure'].forEach(t => {
      const ns = nodes().filter(n => n.type === t); if (!ns.length) return;
      h += '<div class="ph">' + typeName[t] + 's<span class="mini">' + ns.length + '</span></div><ul>' + ns.map(n => {
        const cs = sel === n.id ? conns(n.id) : [];
        return '<li><button class="gi" data-gl="' + n.id + '" aria-pressed="' + (sel === n.id) + '">' + ic(t === 'report' ? 'file' : t === 'run' ? 'run' : 'image') + '<span class="nm' + (t === 'run' ? ' mono' : '') + '">' + esc(n.label) + '</span>' + (n.type === 'report' ? (n.shared ? (n.cur ? '<span class="pill acc">this page</span>' : '') : (reader ? '' : '<span class="pill un">' + ic('lock') + 'not shared</span>')) : '') + '</button>' +
          '<div class="conn"' + (sel === n.id ? '' : ' hidden') + '>' + (n.meta ? '<span>' + esc(n.meta) + '</span>' : '') + cs.map(c => '<span>' + c.rel + ' <strong>' + esc(c.o.label) + '</strong></span>').join('') + (n.type === 'report' ? '<span><button class="btn sm" data-gopen="' + n.id + '">Open page</button></span>' : '') + '</div></li>';
      }).join('') + '</ul>';
    });
    L.innerHTML = h;
  }
  const pick = id => { sel = sel === id ? null : id; draw(); const t = $('[data-n="' + id + '"]', el); const b = $('[data-gl="' + id + '"]', el); const act = document.activeElement; if (t && !(act && act.matches('[data-gl]'))) t.focus(); else if (b) b.focus(); };
  el.addEventListener('click', e => {
    const n = e.target.closest('[data-n]'), g = e.target.closest('[data-gl]'), o = e.target.closest('[data-gopen]'), gv = e.target.closest('[data-gv]');
    if (o) { toast('Prototype: only one report page is built.'); return; }
    if (n) pick(n.dataset.n); else if (g) pick(g.dataset.gl);
    else if (gv && gv.matches('button')) { el.dataset.gv = gv.dataset.gv; $$('.gseg button', el).forEach(b => b.setAttribute('aria-pressed', b === gv)); draw(); }
  });
  el.addEventListener('change', e => { const t = e.target.dataset.gt; if (t) { show[t] = e.target.checked; if (sel && !nodes().find(n => n.id === sel)) sel = null; draw(); } });
  el.addEventListener('keydown', e => {
    const g = e.target.closest && e.target.closest('[data-n]'); if (!g) return;
    if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); pick(g.dataset.n); return; }
    const dirs = { ArrowRight: [1, 0], ArrowLeft: [-1, 0], ArrowUp: [0, -1], ArrowDown: [0, 1] }, d = dirs[e.key]; if (!d) return;
    e.preventDefault(); const here = byId(g.dataset.n); let best = null, bs = 1e9;
    nodes().forEach(n => { if (n === here) return; const dx = n.x - here.x, dy = n.y - here.y, along = dx * d[0] + dy * d[1]; if (along <= 0.01) return; const across = Math.abs(dx * d[1] + dy * d[0]); const sc = Math.hypot(dx, dy) + across * 1.5; if (sc < bs) { bs = sc; best = n; } });
    if (best) { const t = $('[data-n="' + best.id + '"]', el); t && t.focus(); }
  });
  new ResizeObserver(draw).observe($('#gc', el));
  draw();
  return draw;
}

/* ---------- history menu ---------- */
function histHTML() {
  return [['Rev 4', '9 Nov 2026', 'current'], ['Rev 3', '6 Nov 2026', ''], ['Rev 2', '4 Nov 2026', ''], ['Rev 1', '2 Nov 2026', '']].map(r => '<button role="menuitem" data-rev="' + r[0] + '">' + r[0] + '<span class="mini">' + r[1] + '</span>' + (r[2] ? '<span class="pill ok">latest</span>' : '') + '</button>').join('');
}

/* ---------- init and events ---------- */
const hasDoc = buildShell();
if (!hasDoc) return;
popEl = $('#pop'); drawerEl = $('#drawer');
if (page !== 'graph' && $('#doc')) renderDoc();
if (role === 'owner' && page === 'editor') {
  document.body.insertAdjacentHTML('beforeend', pfHTML());
}
let redrawGraph = null;
if (page === 'graph') redrawGraph = initGraph($('#gview'), false);
if (role === 'reader' && $('#gview')) redrawGraph = initGraph($('#gview'), true);
updateCounts();
$('#hist') && ($('#hist').innerHTML = histHTML());

const app = $('#app');
function openPF() { const d = $('#pf'); if (d && !d.open) d.showModal(); }
document.addEventListener('click', e => {
  const t = e.target, c = s => t.closest(s);
  let el;
  if (c("[data-themebtn]")) { const dark = getComputedStyle(root).colorScheme === 'dark'; root.dataset.theme = dark ? 'light' : 'dark'; return; }
  if (c('[data-menu]')) { const o = app.classList.toggle('drawer-open'); $('[data-menu]').setAttribute('aria-expanded', o); return; }
  if (c('[data-scrim]')) { app.classList.remove('drawer-open'); return; }
  if ((el = c('.grp>button.row'))) { el.setAttribute('aria-expanded', el.getAttribute('aria-expanded') !== 'true'); return; }
  if ((el = c('a[data-rep],a[data-wl]'))) { if (!(el.dataset.rep && REP.find(r => r.id === el.dataset.rep).cur) ) { e.preventDefault(); toast('Prototype: only one report page is built.'); } return; }
  if ((el = c('[data-gut]'))) { openThreads(el.dataset.gut, el); return; }
  if ((el = c('.cm[data-th],.pin[data-th]'))) { const th = TH[el.dataset.th]; openThreads(th.block, el, th.id); return; }
  if (c('[data-closepop]')) { closePop(true); return; }
  if (c('[data-opencomments]')) { drawerEl.hidden ? openDrawer({}) : closeDrawer(true); return; }
  if (c('[data-closedrawer]')) { closeDrawer(true); return; }
  if (c('[data-allc]')) { openDrawer({}); return; }
  if ((el = c('[data-filt]'))) { drawerFilter = el.dataset.filt; renderDrawer(); const b = $('[data-filt="' + drawerFilter + '"]'); b && b.focus(); return; }
  if ((el = c('[data-goto]'))) { const th = TH[el.dataset.goto]; closeDrawer(false); const target = $('[data-b="' + th.block + '"]'); if (target) { target.scrollIntoView({ block: 'center' }); const m = $('.cm[data-th="' + th.id + '"],.pin[data-th="' + th.id + '"],[data-gut="' + th.block + '"]', target); m && m.focus(); } return; }
  if ((el = c('[data-resolve]'))) { const th = TH[el.dataset.resolve]; th.resolved = !th.resolved; refresh(); const b = $('[data-resolve="' + th.id + '"]'); b && b.focus(); return; }
  if ((el = c('[data-send]'))) { const th = TH[el.dataset.send]; th.msgs.push({ a: 'D', n: 'Denis', d: '10 Nov, 15:40', x: th.draft, s: 'sent' }); th.draft = null; refresh(); toast('Sent as Denis. The agent never sends to readers on its own.'); return; }
  if ((el = c('[data-editdraft]'))) { const th = TH[el.dataset.editdraft]; const txt = th.draft; th.draft = null; refresh(); const ta = $('.th[data-thread="' + th.id + '"] textarea'); if (ta) { ta.value = txt; ta.focus(); } return; }
  if ((el = c('[data-discard]'))) { TH[el.dataset.discard].draft = null; refresh(); return; }
  if ((el = c('[data-acc]'))) { accept(el.dataset.acc); return; }
  if ((el = c('[data-rej]'))) { reject(el.dataset.rej); return; }
  if (c('[data-accall]')) { pending().forEach(b => { b.src = clean(b.prop.src); b.prop.state = 'accepted'; b.changed = true; }); renderDoc(); return; }
  if (c('[data-review]')) { const b = pending()[0]; if (b) { const d = $('[data-diff="' + b.id + '"]'); d.scrollIntoView({ block: 'center' }); $('[data-acc="' + b.id + '"]').focus(); } return; }
  if (c('[data-publish]')) { openPF(); return; }
  if (c('[data-pfclose]')) { $('#pf').close(); return; }
  if (c('[data-pfgo]')) { $('#pf').close(); toast('Prototype: nothing was sent.'); return; }
  if ((el = c('[data-hist]'))) { const h = $('#hist'); h.hidden = !h.hidden; el.setAttribute('aria-expanded', !h.hidden); const r = el.getBoundingClientRect(), m = $('#main').getBoundingClientRect(); h.style.left = Math.max(8, Math.min(r.left - m.left, m.width - 250)) + 'px'; h.style.top = (r.bottom - m.top + 4) + 'px'; if (!h.hidden) $('button', h).focus(); return; }
  if ((el = c('[data-rev]'))) { $('#hist').hidden = true; $('[data-hist]').setAttribute('aria-expanded', 'false'); $('[data-hist]').focus(); if (el.dataset.rev !== 'Rev 4') toast('Prototype: older revisions are not built.'); return; }
  if ((el = c('[data-view]'))) {
    const g = el.dataset.view === 'graph'; $$('[data-view]').forEach(b => b.setAttribute('aria-pressed', b === el));
    const sc = $('.scroll'); if (sc) sc.hidden = g; $('#gview').hidden = !g; if (g && redrawGraph) redrawGraph(); closePop(false); return;
  }
  if (!c('.pop') && popEl && !popEl.hidden && !c('[data-gut]')) closePop(false);
  if (!c('#hist') && $('#hist') && !$('#hist').hidden) $('#hist').hidden = true;
  const blkEl = c('[data-edit]');
  if (blkEl && !c('.cm') && !c('a') && !c('.gut') && !blkEl.classList.contains('editing')) enterEdit(blkEl);
});
document.addEventListener('submit', e => { e.preventDefault(); submit(e.target); });
document.addEventListener('keydown', e => {
  if (e.key === 'Enter' && (e.ctrlKey || e.metaKey) && e.target.matches('.comp textarea')) { e.preventDefault(); submit(e.target.closest('form')); return; }
  if (e.key === 'Escape') {
    if (popEl && !popEl.hidden) { closePop(true); return; }
    if (drawerEl && !drawerEl.hidden) { closeDrawer(true); return; }
    if (app.classList.contains('drawer-open')) { app.classList.remove('drawer-open'); const m = $('[data-menu]'); m && m.focus(); return; }
    const h = $('#hist'); if (h && !h.hidden) { h.hidden = true; $('[data-hist]').focus(); }
  }
  if (e.key === 'Enter' && e.target.matches('[data-edit]') && !e.target.classList.contains('editing')) { e.preventDefault(); enterEdit(e.target); }
  if ((e.key === 'Enter' || e.key === ' ') && e.target.matches('.cm')) { e.preventDefault(); e.target.click(); }
});
const q = $('#q');
if (q) q.addEventListener('input', () => { const v = q.value.trim().toLowerCase(); $$('#tree li[data-title]').forEach(li => li.hidden = !!v && !li.dataset.title.includes(v)); });
document.addEventListener('focusin', e => { if (popEl && !popEl.hidden && !popEl.contains(e.target) && !e.target.closest('[data-gut],.cm,.pin') && e.target !== document.body && !e.target.closest('#drawer')) { /* keep popover open until Esc or outside click, tab order passes through */ } });
if (state === 'preflight' && role === 'owner') openPF();
if (Q.get('drawer') === '1') $('[data-menu]').click();
if (Q.get('open') === 'comments') openDrawer({});
if (Q.get('open') === 'pop') { const m = $('.cm[data-th="t1"]'); if (m) { m.click(); } }
if (Q.get('open') === 'sheet') { const m = $('.pin[data-th="t2"]'); if (m) { m.scrollIntoView({ block: 'center' }); m.click(); } }
})();
