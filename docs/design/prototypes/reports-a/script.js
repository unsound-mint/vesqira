/* Reports prototype A. Static, no build. All figures/numbers are fictional. */
(function () {
  var Q = new URLSearchParams(location.search);
  var root = document.documentElement;
  var th = Q.get('theme');
  if (th === 'dark' || th === 'light') root.setAttribute('data-theme', th);
  var STATE = Q.get('state') || 'default';
  var $ = function (s, r) { return (r || document).querySelector(s); };
  var $$ = function (s, r) { return Array.prototype.slice.call((r || document).querySelectorAll(s)); };
  var MOBILE = window.matchMedia('(max-width:860px)');

  var P = {
    'file-text': '<path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><path d="M14 2v6h6M16 13H8M16 17H8M10 9H8"/>',
    database: '<ellipse cx="12" cy="5" rx="9" ry="3"/><path d="M3 5v14a9 3 0 0 0 18 0V5M3 12a9 3 0 0 0 18 0"/>',
    layers: '<path d="m12 2-10 5 10 5 10-5z"/><path d="m2 17 10 5 10-5M2 12l10 5 10-5"/>',
    grid: '<rect x="3" y="3" width="7" height="7"/><rect x="14" y="3" width="7" height="7"/><rect x="14" y="14" width="7" height="7"/><rect x="3" y="14" width="7" height="7"/>',
    activity: '<path d="M22 12h-4l-3 9L9 3l-3 9H2"/>',
    sun: '<circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/>',
    chev: '<path d="m6 9 6 6 6-6"/>',
    msg: '<path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z"/>',
    msgplus: '<path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z"/><path d="M12 7v6M9 10h6"/>',
    link: '<path d="M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71"/><path d="M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71"/>',
    image: '<rect x="3" y="3" width="18" height="18" rx="2"/><circle cx="9" cy="9" r="2"/><path d="m21 15-3.09-3.09a2 2 0 0 0-2.82 0L6 21"/>',
    sigma: '<path d="M18 7V5a1 1 0 0 0-1-1H6.5a.5.5 0 0 0-.4.8l4.5 6a2 2 0 0 1 0 2.4l-4.5 6a.5.5 0 0 0 .4.8H17a1 1 0 0 0 1-1v-2"/>',
    send: '<path d="m22 2-7 20-4-9-9-4z"/><path d="M22 2 11 13"/>',
    check: '<path d="M20 6 9 17l-5-5"/>',
    x: '<path d="M18 6 6 18M6 6l12 12"/>',
    eye: '<path d="M2 12s3-7 10-7 10 7 10 7-3 7-10 7-10-7-10-7z"/><circle cx="12" cy="12" r="3"/>',
    eyeoff: '<path d="M9.88 9.88a3 3 0 1 0 4.24 4.24"/><path d="M10.73 5.08A10.43 10.43 0 0 1 12 5c7 0 10 7 10 7a13.16 13.16 0 0 1-1.67 2.68M6.61 6.61A13.53 13.53 0 0 0 2 12s3 7 10 7a9.74 9.74 0 0 0 5.39-1.61M2 2l20 20"/>',
    clock: '<circle cx="12" cy="12" r="10"/><path d="M12 6v6l4 2"/>',
    bot: '<rect x="3" y="11" width="18" height="10" rx="2"/><circle cx="12" cy="5" r="2"/><path d="M12 7v4M8 16h.01M16 16h.01"/>',
    user: '<path d="M19 21v-2a4 4 0 0 0-4-4H9a4 4 0 0 0-4 4v2"/><circle cx="12" cy="7" r="4"/>',
    upload: '<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4M17 8l-5-5-5 5M12 3v12"/>',
    split: '<rect x="3" y="3" width="18" height="18" rx="2"/><path d="M12 3v18"/>',
    code: '<path d="m16 18 6-6-6-6M8 6l-6 6 6 6"/>',
    book: '<path d="M2 3h6a4 4 0 0 1 4 4v14a3 3 0 0 0-3-3H2z"/><path d="M22 3h-6a4 4 0 0 0-4 4v14a3 3 0 0 1 3-3h7z"/>',
    pin: '<path d="M20 10c0 6-8 12-8 12S4 16 4 10a8 8 0 0 1 16 0z"/><circle cx="12" cy="10" r="3"/>',
    warn: '<path d="m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3M12 9v4M12 17h.01"/>',
    menu: '<path d="M4 12h16M4 6h16M4 18h16"/>',
    plus: '<path d="M5 12h14M12 5v14"/>',
    folder: '<path d="M20 20a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.69-.9L9.6 3.9A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2z"/>',
    reply: '<path d="m15 10 5 5-5 5"/><path d="M4 4v7a4 4 0 0 0 4 4h12"/>',
    history: '<path d="M3 12a9 9 0 1 0 9-9 9.75 9.75 0 0 0-6.74 2.74L3 8"/><path d="M3 3v5h5M12 7v5l4 2"/>',
    lock: '<rect x="3" y="11" width="18" height="11" rx="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/>'
  };
  function ic(n, c) { return '<svg class="ic ' + (c || '') + '" viewBox="0 0 24 24" aria-hidden="true">' + P[n] + '</svg>'; }
  // fill every <i data-i="name"> with an icon
  function icons(r) { $$('i[data-i]', r).forEach(function (e) { e.outerHTML = ic(e.dataset.i, e.dataset.c); }); }

  /* ---------------- fixture ---------------- */
  var LAST_SYNC_H = STATE === 'offline' ? 48 : 3;       // reader view: author's workspace last synced this long ago
  var OFFLINE = STATE === 'offline';
  var LABEL = { 'b-q': 'Question', 'b-setup-ch': 'Setup, channels', 'b-fig2': 'Figure 2', 'b-interp': 'Interpretation', 'b-fig1': 'Figure 1', 'b-table': 'Table 1' };
  var ORDER = ['b-q', 'b-setup-runs', 'b-setup-ch', 'b-fig1', 'b-res', 'b-fig2', 'b-table', 'b-eq', 'b-interp', 'b-next'];
  var THREADS = [
    { id: 't1', block: 'b-q', rev: 3, resolved: true, resolvedBy: 'Denis', resolvedAgeH: 30, msgs: [
      { who: 'prof', name: 'Prof. Okafor', t: '8 Nov, 21:14', ageH: 41.6, text: 'Which physical scale does a 32-pixel block correspond to at the LMC distance? It should be stated up front.' },
      { who: 'owner', name: 'Denis', t: '9 Nov, 09:02', ageH: 30, text: 'Added the angular size of one block, from the declared pixel scale, to the Setup paragraph.' }] },
    { id: 't2', block: 'b-fig2', rev: 4, pin: { n: 1, x: 58, y: 34, panel: 'difference map' }, msgs: [
      { who: 'prof', name: 'Prof. Okafor', t: '9 Nov, 19:50', ageH: 19, text: 'There is a coherent residual along the north-east ridge. Is this the edge of the noise mask?' },
      { who: 'owner', name: 'Denis', t: '10 Nov, 08:30', ageH: 6.3, text: 'The mask edge is excluded in both runs. I will check the reliability map there before claiming anything.' }] },
    { id: 't3', block: 'b-setup-ch', rev: 3, changed: true, quote: 'Channels from −15 to +15 km/s enter both runs.', msgs: [
      { who: 'prof', name: 'Prof. Okafor', t: '7 Nov, 10:05', ageH: 76, text: 'Why ±15 km/s? The line is wider toward the bar, so this range may cut real emission.' }] },
    { id: 't4', block: 'b-interp', rev: 4, msgs: [
      { who: 'prof', name: 'Prof. Okafor', t: '10 Nov, 14:35', ageH: 0.25, text: 'A difference of 0.05 with those uncertainties: are you comfortable calling it a trend? And what exactly is the ± here?' },
      { who: 'agent', draft: true, name: 'VGT agent', t: 'draft, 15:31', ageH: 0.1, text: 'The ± is the bootstrap standard error over spatial blocks (see the definition under Table 1). Combined in quadrature the difference is about 1σ, so the text says the runs are not clearly distinguishable. The two runs share the same pixels, so even that overstates their independence.' }] }
  ];
  var composing = null, justOpened = false;

  function visibleMsgs(t, ctx) {
    return t.msgs.filter(function (m) {
      if (m.draft) return ctx === 'owner';
      if (ctx === 'reader' && OFFLINE && m.who !== 'prof' && m.ageH < LAST_SYNC_H) return false;
      return true;
    });
  }
  function isResolved(t, ctx) { return t.resolved && !(ctx === 'reader' && OFFLINE && t.resolvedAgeH < LAST_SYNC_H); }
  function anchorLabel(t) {
    var s = ic(t.pin ? 'pin' : 'msg') + '<span>' + LABEL[t.block] + (t.pin ? ', place ' + t.pin.n + ' on the ' + t.pin.panel : '') + '</span>';
    if (t.changed) s += '<span class="stale">on revision ' + t.rev + ' · this paragraph changed since</span>';
    else if (t.rev < 4) s += '<span>on revision ' + t.rev + '</span>';
    return s;
  }
  function msgHTML(t, m, ctx) {
    var by = m.who === 'agent' ? ' <span class="by">drafted by VGT agent · only you see this</span>' : '';
    var h = '<div class="msg' + (m.draft ? ' draft' : '') + '"><div class="who">' + m.name + (m.who === 'agent' ? '' : '') + '<time>' + m.t + '</time>' + by + '</div>';
    if (m.editing) h += '<textarea class="inp" aria-label="Edit draft reply" data-draftedit="' + t.id + '">' + m.text + '</textarea>';
    else h += '<p>' + m.text + '</p>';
    if (m.draft) h += '<div class="acts" style="margin-top:8px"><button class="btn sm pri" data-act="send" data-t="' + t.id + '">' + ic('send') + 'Send as Denis</button><button class="btn sm" data-act="edit" data-t="' + t.id + '">' + (m.editing ? 'Done editing' : 'Edit') + '</button><button class="btn sm ghost" data-act="discard" data-t="' + t.id + '">Discard</button></div>';
    if (ctx === 'reader' && m.who === 'prof') {
      var seen = m.ageH > LAST_SYNC_H;
      h += '<div class="deliv' + (seen ? '' : ' pend') + '">' + ic(seen ? 'check' : 'clock') + (seen ? 'Seen by the author' : (OFFLINE ? 'Held on the publish server · the author’s workspace has not synced since' : 'Not yet seen by the author · their workspace has not synced since')) + '</div>';
    }
    return h + '</div>';
  }
  function composerHTML(label, t) {
    return '<div class="composer"><label class="sr" for="cmp">' + label + '</label><textarea class="inp" id="cmp" placeholder="' + (t ? 'Write a reply' : 'Write a comment on this paragraph') + '"></textarea><div class="row"><span class="hint">Ctrl+Enter to post. Esc to cancel.</span><button class="btn sm ghost" data-act="cancel">Cancel</button><button class="btn sm pri" data-act="post">Post</button></div></div>';
  }
  function threadHTML(t, ctx) {
    var res = isResolved(t, ctx), msgs = visibleMsgs(t, ctx);
    var open = t._open || (ctx === 'owner' && false);
    var h = '<article class="thr' + (res ? ' resolved' : '') + (open ? ' open' : '') + '" id="' + ctx + '-' + t.id + '" data-block="' + t.block + '" data-t="' + t.id + '" tabindex="-1" aria-label="Comment thread on ' + LABEL[t.block] + '">';
    if (res) h += '<button class="rsum" data-act="expand" data-t="' + t.id + '" aria-expanded="' + !!open + '">' + ic('check') + '<span>Resolved by ' + t.resolvedBy + '</span><span class="t3">' + LABEL[t.block] + '</span><span style="margin-left:auto" class="t3">' + (open ? 'Hide' : 'Show') + '</span></button>';
    h += '<div class="body"><div class="anc">' + anchorLabel(t) + '</div>';
    if (t.changed) h += '<blockquote class="quote">“' + t.quote + '”</blockquote>';
    h += msgs.map(function (m) { return msgHTML(t, m, ctx); }).join('');
    if (composing && composing.thread === t.id) h += composerHTML('Reply', t);
    else h += '<div class="acts"><button class="btn sm ghost" data-act="reply" data-t="' + t.id + '">' + ic('reply') + 'Reply</button>' + (ctx === 'owner' ? '<button class="btn sm ghost" data-act="resolve" data-t="' + t.id + '">' + ic('check') + (t.resolved ? 'Reopen' : 'Resolve') + '</button>' : '') + '</div>';
    return h + '</div></article>';
  }
  function sortedThreads() { return THREADS.slice().sort(function (a, b) { return ORDER.indexOf(a.block) - ORDER.indexOf(b.block); }); }
  function newCardHTML(block) { return '<article class="thr newc" data-block="' + block + '" id="newc"><div class="anc">' + ic('msgplus') + '<span>New comment on ' + (LABEL[block] || 'paragraph') + '</span></div>' + composerHTML('New comment', null) + '</article>'; }

  /* ---------------- report body ---------------- */
  function wl(name, owner) {
    var shared = name === 'Noise masking threshold sweep';
    if (owner) return '<a class="wikilink" href="#" data-wl="' + name + '">' + name + '</a>' + (shared ? '' : ' <span class="pill warn" title="Prof. Okafor will see plain text"><i data-i="eyeoff"></i>not shared with Prof. Okafor</span>');
    if (shared) return '<a class="wikilink" href="#space">' + name + '</a>';
    return '<span class="notshared" title="This report has not been shared with you">' + name + ' (not shared with you)</span>';
  }
  var PROV = '<div class="prov">' + ic('lock') + '<span>lmc_hi_cube.fits · −12…+12 km/s (declared, LSRK) · runs 2026-11-08_a1f3c, 2026-11-08_9be20 · header values as declared, not verified</span></div>';
  function reportHTML(owner) {
    var h = '';
    h += '<h1>Block size 32 vs 64: gradient orientation on the LMC H I cube</h1>';
    h += '<div class="meta">' + (owner
      ? '<span class="attrib">' + ic('bot') + 'Drafted by VGT agent, edited by Denis</span><span class="sep">·</span><span>Draft revision 5</span><span class="sep">·</span><span>Published revision 4</span>'
      : '<span class="attrib">' + ic('bot') + 'Drafted by VGT agent, edited by Denis</span><span class="sep">·</span><span>Published by Denis, 9 Nov 2026, 17:30</span>') + '</div>';
    h += '<h2 id="s-question">Question</h2>';
    h += blk('b-q', '<p>Does the VGT orientation estimated with 64-pixel blocks align with the reference orientation better or worse than the one from 32-pixel blocks, on the same cube and channel range?</p>');
    h += '<h2 id="s-setup">Setup</h2>';
    h += blk('b-setup-runs', '<p>Two runs share one input, <code>lmc_hi_cube.fits</code>, and differ only in block size. One block of 32 px spans the angular size given by the declared pixel scale.</p>' +
      '<div class="runline">' + ic('activity') + '<b>2026-11-08_a1f3c</b><span>block 32</span><span>finished 8 Nov, 22:10</span><span class="pill">computation completed</span></div>' +
      '<div class="runline">' + ic('activity') + '<b>2026-11-08_9be20</b><span>block 64</span><span>finished 8 Nov, 23:41</span><span class="pill">computation completed</span></div>' +
      '<p class="note-sm">Completed means the computation ran. It says nothing about scientific validity.</p>');
    h += blk('b-setup-ch', '<p>Channels from −12 to +12 km/s (declared LSRK velocity, taken from the header and not independently verified) enter both runs. The noise mask is described in ' + wl('Noise masking threshold sweep', owner) + ' and is the same for both runs.</p>');
    h += '<h2 id="s-results">Results</h2>';
    h += blk('b-fig1', '<figure class="fig"><div class="pnls"><div class="pn"><img src="../_shared/assets/vgt_baseline_axial.png" alt="Axial orientation field, block 32, LMC H I, channels minus 12 to plus 12 kilometres per second" width="512" height="512"><span class="cap">block 32</span></div><div class="pn"><img src="../_shared/assets/vgt_candidate_axial.png" alt="Axial orientation field, block 64, same cube and channels" width="512" height="512"><span class="cap">block 64</span><span class="frz">preview image</span></div></div>' +
      '<img class="legend" src="../_shared/assets/axial_legend.png" alt="Cyclic axial colour scale from 0 to 180 degrees; 0 and 180 are the same colour" width="361" height="24"><div class="legend-l"><span>0°</span><span>90°</span><span>180° (= 0°)</span></div>' +
      '<figcaption><b>Figure 1.</b> Axial orientation from block 32 (left) and block 64 (right). The scale is cyclic: 0° and 180° are the same orientation.</figcaption>' + PROV + '</figure>');
    h += blk('b-res', '<p>The large-scale pattern is the same in both runs. The finer structure in the south-east is smoothed out with 64-pixel blocks; first notes on this are in ' + wl('Weekly notes 2026-W45', owner) + '.</p>');
    h += blk('b-fig2', '<figure class="fig"><div class="pnls"><div class="pn"><img src="../_shared/assets/vgt_axial_diff.png" alt="Axial difference map between the block 32 and block 64 orientation fields" width="512" height="512"><span class="cap">difference</span>' + pinBtn(owner) + '</div><div class="pn"><img src="../_shared/assets/vgt_reliability.png" alt="Reliability map for the block 64 run" width="512" height="512"><span class="cap">reliability</span><span class="frz">preview image</span></div></div>' +
      '<figcaption><b>Figure 2.</b> Axial difference between the two orientation fields (left) and reliability of the block 64 run (right).</figcaption>' + PROV + '</figure>');
    h += blk('b-table', '<div class="tblw"><table class="dt"><caption><b>Table 1.</b> Alignment measure AM per run</caption><thead><tr><th scope="col">Run</th><th scope="col">Block<small>px</small></th><th scope="col">AM<small>dimensionless</small></th><th scope="col">Blocks used<small>count</small></th><th scope="col">Excluded<small>count</small></th></tr></thead><tbody>' +
      '<tr><td>2026-11-08_a1f3c</td><td>32</td><td>0.41 ± 0.03</td><td>18 432</td><td>2 112</td></tr>' +
      '<tr><td>2026-11-08_9be20</td><td>64</td><td>0.36 ± 0.04</td><td>4 608</td><td class="na">—</td></tr></tbody></table></div>' +
      '<p class="note-sm" style="margin-top:8px">Excluded blocks for block 64 are not recorded in the run output; shown as unknown (—), not zero. ± is the bootstrap standard error over spatial blocks, as declared in the run configuration.</p>');
    h += blk('b-eq', '<div class="eq"><math display="block" aria-label="AM equals the mean of cosine of two theta r"><mrow><mi mathvariant="normal">AM</mi><mo>=</mo><mo>⟨</mo><mi>cos</mi><mo>⁡</mo><mo>(</mo><mn>2</mn><msub><mi>θ</mi><mi mathvariant="normal">r</mi></msub><mo>)</mo><mo>⟩</mo></mrow></math><span class="no">(1)</span></div>' +
      '<p>Here θ<sub>r</sub> is the angle between the VGT orientation and the reference orientation. Both are axial, defined modulo 180°, so the average is taken over cos 2θ<sub>r</sub> and never over the raw angles.</p>');
    h += '<h2 id="s-interp">Interpretation <span class="tag-interp">interpretation · not validated</span></h2>';
    var interpTxt = 'AM is slightly lower for block 64 (0.36 ± 0.04 against 0.41 ± 0.03). With these uncertainties the two values are not clearly distinguishable, so this pair of runs does not show that block size matters for alignment on this cube.';
    if (owner) h += blk('b-interp', '<div class="agentchg" data-chg="1"><p>' + interpTxt + ' <ins>Both runs use the same pixels, so their uncertainties are not independent and the difference is probably less certain than the quoted ± suggests.</ins></p>' + acbar('Agent change 1 of 2: added a caveat sentence') + '</div>');
    else h += blk('b-interp', '<p>' + interpTxt + '</p>');
    h += '<h2 id="s-next">Next steps</h2>';
    h += blk('b-next', '<ul><li>Repeat with block 16 and block 48 to see whether the trend is monotonic.</li><li>Rerun both after the mask sweep in ' + wl('Noise masking threshold sweep', owner) + ' settles.</li>' +
      (owner ? '<li class="agentchg" data-chg="2">Compare the south-east region with the observations in ' + wl('Weekly notes 2026-W45', true) + '.' + acbar('Agent change 2 of 2: added a next step') + '</li>' : '') + '</ul>');
    return h;
  }
  function acbar(txt) { return '<div class="acbar">' + ic('bot') + '<span>' + txt + ', pending your review</span><button class="btn sm" data-act="accept">' + ic('check') + 'Accept</button><button class="btn sm ghost" data-act="revert">Revert</button></div>'; }
  function pinBtn(owner) { var t = THREADS[1]; return '<button class="pin" data-act="pin" data-t="t2" style="left:' + t.pin.x + '%;top:' + t.pin.y + '%" aria-label="Comment thread 1 on the difference map of Figure 2">1</button>'; }
  function blk(id, inner) { return '<div class="blk" id="' + id + '" data-block="' + id + '">' + inner + '</div>'; }

  /* ---------------- markdown source (owner) ---------------- */
  var SRC = [
    '# Block size 32 vs 64: gradient orientation on the LMC H I cube', '',
    '## Question',
    'Does the VGT orientation estimated with 64-pixel blocks align with the reference orientation better or worse than the one from 32-pixel blocks, on the same cube and channel range?', '',
    '## Setup',
    'Two runs share one input, `lmc_hi_cube.fits`, and differ only in block size. One block of 32 px spans the angular size given by the declared pixel scale.', '',
    '![[run 2026-11-08_a1f3c]]', '![[run 2026-11-08_9be20]]', '',
    'Channels from −12 to +12 km/s (declared LSRK velocity, taken from the header and not independently verified) enter both runs. The noise mask is described in [[Noise masking threshold sweep]] and is the same for both runs.', '',
    '## Results',
    '![[figure fig_7c41 @ rev 2]]',
    'The large-scale pattern is the same in both runs. The finer structure in the south-east is smoothed out with 64-pixel blocks; first notes on this are in [[Weekly notes 2026-W45]].', '',
    '![[figure fig_7c44 @ rev 1]]', '',
    '| Run | Block (px) | AM | Blocks used | Excluded |', '| --- | --- | --- | --- | --- |',
    '| 2026-11-08_a1f3c | 32 | 0.41 ± 0.03 | 18432 | 2112 |',
    '| 2026-11-08_9be20 | 64 | 0.36 ± 0.04 | 4608 | — |', '',
    '$$ \\mathrm{AM} = \\langle \\cos(2\\theta_r) \\rangle \\tag{1} $$',
    'Here $\\theta_r$ is the angle between the VGT orientation and the reference orientation. Both are axial, defined modulo 180°, so the average is taken over $\\cos 2\\theta_r$ and never over the raw angles.', '',
    '## Interpretation {interpretation, not validated}',
    'AM is slightly lower for block 64 (0.36 ± 0.04 against 0.41 ± 0.03). With these uncertainties the two values are not clearly distinguishable, so this pair of runs does not show that block size matters for alignment on this cube. Both runs use the same pixels, so their uncertainties are not independent and the difference is probably less certain than the quoted ± suggests.', '',
    '## Next steps',
    '- Repeat with block 16 and block 48 to see whether the trend is monotonic.',
    '- Rerun both after the mask sweep in [[Noise masking threshold sweep]] settles.',
    '- Compare the south-east region with the observations in [[Weekly notes 2026-W45]].'
  ];
  var AG = { 29: 1, 34: 1 };
  function esc(s) { return s.replace(/&/g, '&amp;').replace(/</g, '&lt;'); }
  function hl(s) {
    s = esc(s);
    if (/^#/.test(s)) return '<span class="h">' + s + '</span>';
    s = s.replace(/(!\[\[[^\]]+\]\])/g, '<span class="em">$1</span>').replace(/((?:^|[^!])\[\[[^\]]+\]\])/g, function (m) { return m.charAt(0) === '[' ? '<span class="wl">' + m + '</span>' : m.charAt(0) + '<span class="wl">' + m.slice(1) + '</span>'; });
    s = s.replace(/(`[^`]+`)/g, '<span class="cd">$1</span>').replace(/(\$[^$]+\$|\$\$[^$]+\$\$)/g, '<span class="mt">$1</span>');
    if (/^\|/.test(s)) s = '<span class="cd">' + s + '</span>';
    return s;
  }
  function srcHTML() { return SRC.map(function (l, i) { return '<div class="l' + (AG[i + 1] ? ' ag' : '') + '"><span class="n">' + (i + 1) + '</span><span class="t">' + (hl(l) || ' ') + '</span></div>'; }).join(''); }

  /* ---------------- shared behaviour ---------------- */
  function counts(b) { return THREADS.filter(function (t) { return t.block === b; }).length; }
  function addBlockButtons(owner) {
    $$('.blk').forEach(function (b) {
      var id = b.dataset.block, n = counts(id), btn = document.createElement('button');
      btn.className = (owner ? 'cb' : 'rb') + (n ? ' has' : '');
      btn.setAttribute('data-act', owner ? 'ownerjump' : 'cmt'); btn.dataset.b = id;
      var lbl = n ? n + ' comment thread' + (n > 1 ? 's' : '') + ' on this paragraph' : 'Add a comment on this paragraph';
      btn.setAttribute('aria-label', (LABEL[id] || 'Paragraph') + ': ' + lbl);
      btn.innerHTML = ic(n ? 'msg' : 'msgplus') + (n ? '<span>' + n + '</span>' : '') + (!owner && MOBILE.matches ? '<span>' + (n ? n : 'Comment') + '</span>' : '');
      if (!owner && MOBILE.matches) btn.innerHTML = ic(n ? 'msg' : 'msgplus') + '<span>' + (n ? n + (n > 1 ? ' threads' : ' thread') : 'Comment') + '</span>';
      b.appendChild(btn);
    });
  }
  function setActive(b) { $$('.blk.active').forEach(function (e) { e.classList.remove('active'); }); if (b) { var e = document.getElementById(b); e && e.classList.add('active'); } }

  function onDocClick(ctx, rerender) {
    document.addEventListener('click', function (e) {
      var a = e.target.closest('[data-act]'); if (!a) return;
      var act = a.dataset.act, tid = a.dataset.t, t = THREADS.filter(function (x) { return x.id === tid; })[0];
      if (act === 'reply') { composing = { thread: tid, block: t.block }; justOpened = true; rerender(); }
      else if (act === 'cancel') { composing = null; rerender(); }
      else if (act === 'post') post(a, ctx, rerender);
      else if (act === 'expand') { t._open = !t._open; rerender(); }
      else if (act === 'resolve') { t.resolved = !t.resolved; t.resolvedBy = 'Denis'; t.resolvedAgeH = 0; t._open = false; rerender(); }
      else if (act === 'send') { var m = t.msgs.filter(function (x) { return x.draft; })[0]; var ta = $('[data-draftedit="' + tid + '"]'); if (ta) m.text = ta.value; m.draft = false; m.who = 'owner'; m.name = 'Denis'; m.t = 'just now, 15:42'; m.ageH = 0; m.by = 1; m.editing = false; rerender(); toast('Reply sent as Denis. It reaches Prof. Okafor at the next publish sync.'); }
      else if (act === 'edit') { var d = t.msgs.filter(function (x) { return x.draft; })[0]; if (d.editing) { var ta2 = $('[data-draftedit="' + tid + '"]'); if (ta2) d.text = ta2.value; } d.editing = !d.editing; justOpened = d.editing; rerender(); }
      else if (act === 'discard') { t.msgs = t.msgs.filter(function (x) { return !x.draft; }); rerender(); }
    });
    document.addEventListener('keydown', function (e) {
      var ta = e.target.closest && e.target.closest('.composer textarea');
      if (!ta) return;
      if (e.key === 'Escape') { composing = null; rerender(); }
      else if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) { e.preventDefault(); post(ta, ctx, rerender); }
    });
  }
  function post(from, ctx, rerender) {
    var box = from.closest('.thr'), ta = $('textarea', box), text = ta ? ta.value.trim() : '';
    if (!text) { ta && ta.focus(); return; }
    var who = ctx === 'owner' ? { who: 'owner', name: 'Denis' } : { who: 'prof', name: 'Prof. Okafor' };
    var msg = { who: who.who, name: who.name, t: 'just now', ageH: 0, text: text.replace(/</g, '&lt;') };
    if (composing.thread) THREADS.filter(function (x) { return x.id === composing.thread; })[0].msgs.push(msg);
    else THREADS.push({ id: 'n' + Date.now(), block: composing.block, rev: 4, msgs: [msg] });
    composing = null; rerender();
  }
  var toastT;
  function toast(msg) { var t = $('.toast'); if (!t) { t = document.createElement('div'); t.className = 'toast'; t.setAttribute('role', 'status'); document.body.appendChild(t); } t.textContent = msg; t.hidden = false; clearTimeout(toastT); toastT = setTimeout(function () { t.hidden = true; }, 4500); }
  window.__toast = toast;

  /* ---------------- reader ---------------- */
  var SPACE = [
    { n: 'Block size 32 vs 64: gradient orientation on the LMC H I cube', s: 'rev 4 · 9 Nov · 3 open comments', cur: 1 },
    { n: 'Noise masking threshold sweep', s: 'rev 2 · 6 Nov' },
    { n: 'Calibrating reliability maps', s: 'rev 1 · 2 Nov' }
  ];
  function initReader() {
    document.body.classList.add('reader');
    var app = $('#app');
    if (STATE === 'revoked') {
      document.body.classList.add('neutralbody');
      document.title = 'Link';
      app.innerHTML = '<div style="position:fixed;right:12px;top:12px"><span class="mockflag">ILLUSTRATIVE MOCK DATA</span></div><main class="neutral"><div class="in"><h1>This link does not open anything.</h1><p>Check that you have the complete link. If you expected to see something here, ask the person who sent it to you.</p></div></main>';
      return;
    }
    var empty = STATE === 'empty';
    var idx = '<aside class="ridx" id="space" data-collapsed="' + (MOBILE.matches ? 1 : 0) + '" aria-label="Reports shared with you"><div class="who"><b>Shared with Prof. Okafor</b>Read-only link, no account. Named by Denis; he can revoke it at any time.</div>' +
      '<nav aria-label="Reports in this space"><ul>' + (empty ? '<li><p class="note-sm" style="padding:8px">No reports yet.</p></li>' : SPACE.map(function (r) { return '<li><a href="#"' + (r.cur ? ' aria-current="page"' : '') + '>' + r.n + '<small>' + r.s + '</small></a></li>'; }).join('')) + '</ul></nav>' +
      '<div class="foot"><span>This page only shows pictures of figures and the text of reports, never data files, paths or machines.</span></div></aside>';
    var top = '<div class="rtop"><button class="btn idxbtn" id="idxbtn" aria-expanded="' + !MOBILE.matches + '" aria-controls="space">' + ic('menu') + 'Reports</button>' +
      (empty ? '<b style="font-size:14px">Space</b>' : '<details class="revs"><summary aria-label="Revision history">' + ic('history') + 'Revision 4, latest' + ic('chev') + '</summary><div class="pop"><ul class="hist" style="margin-bottom:8px"><li class="cur"><div class="h1">Revision 4 <span class="pill ok">latest</span></div><small>9 Nov, 17:30 · published by Denis</small></li><li><div class="h1">Revision 3</div><small>6 Nov, 11:12 · channel range was −15…+15 km/s</small></li><li><div class="h1">Revision 2</div><small>4 Nov, 16:40</small></li></ul><p class="note-sm" style="margin:0">Comments stay attached to the revision they were written on.</p></div></details>') +
      '<span class="sp"></span><span class="mockflag">ILLUSTRATIVE MOCK DATA</span>' +
      '<button class="btn sq ghost" id="themebtn" aria-label="Toggle light and dark theme">' + ic('sun') + '</button></div>';
    var sync = empty ? '' : '<div class="sync' + (OFFLINE ? ' late' : '') + '" role="status">' + ic(OFFLINE ? 'warn' : 'clock', 'md') + '<span>' + (OFFLINE ? 'The author’s workspace last synced 2 days ago. You can still comment; your comments are held on the publish server and the author will see them when their workspace next syncs.' : 'Denis’s workspace last synced 3 h ago. Comments you leave after that are marked as not yet seen.') + '</span></div>';
    var body = empty
      ? '<main class="empty"><div class="in"><h1>Nothing has been shared with you yet.</h1><p>Reports appear here when Denis publishes them to this space. Come back to this link later; you do not need an account.</p></div></main>'
      : '<main class="doc"><article class="prose" id="report" aria-label="Report"></article><div class="margin" id="margin" aria-label="Comments"></div></main>';
    app.innerHTML = '<div class="rd">' + idx + '<div class="rmain">' + top + sync + body + '</div></div>';
    $('#themebtn').onclick = themeToggle;
    $('#idxbtn').onclick = function () { var s = $('#space'), c = s.dataset.collapsed === '1'; s.dataset.collapsed = c ? 0 : 1; this.setAttribute('aria-expanded', c); };
    if (empty) return;
    $('#report').innerHTML = reportHTML(false);
    icons($('#report'));
    addBlockButtons(false);
    var reflow = function () { renderMargin(); renderSheet(); };
    function renderMargin() {
      var m = $('#margin'); if (!m) return;
      var h = sortedThreads().map(function (t) { return threadHTML(t, 'reader'); }).join('');
      if (composing && !composing.thread) {
        var has = THREADS.some(function (t) { return t.block === composing.block; });
        h += newCardHTML(composing.block);
      }
      m.innerHTML = h;
      if (justOpened && !MOBILE.matches) { var ta = $('textarea', m); if (ta) ta.focus(); justOpened = false; }
      layoutMargin();
    }
    var sheetBlock = null;
    function renderSheet() {
      var s = $('.sheet'); if (!MOBILE.matches || !sheetBlock) { if (s) { s.remove(); $('.sheet-bd') && $('.sheet-bd').remove(); } return; }
      if (!s) {
        var bd = document.createElement('div'); bd.className = 'sheet-bd'; bd.onclick = closeSheet; document.body.appendChild(bd);
        s = document.createElement('div'); s.className = 'sheet'; s.setAttribute('role', 'dialog'); s.setAttribute('aria-label', 'Comments'); document.body.appendChild(s);
      }
      var list = sortedThreads().filter(function (t) { return t.block === sheetBlock; });
      var h = '<div class="sh-h"><b>' + (LABEL[sheetBlock] || 'This paragraph') + '</b><button class="btn sq ghost" data-act="closesheet" aria-label="Close comments">' + ic('x') + '</button></div><div class="sh-b">';
      h += list.map(function (t) { return threadHTML(t, 'reader'); }).join('');
      if (composing && !composing.thread) h += newCardHTML(sheetBlock);
      else if (!composing) h += '<div style="padding-top:8px"><button class="btn md" data-act="cmtnew" data-b="' + sheetBlock + '">' + ic('msgplus') + 'Add a comment</button></div>';
      s.innerHTML = h + '</div>';
      if (justOpened) { var ta = $('textarea', s); if (ta) ta.focus(); justOpened = false; }
    }
    function closeSheet() { sheetBlock = null; composing = null; setActive(null); renderSheet(); }
    function layoutMargin() {
      var m = $('#margin'); if (!m || MOBILE.matches) return;
      var base = m.getBoundingClientRect().top + window.scrollY, y = 0;
      $$('.thr', m).forEach(function (c) {
        var b = document.getElementById(c.dataset.block); if (!b) return;
        var want = b.getBoundingClientRect().top + window.scrollY - base;
        var top = Math.max(want, y); c.style.top = top + 'px';
        y = top + c.offsetHeight + 16;
      });
      m.style.height = Math.max(y, 0) + 'px';
    }
    window.__relayout = layoutMargin;
    onDocClick('reader', function () { renderMargin(); renderSheet(); });
    document.addEventListener('click', function (e) {
      var a = e.target.closest('[data-act]'); if (!a) return;
      var act = a.dataset.act;
      if (act === 'cmt' || act === 'cmtnew') {
        var b = a.dataset.b, has = THREADS.some(function (t) { return t.block === b; });
        setActive(b);
        if (MOBILE.matches) { sheetBlock = b; if (act === 'cmtnew' || !has) { composing = { block: b }; justOpened = true; } renderSheet(); return; }
        if (!has || act === 'cmtnew') { composing = { block: b }; justOpened = true; renderMargin(); var nc = $('#newc'); nc && nc.scrollIntoView({ block: 'nearest', behavior: 'auto' }); }
        else { var c = $('#margin .thr[data-block="' + b + '"]'); c.classList.add('active'); c.focus({ preventScroll: false }); }
      } else if (act === 'pin') {
        var tt = a.dataset.t; setActive('b-fig2'); $$('.pin').forEach(function (p) { p.classList.add('on'); });
        if (MOBILE.matches) { sheetBlock = 'b-fig2'; renderSheet(); } else { var cc = document.getElementById('reader-' + tt); cc && cc.focus(); }
      } else if (act === 'closesheet') closeSheet();
    });
    document.addEventListener('focusin', function (e) { var c = e.target.closest && e.target.closest('.margin .thr'); $$('.margin .thr.active').forEach(function (x) { x.classList.remove('active'); }); if (c && c.dataset.block) { c.classList.add('active'); setActive(c.dataset.block); } });
    document.addEventListener('keydown', function (e) { if (e.key === 'Escape' && sheetBlock && !(e.target.closest && e.target.closest('.composer'))) closeSheet(); });
    renderMargin();
    var rl = function () { layoutMargin(); }; window.addEventListener('resize', function () { if (MOBILE.matches !== (document.body.dataset.m === '1')) { document.body.dataset.m = MOBILE.matches ? 1 : 0; location.reload(); } rl(); });
    document.body.dataset.m = MOBILE.matches ? 1 : 0;
    $$('#report img').forEach(function (i) { if (!i.complete) i.addEventListener('load', rl); });
    if (document.fonts && document.fonts.ready) document.fonts.ready.then(rl);
    window.addEventListener('load', rl);
    document.addEventListener('click', function (e) { if (e.target.closest('.rsum')) setTimeout(rl, 0); });
    // thread hover highlights paragraph
    document.addEventListener('mouseover', function (e) { var c = e.target.closest && e.target.closest('.margin .thr'); if (c && c.dataset.block) setActive(c.dataset.block); });
  }
  function themeToggle() {
    var cur = root.getAttribute('data-theme') || (matchMedia('(prefers-color-scheme:light)').matches ? 'light' : 'dark');
    root.setAttribute('data-theme', cur === 'dark' ? 'light' : 'dark');
  }

  /* ---------------- owner ---------------- */
  function initOwner() {
    document.body.classList.add('owner');
    icons(document);
    $('#report').innerHTML = reportHTML(true); icons($('#report'));
    $('#src').innerHTML = srcHTML();
    addBlockButtons(true);
    var pend = 2;
    function renderInsp() { var el = $('#thr-list'); el.innerHTML = sortedThreads().map(function (t) { return threadHTML(t, 'owner'); }).join(''); if (justOpened) { var ta = $('textarea', el); ta && ta.focus(); justOpened = false; } }
    renderInsp();
    onDocClick('owner', renderInsp);
    $$('.sh[aria-controls]').forEach(function (b) { b.onclick = function () { var x = b.getAttribute('aria-expanded') === 'true'; b.setAttribute('aria-expanded', !x); document.getElementById(b.getAttribute('aria-controls')).hidden = x; }; });
    // mode toggle
    var panes = $('.panes');
    function mode(m) { panes.dataset.mode = m; $$('.seg button').forEach(function (b) { b.setAttribute('aria-pressed', b.dataset.mode === m); }); }
    $$('.seg button').forEach(function (b) { b.onclick = function () { mode(b.dataset.mode); }; });
    mode(panes.clientWidth < 700 || MOBILE.matches ? 'preview' : 'split');
    // mobile tabs
    $$('.mtabs button').forEach(function (b) { b.onclick = function () { $('.wb').dataset.tab = b.dataset.tab; $$('.mtabs button').forEach(function (x) { x.setAttribute('aria-selected', x === b); }); }; });
    $('#themebtn').onclick = themeToggle;
    // agent changes
    function updPend() { $$('[data-pend]').forEach(function (e) { e.textContent = pend; }); if (!pend) { var rb = $('.reviewbar'); rb && (rb.hidden = true); $$('.l.ag').forEach(function (l) { l.classList.remove('ag'); }); } var p = $('#pubbtn-n'); }
    document.addEventListener('click', function (e) {
      var a = e.target.closest('[data-act]'); if (!a) return;
      if (a.dataset.act === 'accept' || a.dataset.act === 'revert') {
        var w = a.closest('[data-chg]'); if (a.dataset.act === 'revert') { var ins = $('ins', w); if (ins) ins.remove(); else w.remove(); } w.classList.remove('agentchg'); var bar = $('.acbar', w); bar && bar.remove(); pend--; updPend();
      } else if (a.dataset.act === 'accept-all') { $$('[data-chg]').forEach(function (w) { w.classList.remove('agentchg'); var b = $('.acbar', w); b && b.remove(); }); pend = 0; updPend(); }
      else if (a.dataset.act === 'ownerjump') {
        var b2 = a.dataset.b; setActive(b2); if (MOBILE.matches) { $('.wb').dataset.tab = 'inspector'; $$('.mtabs button').forEach(function (x) { x.setAttribute('aria-selected', x.dataset.tab === 'inspector'); }); }
        var cs = $('#sec-comments'); if (cs.hidden) $('[aria-controls="sec-comments"]').click();
        var c = $('#thr-list .thr[data-block="' + b2 + '"]'); if (c) { c.scrollIntoView({ block: 'nearest' }); c.focus(); } else { composing = { block: b2 }; }
      } else if (a.dataset.act === 'openpub') $('#pub').showModal();
      else if (a.dataset.act === 'closepub') $('#pub').close();
      else if (a.dataset.act === 'dopub') { $('#pub').close(); toast('Revision 5 pushed to the publish server (mock). Prof. Okafor sees it on the next page load.'); $('#pubstate').innerHTML = 'Published revision 5, just now'; }
      else if (a.dataset.act === 'ins') toast('Inserted at the cursor (mock): ' + a.dataset.what);
    });
    // wikilink hint
    var pub = $('#pub'); if (pub) pub.addEventListener('click', function (e) { if (e.target === pub) pub.close(); });
    var ack = $('#ack'); if (ack) ack.onchange = function () { $('[data-act="dopub"]').disabled = !ack.checked; };
    if (STATE === 'preflight' && pub) pub.showModal();
  }

  document.addEventListener('DOMContentLoaded', function () {
    var kind = document.body.dataset.kind;
    if (kind === 'reader') initReader(); else if (kind === 'owner') initOwner();
    icons(document);
  });
  window.__vq = { ic: ic };
})();
