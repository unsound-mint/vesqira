/* Vesqira Reports, direction C: review inbox and changes first. Disposable prototype, no framework.
   All names, numbers and runs are fictional. */
(function () {
  'use strict';
  var Q = new URLSearchParams(location.search);
  var root = document.documentElement;
  var th = Q.get('theme');
  if (th === 'dark' || th === 'light') root.setAttribute('data-theme', th);

  /* ---------- icons ---------- */
  var P = {
    check: '<path d="M20 6 9 17l-5-5"/>',
    x: '<path d="M18 6 6 18M6 6l12 12"/>',
    down: '<path d="m6 9 6 6 6-6"/>',
    up: '<path d="m18 15-6-6-6 6"/>',
    right: '<path d="m9 18 6-6-6-6"/>',
    msg: '<path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z"/>',
    inbox: '<path d="M22 12h-6l-2 3h-4l-2-3H2"/><path d="M5.45 5.11 2 12v6a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-6l-3.45-6.89A2 2 0 0 0 16.76 4H7.24a2 2 0 0 0-1.79 1.11z"/>',
    file: '<path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><path d="M14 2v6h6M8 13h8M8 17h5"/>',
    plus: '<path d="M5 12h14M12 5v14"/>',
    history: '<path d="M3 12a9 9 0 1 0 3-6.7L3 8"/><path d="M3 3v5h5M12 7v5l4 2"/>',
    link: '<path d="M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71"/><path d="M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71"/>',
    send: '<path d="m22 2-7 20-4-9-9-4z"/><path d="M22 2 11 13"/>',
    pencil: '<path d="M17 3a2.85 2.85 0 0 1 4 4L7.5 20.5 2 22l1.5-5.5z"/>',
    theme: '<circle cx="12" cy="12" r="9"/><path d="M12 3v18"/><path d="M12 3a9 9 0 0 1 0 18z" fill="currentColor"/>',
    back: '<path d="m12 19-7-7 7-7M19 12H5"/>',
    alert: '<path d="m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3z"/><path d="M12 9v4M12 17h.01"/>',
    cloudoff: '<path d="m2 2 20 20"/><path d="M5.78 5.78A7 7 0 0 0 9 19h8.5a4.5 4.5 0 0 0 1.31-.2M21.53 16.5A4.5 4.5 0 0 0 17.5 10h-1.79A7 7 0 0 0 10 5.07"/>',
    cloud: '<path d="M17.5 19H9a7 7 0 1 1 6.71-9h1.79a4.5 4.5 0 1 1 0 9z"/>',
    bot: '<path d="M12 8V4H8"/><rect width="16" height="12" x="4" y="8" rx="2"/><path d="M2 14h2M20 14h2M15 13v2M9 13v2"/>',
    user: '<path d="M19 21v-2a4 4 0 0 0-4-4H9a4 4 0 0 0-4 4v2"/><circle cx="12" cy="7" r="4"/>',
    upload: '<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4M17 8l-5-5-5 5M12 3v12"/>',
    ban: '<circle cx="12" cy="12" r="10"/><path d="m4.9 4.9 14.2 14.2"/>',
    list: '<path d="M8 6h13M8 12h13M8 18h13M3 6h.01M3 12h.01M3 18h.01"/>',
    shield: '<path d="M20 13c0 5-3.5 7.5-7.66 8.95a1 1 0 0 1-.67-.01C7.5 20.5 4 18 4 13V6a1 1 0 0 1 1-1c2 0 4.5-1.2 6.24-2.72a1.17 1.17 0 0 1 1.52 0C14.51 3.81 17 5 19 5a1 1 0 0 1 1 1z"/><path d="m9 12 2 2 4-4"/>',
    eye: '<path d="M2 12s3-7 10-7 10 7 10 7-3 7-10 7-10-7-10-7z"/><circle cx="12" cy="12" r="3"/>',
    image: '<rect width="18" height="18" x="3" y="3" rx="2"/><circle cx="9" cy="9" r="2"/><path d="m21 15-3.09-3.09a2 2 0 0 0-2.82 0L6 21"/>',
    layers: '<path d="m12 2 10 5-10 5L2 7z"/><path d="m2 17 10 5 10-5M2 12l10 5 10-5"/>',
    cpu: '<rect width="16" height="16" x="4" y="4" rx="2"/><rect width="6" height="6" x="9" y="9"/><path d="M15 2v2M15 20v2M2 15h2M2 9h2M20 15h2M20 9h2M9 2v2M9 20v2"/>',
    lock: '<rect width="18" height="11" x="3" y="11" rx="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/>'
  };
  window.ic = function (n, c) { return '<svg class="ic ' + (c || '') + '" viewBox="0 0 24 24" aria-hidden="true">' + P[n] + '</svg>'; };
  var ic = window.ic;
  var $ = function (s, r) { return (r || document).querySelector(s); };
  var $$ = function (s, r) { return Array.prototype.slice.call((r || document).querySelectorAll(s)); };

  /* ---------- shared chrome ---------- */
  function themeBtn() {
    return '<button class="btn ic ghost" id="themeBtn" aria-label="Switch colour theme" title="Switch colour theme">' + ic('theme') + '</button>';
  }
  function bindTheme() {
    var b = $('#themeBtn'); if (!b) return;
    b.addEventListener('click', function () {
      var cur = root.getAttribute('data-theme') || (matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark');
      root.setAttribute('data-theme', cur === 'dark' ? 'light' : 'dark');
    });
  }
  function logo() {
    return '<svg class="ic md" viewBox="0 0 24 24" aria-hidden="true" style="color:var(--accent)"><circle cx="12" cy="12" r="9"/><path d="M12 3v18M3 12h18" stroke-opacity=".5"/><circle cx="12" cy="12" r="3" fill="currentColor"/></svg>';
  }
  var FLAG = '<span class="flag" title="All names, figures and numbers are fictional">ILLUSTRATIVE MOCK DATA</span>';

  /* ---------- report fixture ---------- */
  var RUN1 = '2026-11-08_a1f3c', RUN2 = '2026-11-08_9be20';
  var WL_NOISE = 'Noise masking threshold sweep', WL_WEEK = 'Weekly notes 2026-W45';
  function wl(name, mode) {
    if (mode === 'owner') {
      if (name === WL_NOISE) return '<a class="wl" href="#">' + name + '</a> <span class="pill warn" style="vertical-align:1px">draft, not shared</span>';
      return '<a class="wl" href="#">' + name + '</a>';
    }
    if (name === WL_NOISE) return '<span class="wl-un">' + name + '<small>not shared with you</small></span>';
    return '<a class="wl" href="#" title="Other reports are not part of this mock">' + name + '</a>';
  }
  var T = {
    setupOld: 'Both runs use the same input, <code>lmc_hi_cube.fits</code>, integrated over −8…+8 km/s.',
    setupNew: 'Both runs use the same input, <code>lmc_hi_cube.fits</code>, integrated over −12…+12 km/s (the range was widened after review). Only the block size differs between the runs.',
    resOld: 'The two runs agree over most of the disk. The alignment measure is 0.05 lower with the larger block (Table 1).',
    resNew: 'Orientation maps are drawn as axial segments, so a segment at 0° and one at 180° look identical. The two runs agree over most of the disk. The alignment measure is 0.05 lower with the larger block (Table 1). The largest disagreement is in the south-east, where the 64-pixel map is rotated against the 32-pixel map (median axial difference 6° over the unmasked region).',
    intOld: 'Block 32 gives higher alignment than block 64, which suggests that smaller blocks recover the field better.',
    intNew: 'Block 32 gives a higher alignment (0.41 vs 0.36), but the difference of 0.05 is about 1σ of the combined reported uncertainty (≈0.05), so the two runs are not distinguishable on this evidence. The south-east rotation may be an edge effect at the masked border; this has not been tested.'
  };
  function diffInline(a, b) { return '<del>' + a + '</del> <ins>' + b + '</ins>'; }
  function nextList(mode, v) {
    var a = '<li>Repeat both runs with the reliability map masked out near the border.</li>';
    var b = '<li>Check consistency with ' + wl(WL_NOISE, mode) + '.</li><li>Summarise the outcome in ' + wl(WL_WEEK, mode) + '.</li>';
    return '<ul>' + (v === 'old' ? '' : v === 'trk' ? a.replace('<li>', '<li><ins>').replace('</li>', '</ins></li>') : a) + b + '</ul>';
  }
  function plate(n, img, run, block, mode, pin) {
    var thumb = '<img src="../_shared/assets/' + img + '" alt="Axial orientation map of the LMC H I cube from run ' + run + ', block ' + block + ' pixels. Short line segments show axial orientation; colour encodes angle modulo 180 degrees." width="512" height="512">';
    var pinHtml = pin && mode === 'reader' ? '<button class="pin" style="left:' + pin.x + '%;top:' + pin.y + '%" data-thread="' + pin.t + '" aria-label="Comment pinned on Figure ' + n + ' at ' + pin.x + ' percent, ' + pin.y + ' percent: open thread">1</button>' : '';
    return '<figure class="plate">' +
      '<div class="pic">' + thumb + '<span class="mockb">ILLUSTRATIVE</span>' + pinHtml + '</div>' +
      '<figcaption class="cap"><b class="ft">Figure ' + n + '. VGT axial orientation, block ' + block + ' px</b>' +
      '<div style="color:var(--text-2)">Segments are axial: 0° and 180° are the same orientation. Colour is angle modulo 180°.</div>' +
      '<img class="leg" src="../_shared/assets/axial_legend.png" alt="Colour legend for axial angle, 0 to 180 degrees, cyclic" width="361" height="24">' +
      '<dl><dt>Dataset</dt><dd>lmc_hi_cube.fits</dd><dt>Channels</dt><dd>−12…+12 km/s</dd><dt>Run</dt><dd>' + run + '</dd><dt>Block</dt><dd>' + block + ' px</dd><dt>Header</dt><dd>as declared, not verified</dd></dl>' +
      '<div class="frozen">' + ic('image') + '<span>Frozen preview: a picture, not data. Pinned ' + (n === 1 ? '8' : '8') + ' Nov.</span></div></figcaption></figure>';
  }
  function chgWrap(id, label, kind, inner, prevHtml, cls) {
    return '<div class="blk ' + (cls || '') + (kind ? ' chg' : '') + '" id="' + id + '" data-label="' + label + '" data-ord="' + (ORD.indexOf(id)) + '">' +
      (kind ? '<span class="chglbl">' + ic('pencil') + 'changed in rev 5</span>' : '') +
      (kind ? '<div class="prev"><span class="lbl">Revision 4</span>' + prevHtml + '</div>' : '') +
      inner + '</div>';
  }
  var ORD = ['b1', 'b2', 'runs', 'f1', 'f2', 'b5', 'tbl', 'eq', 'b6', 'b7'];
  var LABEL = { b2: 'Setup', f2: 'Figure 2', b5: 'Results ¶ 1', b6: 'Interpretation', b7: 'Next steps' };

  function tableEq() {
    return '<div class="blk wide" id="tbl" data-ord="6"><div class="tblwrap"><table class="rt"><caption>Table 1. Alignment measure (AM) by block size. Values and ± as reported by the runs; uncertainty method not verified.</caption>' +
      '<thead><tr><th>Run</th><th>Block (px)</th><th>AM (dimensionless)</th><th>Valid blocks</th></tr></thead><tbody>' +
      '<tr><td><span class="run">' + RUN1 + '</span></td><td>32</td><td>0.41 ± 0.03</td><td>912</td></tr>' +
      '<tr><td><span class="run">' + RUN2 + '</span></td><td>64</td><td>0.36 ± 0.04</td><td>228</td></tr></tbody>' +
      '<tfoot><tr><td class="df" colspan="4">AM is the mean of cos 2θ<sub>r</sub> over valid blocks, where θ<sub>r</sub> is the relative angle between the VGT orientation and the reference orientation declared by the run. Axial: 0° ≡ 180°. Valid blocks exclude masked and low-reliability blocks.</td></tr></tfoot></table></div></div>' +
      '<div class="blk wide" id="eq" data-ord="7"><div class="eq"><math display="block" aria-label="AM equals the mean of cosine of two theta r"><mrow><mi>AM</mi><mo>=</mo><mo>⟨</mo><mi>cos</mi><mo> </mo><mn>2</mn><msub><mi>θ</mi><mi>r</mi></msub><mo>⟩</mo></mrow></math><span class="en">(1)</span></div></div>';
  }
  function runs() {
    return '<div class="blk wide" id="runs" data-ord="2"><div class="runline"><span class="run">' + RUN1 + '</span><span class="rm">block 32 px · input lmc_hi_cube.fits · −12…+12 km/s</span><span class="pill">run output imported</span></div>' +
      '<div class="runline"><span class="run">' + RUN2 + '</span><span class="rm">block 64 px · input lmc_hi_cube.fits · −12…+12 km/s</span><span class="pill">run output imported</span></div></div>';
  }
  var INTERP_TAG = '<span class="pill warn interp-tag">Interpretation · not validated</span>';

  /* ---------- threads (reader) ---------- */
  var ME = 'Prof. Lindqvist';
  var THREADS = [
    { id: 't1', block: 'b2', anchor: 'Setup · channel range', status: 'resolved', rev: 3, msgs: [
      { who: ME, you: 1, t: '5 Nov 11:20', x: 'Why only −8…+8 km/s? The LMC disk emission extends beyond that.' },
      { who: 'Denis', t: '9 Nov 17:22', x: 'Agreed. Widened to −12…+12 km/s in revision 5. The wing beyond that is within the noise.' }], note: 'Resolved by Denis, 9 Nov' },
    { id: 't2', block: 'f2', anchor: 'Figure 2 · pin 1, south-east', status: 'open', rev: 5, msgs: [
      { who: ME, you: 1, t: '8 Nov 09:05', x: 'South-east corner: is this rotation real, or an edge effect?' },
      { who: 'Denis', att: 'drafted by VGT agent, edited by Denis', t: '9 Nov 17:25', x: 'Most likely an edge effect: the 64-pixel blocks touch the masked border there (see reliability map). Not yet quantified.' },
      { who: ME, you: 1, t: '10 Nov 08:10', x: 'Can you quantify it before the next meeting?' }], note: 'Delivered to the author’s workspace' },
    { id: 't3', block: 'b5', anchor: 'Results ¶ 1', status: 'stale', rev: 3, msgs: [
      { who: ME, you: 1, t: '4 Nov 16:40', x: 'Is a 0.05 difference in AM meaningful given the sample count?' }], note: 'Seen by the author 5 Nov · no reply yet' },
    { id: 't4', block: 'b6', anchor: 'Interpretation', status: 'unseen', rev: 5, msgs: [
      { who: ME, you: 1, t: 'Today 10:55', x: 'Which reliability cut was used for both runs? If none, please say so.' }], note: 'Not yet seen by the author' }
  ];

  /* ====================================================== READER REPORT ====================================================== */
  function readerReport() {
    var tcount = {}; THREADS.forEach(function (t) { tcount[t.block] = (tcount[t.block] || 0) + 1; });
    function bar(id) {
      var n = tcount[id];
      return '<div class="blkbar">' + (n ? '<button class="mkb has" data-thread-of="' + id + '" aria-label="' + n + ' comment thread' + (n > 1 ? 's' : '') + ' on this block: open">' + ic('msg') + n + '</button>' : '') +
        '<button class="mkb add" data-add="' + id + '" aria-label="Comment on ' + (LABEL[id] || id) + '">' + ic('plus') + '</button></div>';
    }
    var h = '';
    h += '<h1>Block size 32 vs 64: gradient orientation on the LMC H I cube</h1>';
    h += '<div class="byline"><span>Revision 5 · published 9 Nov 2026 17:20</span><span class="agent-tag">' + ic('bot') + 'drafted by VGT agent, edited by Denis</span></div>';
    h += '<h2><span class="sn">1</span>Question</h2>';
    h += '<div class="blk" id="b1" data-ord="0">' + bar('b1') + '<p>Does doubling the gradient block size from 32 to 64 pixels change the recovered plane-of-sky field orientation in the LMC H I cube, and where in the map does it matter?</p></div>';
    h += '<h2><span class="sn">2</span>Setup</h2>';
    h += chgWrap('b2', 'Setup', 1, bar('b2') + '<p>' + T.setupNew + '</p>', '<p>' + T.setupOld + '</p>');
    h += runs();
    h += '<h2><span class="sn">3</span>Results</h2>';
    h += '<div class="blk wide" id="f1" data-ord="3">' + plate(1, 'vgt_baseline_axial.png', RUN1, 32, 'reader') + '</div>';
    h += '<div class="blk wide" id="f2" data-ord="4">' + plate(2, 'vgt_candidate_axial.png', RUN2, 64, 'reader', { x: 62, y: 78, t: 't2' }) + bar('f2').replace('right:-52px', '') + '</div>';
    h += chgWrap('b5', 'Results ¶ 1', 1, bar('b5') + '<p>' + T.resNew + '</p>', '<p>' + T.resOld + '</p>');
    h += tableEq();
    h += '<h2><span class="sn">4</span>Interpretation</h2>';
    h += chgWrap('b6', 'Interpretation', 1, bar('b6') + '<div class="interp">' + INTERP_TAG + '<p>' + T.intNew + '</p></div>', '<p>' + T.intOld + '</p>');
    h += '<h2><span class="sn">5</span>Next steps</h2>';
    h += chgWrap('b7', 'Next steps', 1, bar('b7') + nextList('reader', 'new'), nextList('reader', 'old').replace('<ul>', '<ul>'));
    h += '<div class="backl"><div class="lbl">Linked from</div><ul><li>' + ic('link') + ' ' + wl(WL_WEEK, 'reader') + '</li><li class="t2">' + ic('lock') + ' 1 more report that is not shared with you</li></ul></div>';
    return h;
  }
  var CHG = ['b2', 'b5', 'b6', 'b7'];

  function msgHtml(m) {
    return '<div class="msg"><div class="mh"><b>' + m.who + '</b>' + (m.you ? '<span class="pill">you</span>' : '<span class="pill acc">author</span>') + '<span class="t3 mt">' + m.t + '</span></div>' +
      (m.att ? '<div class="agent-tag">' + ic('bot') + m.att + '</div>' : '') + '<p>' + m.x + '</p></div>';
  }
  function statusPill(t) {
    if (t.status === 'resolved') return '<span class="pill ok">' + ic('check') + 'Resolved</span>';
    if (t.status === 'stale') return '<span class="pill warn">' + ic('history') + 'on revision 3 · this paragraph changed since</span>';
    if (t.status === 'unseen') return '<span class="pill un">' + ic('cloudoff') + 'Not yet seen by the author</span>';
    return '<span class="pill acc">Open</span>';
  }
  function threadHtml(t) {
    var res = t.status === 'resolved';
    return '<article class="thr' + (res ? ' collapsed' : '') + '" id="' + t.id + '" data-block="' + t.block + '" aria-label="Thread: ' + t.anchor + '">' +
      '<header class="thh"><button class="jump" data-jump="' + t.block + '" title="Jump to this place in the report">' + ic('right') + '<span>' + t.anchor + '</span></button>' +
      (res ? '<button class="btn sm ghost tg" aria-expanded="false" aria-controls="' + t.id + '-b">Show</button>' : '') + '</header>' +
      '<div class="ths">' + statusPill(t) + '</div>' +
      '<div class="thb" id="' + t.id + '-b">' + t.msgs.map(msgHtml).join('') +
      '<div class="thn t3">' + t.note + '</div>' +
      '<div class="rep"><button class="btn sm rb" aria-expanded="false">Reply</button></div></div>' +
      (res ? '<div class="thsum t2">' + t.msgs[t.msgs.length - 1].who + ': “' + t.msgs[t.msgs.length - 1].x.slice(0, 56) + '…”</div>' : '') +
      '</article>';
  }
  var SYNC = '3 h ago';

  function pageReader() {
    document.body.innerHTML =
      '<a class="skip btn" href="#main">Skip to report</a>' +
      '<header class="top">' + logo() + '<span class="brand">Vesqira</span><span class="crumb"><a href="02-reader-inbox.html' + location.search.replace(/[?&]?focus=[^&]*/, '') + '">Review space</a> / <b>Block size 32 vs 64</b></span><span class="sp"></span>' + FLAG + themeBtn() + '</header>' +
      '<div class="rtool" role="toolbar" aria-label="Report view options">' +
      '<a class="btn sm ghost" href="02-reader-inbox.html">' + ic('back') + '<span class="hm">Inbox</span></a>' +
      '<div class="revwrap"><button class="btn sm" id="revBtn" aria-expanded="false" aria-controls="revPop">' + ic('history') + 'Revision 5 ' + ic('down') + '</button>' +
      '<div class="pop" id="revPop" hidden><div class="lbl" style="padding:var(--sp-2) var(--sp-3)">Revision history</div><ol class="revs">' +
      '<li aria-current="true"><b>Rev 5</b><span>9 Nov 17:20 · 4 blocks changed</span></li><li><b>Rev 4</b><span>7 Nov 14:02 · your last visit · 1 block</span></li><li><b>Rev 3</b><span>3 Nov 09:15 · 2 blocks</span></li><li><b>Rev 2</b><span>1 Nov 18:40</span></li><li><b>Rev 1</b><span>31 Oct 10:05</span></li></ol><div class="t3" style="padding:var(--sp-2) var(--sp-3);font-size:var(--text-xs);line-height:var(--lh-xs)">Older revisions are read-only. This mock opens revision 5 only.</div></div></div>' +
      '<span class="sp"></span>' +
      '<button class="btn sm" id="tChg" aria-pressed="true">' + ic('pencil') + '<span>Changes since rev 4</span></button>' +
      '<button class="btn sm" id="tPrev" aria-pressed="false">' + ic('layers') + '<span>Previous text</span></button>' +
      '<span class="nav"><button class="btn sm ic" id="chgPrev" aria-label="Previous change">' + ic('up') + '</button><span class="t2 cn" id="chgCount" aria-live="polite">—</span><button class="btn sm ic" id="chgNext" aria-label="Next change">' + ic('down') + '</button></span>' +
      '</div>' +
      '<div class="statusline"><span>' + ic('cloud') + '</span><span>Denis’s workspace last synced ' + SYNC + '. Comments you write are saved on the publish server and reach the author when it is online. Replies shown here are the ones already published.</span></div>' +
      '<div class="rd"><main class="rd-main" id="main" tabindex="-1"><article class="doc">' + readerReport() + '</article></main>' +
      '<aside class="disc" id="disc" aria-label="Discussion" role="region"><header class="dh"><h2>Discussion</h2><span class="pill">' + THREADS.length + ' threads</span><span class="sp"></span><button class="btn sm ic ghost" id="dClose" aria-label="Close discussion">' + ic('x') + '</button></header>' +
      '<div class="dsub t2">In document order. <span class="t3">Select a thread to jump to its place.</span></div>' +
      '<div class="dlist" id="dlist">' + THREADS.map(threadHtml).join('') + '</div>' +
      '<form class="comp" id="comp" aria-label="New comment"><div class="lbl">New comment</div><div class="on t2" id="compOn">On: choose a block with the <span class="kbd">+</span> button</div>' +
      '<label class="sr" for="compTx">Comment text</label><textarea id="compTx" rows="3" placeholder="Write a comment. Ctrl+Enter sends."></textarea>' +
      '<div class="crow"><span class="t3 cs" id="compSt">Saved on the publish server. The author sees it at the next sync.</span><button type="submit" class="btn pri sm" id="compSend" disabled>' + ic('send') + 'Send</button></div></form></aside></div>' +
      '<button class="dbtn btn pri" id="dOpen" aria-controls="disc" aria-expanded="false">' + ic('msg') + 'Discussion <span class="pill" id="dCnt" style="background:var(--accent-ink);color:var(--accent)">' + THREADS.length + '</span></button>';
    bindTheme();
    var body = document.body, disc = $('#disc'), target = null;
    var live = $('#chgCount');
    var changed = $$('.blk.chg'), ci = -1;
    function mark() { live.textContent = (ci < 0 ? '—' : (ci + 1)) + ' of ' + changed.length; }
    mark();
    function flash(el) { el.classList.remove('flash'); void el.offsetWidth; el.classList.add('flash'); }
    function goBlock(id) { var el = document.getElementById(id); if (!el) return; el.scrollIntoView({ block: 'start' }); flash(el); }
    function stepChg(d) {
      if (body.classList.contains('nochg')) { $('#tChg').click(); }
      ci = (ci + d + changed.length) % changed.length; mark(); goBlock(changed[ci].id);
    }
    $('#chgNext').onclick = function () { stepChg(ci < 0 ? 0 : 1); };
    $('#chgPrev').onclick = function () { stepChg(ci < 0 ? changed.length - 1 : -1); };
    $('#tChg').onclick = function () {
      var on = this.getAttribute('aria-pressed') !== 'true'; this.setAttribute('aria-pressed', on);
      body.classList.toggle('nochg', !on);
      if (!on) { $('#tPrev').setAttribute('aria-pressed', 'false'); body.classList.remove('showprev'); }
    };
    $('#tPrev').onclick = function () {
      var on = this.getAttribute('aria-pressed') !== 'true'; this.setAttribute('aria-pressed', on);
      body.classList.toggle('showprev', on);
      if (on) { $('#tChg').setAttribute('aria-pressed', 'true'); body.classList.remove('nochg'); }
    };
    var rb = $('#revBtn'), pop = $('#revPop');
    rb.onclick = function () { var o = pop.hidden; pop.hidden = !o; rb.setAttribute('aria-expanded', o); };
    document.addEventListener('keydown', function (e) {
      if (e.key === 'Escape') {
        if (!pop.hidden) { pop.hidden = true; rb.setAttribute('aria-expanded', 'false'); rb.focus(); }
        else if (disc.classList.contains('open')) closeSheet();
      }
    });
    function openSheet() { disc.classList.add('open'); $('#dOpen').setAttribute('aria-expanded', 'true'); }
    function closeSheet() { disc.classList.remove('open'); $('#dOpen').setAttribute('aria-expanded', 'false'); $('#dOpen').focus(); }
    $('#dOpen').onclick = function () { openSheet(); $('#dClose').focus(); };
    $('#dClose').onclick = closeSheet;
    function openThread(id) {
      var t = document.getElementById(id); if (!t) return;
      if (matchMedia('(max-width:1099px)').matches) openSheet();
      if (t.classList.contains('collapsed')) toggleThread(t);
      $$('.thr.cur').forEach(function (x) { x.classList.remove('cur'); }); t.classList.add('cur');
      t.scrollIntoView({ block: 'nearest' });
      var j = $('.jump', t); if (j) j.focus({ preventScroll: true });
    }
    function toggleThread(t) {
      var c = t.classList.toggle('collapsed'), b = $('.tg', t);
      if (b) { b.setAttribute('aria-expanded', !c); b.textContent = c ? 'Show' : 'Hide'; }
    }
    function openComposer(blockId) {
      target = blockId;
      $('#compOn').innerHTML = 'On: <b>' + (LABEL[blockId] || (document.getElementById(blockId) || {}).dataset.label || blockId) + '</b>';
      $('#compSend').disabled = !$('#compTx').value.trim();
      if (matchMedia('(max-width:1099px)').matches) openSheet();
      $('#compTx').focus();
    }
    document.addEventListener('click', function (e) {
      var el = e.target.closest('button'); if (!el) return;
      if (el.dataset.thread) { openThread(el.dataset.thread); }
      else if (el.dataset.threadOf) { var t = THREADS.filter(function (x) { return x.block === el.dataset.threadOf; })[0]; if (t) openThread(t.id); }
      else if (el.dataset.add) { openComposer(el.dataset.add); }
      else if (el.dataset.jump) { var wasM = matchMedia('(max-width:1099px)').matches; if (wasM) { disc.classList.remove('open'); $('#dOpen').setAttribute('aria-expanded', 'false'); } goBlock(el.dataset.jump); $$('.thr.cur').forEach(function (x) { x.classList.remove('cur'); }); el.closest('.thr').classList.add('cur'); }
      else if (el.classList.contains('tg')) { toggleThread(el.closest('.thr')); }
      else if (el.classList.contains('rb')) {
        var thr = el.closest('.thr'), row = $('.rep', thr);
        if ($('form', row)) return;
        el.setAttribute('aria-expanded', 'true');
        var f = document.createElement('form'); f.className = 'rf';
        f.innerHTML = '<label class="sr" for="rt-' + thr.id + '">Reply</label><textarea id="rt-' + thr.id + '" rows="2" placeholder="Reply. Ctrl+Enter sends."></textarea><div class="crow"><button type="button" class="btn sm ghost rc">Cancel</button><button type="submit" class="btn sm pri">' + ic('send') + 'Send</button></div>';
        row.appendChild(f); $('textarea', f).focus();
        f.onsubmit = function (ev) { ev.preventDefault(); var v = $('textarea', f).value.trim(); if (!v) return; $('.thn', thr).insertAdjacentHTML('beforebegin', msgHtml({ who: ME, you: 1, t: 'Just now', x: v.replace(/</g, '&lt;') }) + '<div class="thn t3" style="color:var(--warn)">Saved here, not yet seen by the author</div>'); $$('.thn', thr)[1].remove(); f.remove(); el.setAttribute('aria-expanded', 'false'); el.focus(); };
        $('.rc', f).onclick = function () { f.remove(); el.setAttribute('aria-expanded', 'false'); el.focus(); };
        $('textarea', f).onkeydown = function (ev) { if (ev.key === 'Enter' && (ev.ctrlKey || ev.metaKey)) { ev.preventDefault(); f.requestSubmit(); } };
      }
    });
    var tx = $('#compTx');
    tx.oninput = function () { $('#compSend').disabled = !tx.value.trim() || !target; };
    tx.onkeydown = function (e) { if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) { e.preventDefault(); $('#comp').requestSubmit(); } };
    $('#comp').onsubmit = function (e) {
      e.preventDefault();
      var v = tx.value.trim(); if (!v || !target) return;
      var t = { id: 'tn' + Date.now(), block: target, anchor: LABEL[target] || document.getElementById(target).dataset.label || target, status: 'unseen', msgs: [{ who: ME, you: 1, t: 'Just now', x: v.replace(/</g, '&lt;') }], note: 'Saved here, not yet seen by the author' };
      var ord = function (b) { return +document.getElementById(b).dataset.ord; };
      var list = $$('#dlist .thr'), before = list.filter(function (x) { return ord(x.dataset.block) > ord(target); })[0];
      var tmp = document.createElement('div'); tmp.innerHTML = threadHtml(t); var node = tmp.firstChild;
      $('#dlist').insertBefore(node, before || null);
      tx.value = ''; $('#compSend').disabled = true;
      var n = $$('#dlist .thr').length; $('.dh .pill').textContent = n + ' threads'; $('#dCnt').textContent = n;
      $('#compSt').textContent = 'Sent. Saved on the publish server; the author sees it at the next sync (last sync ' + SYNC + ').';
      node.scrollIntoView({ block: 'nearest' }); $$('.thr.cur').forEach(function (x) { x.classList.remove('cur'); }); node.classList.add('cur');
    };
    // deep links
    var f = Q.get('focus');
    if (Q.get('sheet') === '1') openSheet();
    if (f) setTimeout(function () { if (/^t\d/.test(f)) { openThread(f); var tb = document.getElementById(f); if (tb) goBlock(tb.dataset.block); } else { goBlock(f); ci = Math.max(0, changed.map(function (c) { return c.id; }).indexOf(f)); mark(); } }, 60);
  }

  /* ====================================================== READER INBOX ====================================================== */
  var ITEMS = [
    { k: 'upd', g: 'new', title: 'Block size 32 vs 64: gradient orientation on the LMC H I cube', meta: 'Updated · revision 4 → 5 · 9 Nov 17:20', body: '4 blocks changed: Setup (channel range widened), Results ¶ 1, Interpretation, Next steps (1 item added).', cta: 'Open at first change', href: '03-reader-report.html?focus=b2' },
    { k: 'rep', g: 'new', title: 'Denis replied on Figure 2, pin 1', meta: 'Reply · Block size 32 vs 64 · 9 Nov 17:25', body: '“Most likely an edge effect: the 64-pixel blocks touch the masked border there…” Drafted by the VGT agent, edited by Denis.', cta: 'Open thread', href: '03-reader-report.html?focus=t2' },
    { k: 'rep', g: 'new', title: 'Denis resolved your question on the channel range', meta: 'Reply · Block size 32 vs 64 · 9 Nov 17:22', body: '“Agreed. Widened to −12…+12 km/s in revision 5.”', cta: 'Open thread', href: '03-reader-report.html?focus=t1' },
    { k: 'rep', g: 'new', title: 'Denis replied on Weekly notes 2026-W45', meta: 'Reply · Weekly notes 2026-W45 · 9 Nov 09:10', body: '“Noted. I will bring the block-size comparison to Thursday.”', cta: 'Open thread', href: '#' },
    { k: 'upd', g: 'new', title: 'Weekly notes 2026-W45', meta: 'Updated · revision 1 → 2 · 9 Nov 09:10', body: '1 block changed: Thursday agenda.', cta: 'Open at first change', href: '#' },
    { k: 'new', g: 'new', title: 'Spectral smoothing check', meta: 'New report · revision 1 · shared with you 8 Nov', body: 'Does Hanning smoothing along velocity change the recovered orientation? One figure, no open questions yet.', cta: 'Open report', href: '#' },
    { k: 'upd', g: 'old', title: 'Block size 32 vs 64: gradient orientation on the LMC H I cube', meta: 'Updated · revision 3 → 4 · 7 Nov 14:02', body: '1 block changed: Results ¶ 1.', cta: 'Read as it was in rev 4', href: '#' },
    { k: 'new', g: 'old', title: 'Weekly notes 2026-W45', meta: 'New report · revision 1 · shared with you 3 Nov', body: 'First notes of the week.', cta: 'Open report', href: '#' }
  ];
  var KIND = { upd: ['Updated', 'file'], rep: ['Reply', 'msg'], new: ['New', 'plus'] };
  function pageInbox() {
    var st = Q.get('state') || 'default';
    var topbar = '<header class="top">' + logo() + '<span class="brand">Vesqira</span><span class="crumb">Review space</span><span class="sp"></span>' + FLAG + themeBtn() + '</header>';
    if (st === 'revoked') {
      document.body.innerHTML = topbar + '<main class="neutral" id="main"><div><h1>This link does not open anything.</h1><p class="t2">Check that you copied the whole link. If it still fails, ask the person who sent it.</p></div></main>';
      bindTheme(); return;
    }
    var off = st === 'offline';
    var sync = off
      ? '<div class="statusline warn" role="status">' + ic('alert') + '<span><b>Denis’s workspace has not synced for 2 days.</b> You can still comment: comments are stored on the publish server and reach the author when the workspace is back online. Replies that were not published yet are not shown here.</span></div>'
      : '<div class="statusline" role="status">' + ic('cloud') + '<span>Denis’s workspace last synced ' + SYNC + '. New comments reach the author at the next sync.</span></div>';
    var main;
    if (st === 'empty') {
      main = '<div class="empty"><div class="eicon">' + ic('inbox', 'md') + '</div><h1>Nothing has been shared with you yet</h1><p class="t2">When Denis shares a report with this link, it appears here, together with its replies and updates. There is nothing to do on your side.</p><p class="t3">This page keeps no account and no history of its own. Reports stay available as long as the author keeps them shared.</p></div>';
    } else {
      var items = ITEMS;
      var grp = function (g) {
        return items.filter(function (i) { return i.g === g; }).map(function (i) {
          var kd = KIND[i.k];
          return '<li class="it' + (i.g === 'new' ? ' unread' : '') + '" data-k="' + i.k + '"><span class="dot" aria-hidden="true"></span><span class="ik">' + ic(kd[1]) + '</span><div class="ib"><div class="it-h"><span class="pill ' + (i.k === 'new' ? 'ok' : i.k === 'rep' ? 'acc' : '') + '">' + kd[0] + '</span><span class="t3 im">' + i.meta.replace(/^(Updated|Reply|New report) \u00b7 /, '') + '</span></div>' +
            '<h3>' + i.title + '</h3><p class="t2">' + i.body + '</p>' +
            '<a class="btn sm" href="' + i.href + '"' + (i.href === '#' ? ' title="Not part of this mock"' : '') + '>' + i.cta + ' ' + ic('right') + '</a></div></li>';
        }).join('');
      };
      main = '<div class="ibhead"><div><h1>Since your last visit</h1><p class="t2" id="sum"><b>2 reports updated</b> · <b>3 replies to your comments</b> · <b>1 new report</b><span class="t3"> · last visit 6 Nov</span></p></div>' +
        '<div class="ibtools"><div class="seg" role="group" aria-label="Filter activity"><button class="btn sm" aria-pressed="true" data-f="all">All 6</button><button class="btn sm" aria-pressed="false" data-f="upd">Updates 2</button><button class="btn sm" aria-pressed="false" data-f="rep">Replies 3</button><button class="btn sm" aria-pressed="false" data-f="new">New 1</button></div>' +
        '<button class="btn sm ghost" id="seen">' + ic('check') + 'Mark all as seen</button></div></div>' +
        '<ul class="feed" id="feed">' + grp('new') + '</ul><div class="lbl" style="margin:var(--sp-6) 0 var(--sp-2)">Earlier</div><ul class="feed old">' + grp('old') + '</ul>';
    }
    var side = st === 'empty' ? '' :
      '<aside class="inside" aria-label="Reports in this space"><div class="lbl">Reports in this space · 3</div><table class="rt2"><thead><tr><th scope="col">Report</th><th scope="col">Rev</th><th scope="col">Open threads</th></tr></thead><tbody>' +
      '<tr><td><a href="03-reader-report.html">Block size 32 vs 64</a></td><td class="mono">5</td><td class="mono">3</td></tr>' +
      '<tr><td><a href="#">Weekly notes 2026-W45</a></td><td class="mono">2</td><td class="mono">0</td></tr>' +
      '<tr><td><a href="#">Spectral smoothing check</a></td><td class="mono">1</td><td class="mono">0</td></tr></tbody></table>' +
      '<div class="note">' + ic('lock') + '<p class="t2">This is a personal link, named for you. The author can revoke it at any time. It opens only what has been shared with you. Figures are pictures, not data.</p></div></aside>';
    document.body.innerHTML = '<a class="skip btn" href="#main">Skip to inbox</a>' + topbar + sync + '<div class="ib-wrap"><main id="main" tabindex="-1">' + main + '</main>' + side + '</div>';
    bindTheme();
    $$('.seg button').forEach(function (b) {
      b.onclick = function () {
        $$('.seg button').forEach(function (x) { x.setAttribute('aria-pressed', x === b); });
        $$('#feed .it').forEach(function (li) { li.hidden = b.dataset.f !== 'all' && li.dataset.k !== b.dataset.f; });
      };
    });
    var s = $('#seen'); if (s) s.onclick = function () { $$('#feed .it').forEach(function (l) { l.classList.remove('unread'); }); s.disabled = true; s.lastChild.textContent = ' All seen'; };
  }

  /* ====================================================== OWNER DESK ====================================================== */
  var SUG = [
    { id: 's1', block: 'b2', label: 'Setup', sum: 'Channel range −8…+8 → −12…+12 km/s; adds “only the block size differs”.', st: 'pending', old: '<p>' + T.setupOld + '</p>', nw: '<p>' + T.setupNew + '</p>', trk: '<p>' + diffInline(T.setupOld, T.setupNew) + '</p>' },
    { id: 's2', block: 'b5', label: 'Results ¶ 1', sum: 'Adds axial note and south-east disagreement (median 6°, axial).', st: 'pending', old: '<p>' + T.resOld + '</p>', nw: '<p>' + T.resNew + '</p>', trk: '<p><ins>Orientation maps are drawn as axial segments, so a segment at 0° and one at 180° look identical.</ins> The two runs agree over most of the disk. The alignment measure is 0.05 lower with the larger block (Table 1).<ins> The largest disagreement is in the south-east, where the 64-pixel map is rotated against the 32-pixel map (median axial difference 6° over the unmasked region).</ins></p>' },
    { id: 's3', block: 'b6', label: 'Interpretation', sum: 'Rewrites the claim: 0.05 is about 1σ, not distinguishable.', st: 'pending', old: '<div class="interp">' + INTERP_TAG + '<p>' + T.intOld + '</p></div>', nw: '<div class="interp">' + INTERP_TAG + '<p>' + T.intNew + '</p></div>', trk: '<div class="interp">' + INTERP_TAG + '<p>' + diffInline(T.intOld, T.intNew) + '</p></div>' },
    { id: 's4', block: 'b7', label: 'Next steps', sum: 'Adds item: repeat both runs with reliability map masked near the border.', st: 'pending', old: nextList('owner', 'old'), nw: nextList('owner', 'new'), trk: nextList('owner', 'trk') }
  ];
  var DRAFT = { id: 'd1', thread: 't3', anchor: 'Results ¶ 1 · on revision 3, paragraph changed since', q: 'Is a 0.05 difference in AM meaningful given the sample count?', who: 'Prof. Lindqvist, 4 Nov 16:40', text: 'AM differs by 0.05 against a combined σ of about 0.05, so about 1σ. With 912 vs 228 valid blocks the neighbouring blocks are not independent, so the quoted ± may be too small. I have not tested this.', state: 'draft' };

  function sugBlock(s) {
    var wide = '';
    return '<div class="blk sug" id="' + s.block + '" data-sug="' + s.id + '" data-state="pending">' +
      '<div class="sugh"><span class="pill warn">' + ic('bot') + 'Agent suggestion</span><span class="t2 sst" aria-live="polite">Pending</span><span class="sp"></span>' +
      '<button class="btn sm" data-acc="' + s.id + '">' + ic('check') + 'Accept</button><button class="btn sm" data-rej="' + s.id + '">' + ic('x') + 'Reject</button></div>' +
      '<div class="v v-trk">' + s.trk + '</div><div class="v v-new">' + s.nw + '</div><div class="v v-old">' + s.old + '</div></div>';
  }
  function ownerReport() {
    var S = {}; SUG.forEach(function (s) { S[s.block] = s; });
    var h = '<h1>Block size 32 vs 64: gradient orientation on the LMC H I cube</h1>' +
      '<div class="byline"><span>Draft on revision 4 (published 7 Nov 14:02)</span><span class="agent-tag">' + ic('bot') + 'drafted by VGT agent, edited by Denis</span></div>' +
      '<h2><span class="sn">1</span>Question</h2><div class="blk" id="b1"><p>Does doubling the gradient block size from 32 to 64 pixels change the recovered plane-of-sky field orientation in the LMC H I cube, and where in the map does it matter?</p></div>' +
      '<h2><span class="sn">2</span>Setup</h2>' + sugBlock(S.b2) + runs().replace(' data-ord="2"', '') +
      '<h2><span class="sn">3</span>Results</h2><div class="blk wide" id="f1">' + plate(1, 'vgt_baseline_axial.png', RUN1, 32, 'owner') + '</div><div class="blk wide" id="f2">' + plate(2, 'vgt_candidate_axial.png', RUN2, 64, 'owner') + '</div>' +
      sugBlock(S.b5) + tableEq().replace(/ data-ord="\d"/g, '') +
      '<h2><span class="sn">4</span>Interpretation</h2>' + sugBlock(S.b6) +
      '<h2><span class="sn">5</span>Next steps</h2>' + sugBlock(S.b7) +
      '<div class="backl"><div class="lbl">Linked from</div><ul><li>' + ic('link') + ' ' + wl(WL_WEEK, 'owner') + '</li><li>' + ic('link') + ' <a class="wl" href="#">Spectral smoothing check</a></li></ul></div>';
    return h;
  }
  var READERS = [
    { id: 'r1', name: 'Prof. Lindqvist', link: 'supervisor link', last: 'opened 6 Nov 19:12', scope: '3 of 4 reports' },
    { id: 'r2', name: 'Dr. Haas', link: 'committee link', last: 'never opened', scope: '1 of 4 reports' }
  ];
  function pageOwner() {
    var st = Q.get('state');
    document.body.innerHTML =
      '<a class="skip btn" href="#main">Skip to report</a>' +
      '<header class="top">' + logo() + '<span class="brand">Vesqira</span><span class="crumb">LMC H I · Reports / <b>Block size 32 vs 64</b></span><span class="sp"></span>' + FLAG + themeBtn() + '</header>' +
      '<div class="desk" id="desk" data-pane="queue">' +
      '<aside class="q pane" id="pq" aria-label="Needs you"><header class="ph2"><h2>Needs you</h2><span class="pill acc" id="qTotal"></span></header><div id="qa"></div><div id="qb"></div><div id="qc"></div></aside>' +
      '<main class="c pane" id="main" tabindex="-1"><div class="statusline" style="position:sticky;top:0;z-index:5">' + ic('bot') + '<span>VGT agent updated 4 blocks today, 10 Nov 08:12, from run <span class="run">' + RUN2 + '</span>. The agent can only change drafts; nothing here is visible to readers until you publish.</span></div>' +
      '<div class="ctool"><div class="seg" role="group" aria-label="Draft view"><button class="btn sm" aria-pressed="true" data-v="trk">Tracked changes</button><button class="btn sm" aria-pressed="false" data-v="new">All accepted</button><button class="btn sm" aria-pressed="false" data-v="old">Rev 4</button></div><span class="sp"></span><button class="btn sm" id="accAll">' + ic('check') + 'Accept all</button></div>' +
      '<article class="doc" id="ownDoc" data-view="trk">' + ownerReport() + '</article></main>' +
      '<aside class="pb pane" id="pp" aria-label="Publish"></aside></div>' +
      '<nav class="mtabs" aria-label="Desk sections"><button data-pane="queue" aria-pressed="true">' + ic('list') + 'Queue <span class="pill" id="mqc"></span></button><button data-pane="c" aria-pressed="false">' + ic('file') + 'Report</button><button data-pane="pb" aria-pressed="false">' + ic('upload') + 'Publish</button></nav>' +
      '<dialog id="pre" aria-labelledby="preT"></dialog><div class="toast" id="toast" role="status" aria-live="polite" hidden></div>';
    bindTheme();
    var desk = $('#desk'), published = 4, sent = false;
    function counts() { var p = SUG.filter(function (s) { return s.st === 'pending'; }).length, a = SUG.filter(function (s) { return s.st === 'acc'; }).length; return { p: p, a: a, r: SUG.length - p - a }; }
    function renderQueue() {
      var c = counts();
      $('#qa').innerHTML = '<section class="qs" aria-labelledby="qa-h"><h3 id="qa-h" class="lbl">' + ic('bot') + 'Agent drafts to review <span class="pill">' + c.p + '</span></h3>' +
        (c.p ? '<ul class="ql">' + SUG.filter(function (s) { return s.st === 'pending'; }).map(function (s) { return '<li><button class="qt" data-go="' + s.block + '"><b>' + s.label + '</b><span class="t2">' + s.sum + '</span></button><div class="qa"><button class="btn sm" data-acc="' + s.id + '">' + ic('check') + 'Accept</button><button class="btn sm" data-rej="' + s.id + '">' + ic('x') + 'Reject</button></div></li>'; }).join('') + '</ul>' : '<p class="t3 qe">All agent drafts reviewed.</p>') + '</section>';
      $('#qb').innerHTML = '<section class="qs" aria-labelledby="qb-h"><h3 id="qb-h" class="lbl">' + ic('msg') + 'Comments awaiting your reply <span class="pill">1</span></h3><ul class="ql"><li><div class="qt static"><b>Figure 2 · pin 1 <span class="pill acc">follow-up</span></b><span class="t3">Prof. Lindqvist, 10 Nov 08:10</span><blockquote>Can you quantify it before the next meeting?</blockquote></div><div class="qa"><button class="btn sm rb" aria-expanded="false">Reply</button></div></li></ul></section>';
      $('#qc').innerHTML = '<section class="qs" aria-labelledby="qc-h"><h3 id="qc-h" class="lbl">' + ic('send') + 'Agent replies awaiting send <span class="pill">' + (DRAFT.state === 'draft' ? 1 : 0) + '</span></h3>' + (DRAFT.state === 'draft'
        ? '<ul class="ql"><li><div class="qt static"><b>' + DRAFT.anchor + '</b><span class="t3">' + DRAFT.who + '</span><blockquote>' + DRAFT.q + '</blockquote></div>' +
        '<div class="drf"><div class="agent-tag">' + ic('bot') + 'Draft by VGT agent, not sent</div><p id="drText">' + DRAFT.text + '</p></div><div class="qa"><button class="btn sm pri" id="drSend">' + ic('send') + 'Send as Denis</button><button class="btn sm" id="drEdit">' + ic('pencil') + 'Edit</button><button class="btn sm ghost" id="drDel">Discard</button></div></li></ul>'
        : '<p class="t3 qe">' + (DRAFT.state === 'sent' ? 'Sent as Denis. It appears for the reader when the server next syncs.' : 'Draft discarded.') + '</p>') + '</section>';
      var tot = c.p + 1 + (DRAFT.state === 'draft' ? 1 : 0);
      $('#qTotal').textContent = tot; $('#mqc').textContent = tot;
    }
    function renderPanel() {
      var c = counts();
      $('#pp').innerHTML = '<section class="ps"><div class="lbl">Publish</div><dl class="kv"><dt>Published</dt><dd>revision ' + published + ' · ' + (published === 4 ? '7 Nov 14:02' : 'just now') + '</dd><dt>Draft</dt><dd>revision ' + (published + 1) + ' · ' + (c.a ? c.a + ' accepted' : 'no accepted changes') + (c.p ? ', ' + c.p + ' pending' : '') + '</dd></dl>' +
        '<button class="btn pri md" id="openPre" style="width:100%">' + ic('upload') + 'Preflight and publish…</button></section>' +
        '<section class="ps"><div class="lbl">Preflight</div><ul class="chk">' +
        '<li class="ok">' + ic('check') + '<span>2 figures go out as pictures; no data attached</span></li>' +
        '<li class="ok">' + ic('check') + '<span>No file paths, hosts or cluster names in text or captions</span></li>' +
        '<li class="ok">' + ic('check') + '<span>Interpretation block is labelled; agent text attributed</span></li>' +
        '<li class="wn">' + ic('alert') + '<span>Header values are shown “as declared, not verified”</span></li>' +
        '<li class="wn">' + ic('alert') + '<span>1 linked report is not shared with readers</span></li>' +
        (c.p ? '<li class="wn">' + ic('alert') + '<span>' + c.p + ' agent suggestion' + (c.p > 1 ? 's' : '') + ' pending; they stay in draft</span></li>' : '<li class="ok">' + ic('check') + '<span>No agent suggestions pending</span></li>') + '</ul></section>' +
        '<section class="ps"><div class="lbl">Readers · ' + READERS.length + '</div><ul class="rdr">' + READERS.map(function (r) { return '<li id="' + r.id + '"><div><b>' + r.name + '</b><div class="t3">' + r.link + ' · ' + r.scope + '</div><div class="t3">' + r.last + '</div></div><button class="btn sm danger" data-revoke="' + r.id + '">' + ic('ban') + 'Revoke</button></li>'; }).join('') + '</ul><button class="btn sm ghost" style="margin-top:var(--sp-2)">' + ic('plus') + 'New reader link</button></section>' +
        '<section class="ps"><div class="lbl">Publish server</div><dl class="kv"><dt>Status</dt><dd><span class="pill ok">online</span></dd><dt>Last push</dt><dd>' + (published === 4 ? '7 Nov 14:02' : 'just now') + '</dd><dt>Comments</dt><dd>synced 10 Nov 11:24</dd></dl></section>';
      $('#openPre').onclick = openPre;
    }
    function setSug(id, state) {
      var s = SUG.filter(function (x) { return x.id === id; })[0]; if (!s) return;
      s.st = state;
      var el = $('[data-sug="' + id + '"]');
      el.dataset.state = state;
      $('.sst', el).textContent = state === 'acc' ? 'Accepted: goes out in revision ' + (published + 1) : state === 'rej' ? 'Rejected: revision 4 text kept' : 'Pending';
      renderQueue(); renderPanel();
    }
    function toast(t) { var e = $('#toast'); e.textContent = t; e.hidden = false; clearTimeout(toast.t); toast.t = setTimeout(function () { e.hidden = true; }, 3200); }
    function openPre() {
      var c = counts(), d = $('#pre'), acc = SUG.filter(function (s) { return s.st === 'acc'; }), pen = SUG.filter(function (s) { return s.st === 'pending'; });
      d.innerHTML = '<form method="dialog" class="pf"><header class="pfh"><div><h2 id="preT">Publish revision ' + (published + 1) + '</h2><p class="t2">To Prof. Lindqvist and Dr. Haas. This is exactly what goes out.</p></div><button type="button" class="btn ic ghost" data-close aria-label="Close preflight">' + ic('x') + '</button></header>' +
        '<div class="pfb"><section><h3>' + ic('file') + 'Changes from revision ' + published + ' <span class="pill">' + acc.length + '</span></h3>' +
        (acc.length ? '<ul class="dl">' + acc.map(function (s) { return '<li><b>' + s.label + '</b><div class="mini">' + s.trk.replace(/<\/?p>/g, '').replace(/<div class="interp">.*?<\/span>/, '').replace(/<\/?div>/g, '') + '</div></li>'; }).join('') + '</ul>' : '<p class="t2">No accepted changes: nothing new to publish.</p>') +
        (pen.length ? '<p class="note wn">' + ic('alert') + '<span>Not included, still agent suggestions: ' + pen.map(function (s) { return s.label; }).join(', ') + '. Readers keep seeing the revision ' + published + ' text there.</span></p>' : '') + '</section>' +
        '<section><h3>' + ic('image') + 'Figures go out as pictures <span class="pill">2</span></h3><div class="thumbs"><img src="../_shared/assets/vgt_baseline_axial.png" alt="Figure 1 preview" width="72" height="72"><img src="../_shared/assets/vgt_candidate_axial.png" alt="Figure 2 preview" width="72" height="72"><p class="t2">Frozen previews with a provenance line. No pixel data, no FITS file, no download of the cube.</p></div></section>' +
        '<section><h3>' + ic('cpu') + 'Run lines that go out <span class="pill">2</span></h3><p class="runs"><span class="run">' + RUN1 + '</span> <span class="run">' + RUN2 + '</span></p><p class="t2">Run id, block size, input file name and channel range. Not included: paths, hosts, cluster or job information.</p></section>' +
        '<section><h3>' + ic('link') + 'Linked reports</h3><ul class="lk"><li>' + ic('check') + '<span><a class="wl" href="#">' + WL_WEEK + '</a> is shared with both readers.</span></li><li class="wn">' + ic('alert') + '<span><b>' + WL_NOISE + '</b> is not shared. Readers see plain text “not shared with you”.</span><button type="button" class="btn sm ghost">Share it too…</button></li></ul></section>' +
        '<section><h3>' + ic('bot') + 'Attribution kept</h3><p class="t2">“drafted by VGT agent, edited by Denis” is shown on the report and on agent-drafted replies.</p></section></div>' +
        '<footer class="pff"><span class="t3">Revision ' + (published + 1) + ' replaces what readers see now. Earlier revisions stay in their history.</span><span class="sp"></span><button type="button" class="btn md" data-close>Cancel</button><button type="submit" class="btn pri md" value="publish"' + (acc.length ? '' : ' disabled') + '>' + ic('upload') + 'Publish revision ' + (published + 1) + '</button></footer></form>';
      d.showModal();
      d.onclose = function () {
        if (d.returnValue === 'publish') { published += 1; SUG.forEach(function (s) { if (s.st === 'acc') { s.st = 'done'; var el = $('[data-sug="' + s.id + '"]'); el.dataset.state = 'done'; $('.sst', el).textContent = 'Published in revision ' + published; } }); renderQueue(); renderPanel(); toast('Revision ' + published + ' pushed to the publish server.'); }
        d.returnValue = '';
      };
    }
    $('#pre').addEventListener('click', function (e) { if (e.target.closest('[data-close]')) $('#pre').close(); else if (e.target === e.currentTarget) $('#pre').close(); });
    document.addEventListener('click', function (e) {
      var b = e.target.closest('button'); if (!b) return;
      if (b.dataset.acc) setSug(b.dataset.acc, 'acc');
      else if (b.dataset.rej) setSug(b.dataset.rej, 'rej');
      else if (b.dataset.go) { var el = document.getElementById(b.dataset.go); if (matchMedia('(max-width:1099px)').matches) setPane('c'); el.scrollIntoView({ block: 'center' }); el.classList.remove('flash'); void el.offsetWidth; el.classList.add('flash'); }
      else if (b.dataset.revoke) { var li = document.getElementById(b.dataset.revoke); b.outerHTML = '<span class="pill err">' + ic('ban') + 'Revoked</span>'; li.classList.add('revoked'); toast('Link revoked. It now shows the neutral “does not open anything” page.'); }
      else if (b.id === 'drSend') { DRAFT.state = 'sent'; renderQueue(); toast('Reply sent as Denis.'); }
      else if (b.id === 'drDel') { DRAFT.state = 'discarded'; renderQueue(); }
      else if (b.id === 'drEdit') { var p = $('#drText'); if (p.tagName === 'P') { var ta = document.createElement('textarea'); ta.id = 'drText'; ta.rows = 6; ta.value = p.textContent; ta.setAttribute('aria-label', 'Edit draft reply'); p.replaceWith(ta); ta.focus(); DRAFT.edit = ta; b.textContent = 'Done'; } else { DRAFT.text = p.value; renderQueue(); } }
      else if (b.classList.contains('rb')) {
        var li2 = b.closest('li'); if ($('form', li2)) return; b.setAttribute('aria-expanded', 'true');
        var f = document.createElement('form'); f.className = 'rf';
        f.innerHTML = '<label class="sr" for="own-r">Reply as Denis</label><textarea id="own-r" rows="3" placeholder="Reply as Denis. Ctrl+Enter sends."></textarea><div class="crow"><button type="button" class="btn sm ghost rc">Cancel</button><button type="submit" class="btn sm pri">' + ic('send') + 'Send</button></div>';
        li2.appendChild(f); $('textarea', f).focus();
        $('textarea', f).onkeydown = function (ev) { if (ev.key === 'Enter' && (ev.ctrlKey || ev.metaKey)) { ev.preventDefault(); f.requestSubmit(); } };
        $('.rc', f).onclick = function () { f.remove(); b.setAttribute('aria-expanded', 'false'); b.focus(); };
        f.onsubmit = function (ev) { ev.preventDefault(); if (!$('textarea', f).value.trim()) return; f.remove(); b.outerHTML = '<span class="pill ok">' + ic('check') + 'Replied</span>'; toast('Reply sent as Denis.'); };
      }
    });
    $$('.ctool .seg button').forEach(function (b) { b.onclick = function () { $$('.ctool .seg button').forEach(function (x) { x.setAttribute('aria-pressed', x === b); }); $('#ownDoc').dataset.view = b.dataset.v; }; });
    $('#accAll').onclick = function () { SUG.forEach(function (s) { if (s.st === 'pending') setSug(s.id, 'acc'); }); };
    function setPane(p) { desk.dataset.pane = p; $$('.mtabs button').forEach(function (b) { b.setAttribute('aria-pressed', b.dataset.pane === p); }); }
    $$('.mtabs button').forEach(function (b) { b.onclick = function () { setPane(b.dataset.pane); window.scrollTo(0, 0); }; });
    renderQueue(); renderPanel();
    if (Q.get('pane')) setPane(Q.get('pane'));
    if (st === 'preflight') { ['s1', 's2', 's3'].forEach(function (i) { setSug(i, 'acc'); }); openPre(); }
  }

  var pg = document.body.dataset.page;
  if (pg === 'reader') pageReader(); else if (pg === 'inbox') pageInbox(); else if (pg === 'owner') pageOwner();
})();
