'use strict';

// Dashboard for update-checker. No build step, no dependencies: it reads
// /api/results and /api/status. Results fold into one group per dependency
// (image repository or chart); groups that need action come first and
// everything up to date sits in a collapsed section below them.

// Each category carries its project's official logo (logos/README.md).
const CATS = [
  { id: 'image', label: 'Container images', logo: 'kubernetes.svg', kind: 'Kubernetes manifest', blurb: 'tags referenced by workload pod specs' },
  { id: 'dockerfile', label: 'Dockerfiles', logo: 'docker.svg', kind: 'Dockerfile', blurb: 'base images pinned in FROM lines' },
  { id: 'helm', label: 'Helm chart dependencies', logo: 'helm.svg', kind: 'Helm chart', blurb: 'Chart.yaml dependencies' },
  { id: 'fluxcd', label: 'FluxCD chart releases', logo: 'flux.svg', kind: 'Flux HelmRelease', blurb: 'HelmRelease chart versions' },
];

const KIND_ICON = {
  semver: '\uf02b', calver: '\uf073', date: '\uf073', build: '\uf0ad',
  floating: '\uf0e4', opaque: '\uf05e', unknown: '\uf128',
};
const KIND_HELP = {
  semver: 'version-shaped tag, compared by version',
  calver: 'date-shaped version (year.month), compared as a version',
  date: 'build date used as the tag, compared by date',
  build: 'build counter, compared by its number within the same variant',
  floating: 'mutable tag; the real version was read from image metadata',
  opaque: 'no version in the tag; candidates ranked by build/push date',
  unknown: 'tag could not be classified',
};

// A result's state, most urgent first: this is also the attention order.
const STATES = ['error', 'major', 'drift', 'skipped', 'minor', 'patch', 'update', 'ok'];
const MARK = {
  error: '\uf071', major: '\uf062', drift: '\uf1b8', skipped: '\uf05e',
  minor: '\uf062', patch: '\uf062', update: '\uf062', ok: '\uf00c', idle: '\uf111',
};
const TILES = {
  updates: (r) => r.update_available,
  major: (r) => r.update_available && r.scope === 'major',
  skipped: (r) => (r.skipped || []).length > 0,
  errors: (r) => !!r.check_error,
  drift: (r) => r.digest_changed,
  current: (r) => stateOf(r) === 'ok',
};
// Failure kinds (internal/errkind): a short label and what to check. Every kind
// the server can send needs an entry; anything unknown falls back to "other".
const ERROR_KINDS = {
  auth: ['auth failed', 'the registry or git host refused the request — check the credential (token / Secret), or whether the repo or image is private'],
  not_found: ['not found', 'the repository, chart, tag or image does not exist where the manifest says — renamed, deleted or a typo'],
  unreachable: ['unreachable', 'no answer from the host — DNS, egress / NetworkPolicy, proxy, TLS, or the host is down'],
  timeout: ['timed out', 'the host did not answer in time — usually transient; very large repositories may need a higher scan_timeout'],
  rate_limited: ['rate limited', 'the registry throttled anonymous requests — it clears on its own; try again later'],
  server_error: ['registry error', 'the registry answered with a server error (5xx) — usually transient'],
  host_key: ['host key', 'the ssh host key could not be verified — check scanner.ssh.knownHosts / auth.known_hosts_file'],
  invalid_version: ['bad version', 'the pinned version does not parse, or no published version matches the range'],
  too_large: ['too large', 'the registry response exceeded the size cap'],
  unsupported: ['unsupported', 'a protocol or reference form the checker cannot handle'],
  other: ['lookup failed', ''],
};
const errLabel = (kind) => (ERROR_KINDS[kind] || ERROR_KINDS.other)[0];
const errHint = (kind) => (ERROR_KINDS[kind] || ERROR_KINDS.other)[1];
const POLL_SCANNING = 3000;
const POLL_IDLE = 45000;
const STORE_KEY = 'update-checker:ui';

let results = [];
let status = {};
let scanningTimer = null;
const filters = { q: '', cat: '', scope: '', tile: '' };
const prefs = { view: 'attention', showCurrent: false, ...loadPrefs() };

const $ = (id) => document.getElementById(id);
const esc = (v) => String(v == null ? '' : v).replace(/[&<>"']/g, (c) => (
  { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const ic = (glyph, cls) => `<span class="nf${cls ? ' ' + cls : ''}" aria-hidden="true">${glyph}</span>`;
const chip = (text, cls, title, glyph) =>
  `<span class="chip${cls ? ' ' + cls : ''}"${title ? ` title="${esc(title)}"` : ''}>${glyph ? ic(glyph) : ''}${esc(text)}</span>`;
const plural = (n, word) => `${n} ${word}${n === 1 ? '' : 's'}`;

async function init() {
  $('scan-btn').addEventListener('click', () => triggerScan());
  $('q').addEventListener('input', (e) => { filters.q = e.target.value.trim().toLowerCase(); render(); });
  $('f-scope').addEventListener('change', (e) => { filters.scope = e.target.value; render(); });
  bindSegmented('f-cat', (v) => { filters.cat = v; render(); });
  bindSegmented('f-view', (v) => { prefs.view = v; savePrefs(); render(); }, prefs.view);
  $('stats').addEventListener('click', (e) => {
    const tile = e.target.closest('.stat');
    if (!tile) return;
    filters.tile = filters.tile === tile.dataset.tile ? '' : tile.dataset.tile;
    render();
  });
  $('groups').addEventListener('click', onGroupsClick);
  window.addEventListener('hashchange', render);

  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape' && openKey()) { setOpen(''); return; }
    if (e.metaKey || e.ctrlKey || /^(INPUT|SELECT|TEXTAREA)$/.test(e.target.tagName)) return;
    if (e.key === '/') { e.preventDefault(); $('q').focus(); }
    if (e.key === 'r' && status.trigger_available) { e.preventDefault(); triggerScan(); }
  });

  await refreshStatus();
  await loadResults();
  setInterval(pollIdle, POLL_IDLE);
}

function bindSegmented(id, onPick, initial) {
  const group = $(id);
  const press = (btn) => group.querySelectorAll('.seg').forEach((b) => b.setAttribute('aria-pressed', String(b === btn)));
  if (initial !== undefined) {
    const btn = group.querySelector(`.seg[data-v="${initial}"]`);
    if (btn) press(btn);
  }
  group.addEventListener('click', (e) => {
    const btn = e.target.closest('.seg');
    if (!btn) return;
    press(btn);
    onPick(btn.dataset.v);
  });
}

// Per-viewer conveniences only; the page works the same without storage.
function loadPrefs() {
  try { return JSON.parse(localStorage.getItem(STORE_KEY)) || {}; } catch { return {}; }
}
function savePrefs() {
  try { localStorage.setItem(STORE_KEY, JSON.stringify(prefs)); } catch { /* storage unavailable */ }
}

// ---------- data ----------

async function api(url, method) {
  const res = await fetch(url, method ? { method } : undefined);
  if (!res.ok) throw new Error(`${url}: HTTP ${res.status}`);
  return res.json();
}

async function loadResults() {
  try {
    results = (await api('/api/results')) || [];
    render();
  } catch (err) {
    toast('err', `could not load results: ${err.message}`);
  }
}

async function refreshStatus() {
  try {
    status = (await api('/api/status')) || {};
  } catch {
    setChip('is-error', 'server unreachable', MARK.error);
    return status;
  }
  applyStatus(status);
  return status;
}

function applyStatus(st) {
  $('version-text').textContent = st.version || 'dev';
  $('foot-version').textContent = st.version || 'dev';
  $('scan-btn').classList.toggle('hidden', !st.trigger_available);

  if (st.scanning) {
    setChip('is-scanning', 'scanning', '\uf021');
    startScanPoll();
  } else if (st.last_error) {
    setChip('is-error', 'last scan failed', MARK.error);
  } else {
    setChip('', 'idle', MARK.idle);
  }

  $('last-scan-text').textContent = st.last_scan_at ? relTime(st.last_scan_at) : 'never';

  const banner = $('scan-error');
  banner.classList.toggle('hidden', !st.last_error);
  if (st.last_error) {
    const text = $('scan-error-text');
    // Per repository when the scanner says which ones failed: "repo — label
    // (what to check)"; the raw messages stay available on hover.
    const repos = st.repo_errors || [];
    text.textContent = repos.length
      ? `last scan could not sync ${plural(repos.length, 'repo')}: ${repos.map((f) => {
        const hint = errHint(f.kind);
        return `${f.repo} — ${errLabel(f.kind)}${hint ? ` (${hint})` : ''}`;
      }).join(' · ')}`
      : `last scan ${st.last_error_kind && st.last_error_kind !== 'other' ? `failed (${errLabel(st.last_error_kind)})` : 'reported'}: ${st.last_error}`;
    text.title = st.last_error;
  }
  renderFooter();
}

async function triggerScan() {
  const btn = $('scan-btn');
  btn.disabled = true;
  try {
    await api('/api/scan/trigger', 'POST');
    toast('ok', 'scan triggered');
    setChip('is-scanning', 'scanning', '\uf021');
    startScanPoll();
  } catch (err) {
    toast('err', `trigger failed: ${err.message}`);
  } finally {
    btn.disabled = false;
  }
}

function setChip(cls, text, glyph) {
  $('status-chip').className = `pill${cls ? ' ' + cls : ''}`;
  $('status-text').textContent = text;
  $('status-icon').textContent = glyph;
}

function startScanPoll() {
  if (scanningTimer) return;
  scanningTimer = setInterval(async () => {
    const st = await refreshStatus();
    if (st && !st.scanning) {
      clearInterval(scanningTimer);
      scanningTimer = null;
      await loadResults();
      const upd = results.filter((r) => r.update_available).length;
      const bad = results.filter((r) => r.check_error).length;
      toast(bad ? 'err' : 'ok', `scan done · ${results.length} checks · ${upd} updates${bad ? ` · ${bad} failed` : ''}`);
    }
  }, POLL_SCANNING);
}

async function pollIdle() {
  const before = status.last_scan_at || '';
  const st = await refreshStatus();
  if (st && !st.scanning && (st.last_scan_at || '') !== before) await loadResults();
  renderFooter();
}

// ---------- model ----------

function stateOf(r) {
  if (r.check_error) return 'error';
  if (r.update_available && r.scope === 'major') return 'major';
  if (r.digest_changed) return 'drift';
  if (r.update_available) return ['minor', 'patch'].includes(r.scope) ? r.scope : 'update';
  if ((r.skipped || []).length) return 'skipped';
  return 'ok';
}
const rankOf = (state) => STATES.indexOf(state);
const worst = (states) => states.reduce((a, b) => (rankOf(b) < rankOf(a) ? b : a), 'ok');

function matches(r) {
  if (filters.cat && r.type !== filters.cat) return false;
  if (filters.scope && (r.scope || 'unknown') !== filters.scope) return false;
  if (filters.tile && !TILES[filters.tile](r)) return false;
  if (!filters.q) return true;
  return [r.dependency, r.source, r.chart, r.path, r.current_version, r.latest_version, r.current_resolved,
    r.tag_kind, ...(r.skipped || []).map((s) => s.tag)]
    .join(' ').toLowerCase()
    .includes(filters.q);
}

// One group per dependency; inside it one line per distinct current tag, each
// carrying every workload that uses that tag.
function buildGroups(rows) {
  const groups = new Map();
  for (const r of rows) {
    const key = `${r.type}|${r.dependency}`;
    if (!groups.has(key)) groups.set(key, { key, type: r.type, dependency: r.dependency, lines: new Map() });
    const lines = groups.get(key).lines;
    const tag = r.current_version || '';
    if (!lines.has(tag)) lines.set(tag, []);
    lines.get(tag).push(r);
  }
  return [...groups.values()].map((g) => {
    const lines = [...g.lines.values()].map((rs) => {
      rs.sort((a, b) => rankOf(stateOf(a)) - rankOf(stateOf(b)));
      return { lead: rs[0], users: rs, state: stateOf(rs[0]) };
    }).sort((a, b) => rankOf(a.state) - rankOf(b.state) || String(a.lead.current_version).localeCompare(String(b.lead.current_version)));
    return { ...g, lines, state: worst(lines.map((l) => l.state)) };
  }).sort((a, b) => rankOf(a.state) - rankOf(b.state) || String(a.dependency).localeCompare(String(b.dependency)));
}

// ---------- summary ----------

function updateStats() {
  const count = (fn) => results.filter(fn).length;
  $('s-total').textContent = results.length;
  for (const [tile, fn] of Object.entries(TILES)) {
    $(tile === 'current' ? 's-ok' : `s-${tile}`).textContent = count(fn);
  }
  document.querySelectorAll('#stats .stat').forEach((b) => {
    b.setAttribute('aria-pressed', String(b.dataset.tile === filters.tile && filters.tile !== ''));
  });
}

function renderFooter() {
  const latest = results.reduce((acc, r) => (r.checked_at && r.checked_at > acc ? r.checked_at : acc), '');
  $('foot-checked').textContent = latest ? relTime(latest) : 'never';
  $('foot-refresh').textContent = `${Math.round(POLL_IDLE / 1000)}s`;
}

// ---------- rendering ----------

function render() {
  updateStats();
  renderFooter();
  const rows = results.filter(matches);
  const groups = buildGroups(rows);
  $('shown-count').textContent = (rows.length === results.length
    ? `${results.length} checks` : `${rows.length} of ${results.length} checks`)
    + ` · ${groups.length} ${groups.length === 1 ? 'dependency' : 'dependencies'}`;

  if (!groups.length) {
    $('groups').innerHTML = results.length
      ? `<div class="empty">${ic('\uf05e')}nothing matches the current filters</div>`
      : `<div class="empty">${ic('\uf017')}no scan results yet</div>`;
    return;
  }
  $('groups').innerHTML = prefs.view === 'category' ? renderByCategory(groups) : renderByAttention(groups);
}

function renderByAttention(groups) {
  const attention = groups.filter((g) => g.state !== 'ok');
  const current = groups.filter((g) => g.state === 'ok');
  const checks = (gs) => gs.reduce((n, g) => n + g.lines.reduce((m, l) => m + l.users.length, 0), 0);

  const head = attention.length
    ? lane('attention', `${ic('\uf0e7')}Needs attention`, attention.length, laneLegend(attention), groupList(attention))
    : `<div class="lane-clear">${ic('\uf00c', 'ok')}nothing needs attention — ${plural(checks(current), 'check')} up to date</div>`;
  if (!current.length) return head;

  const open = prefs.showCurrent || !attention.length;
  return head + `<section class="lane is-current">
  <button class="lane-head lane-toggle" type="button" data-toggle="current" aria-expanded="${open}">
    ${ic(open ? '\uf078' : '\uf054', 'caret')}<span class="lane-title">${ic('\uf00c', 'ok')}Up to date</span>
    <span class="lane-n">${current.length}</span><span class="lane-legend">${plural(checks(current), 'check')}</span>
  </button>
  ${open ? groupList(current) : ''}
</section>`;
}

function renderByCategory(groups) {
  const order = (id) => { const i = CATS.findIndex((c) => c.id === id); return i < 0 ? 99 : i; };
  const types = [...new Set(groups.map((g) => g.type))].sort((a, b) => order(a) - order(b));
  return types.map((type) => {
    const meta = CATS.find((c) => c.id === type) || { label: type || 'Uncategorised', blurb: '' };
    const gs = groups.filter((g) => g.type === type);
    return lane(type, `${logo(type, true) || ic('\uf128')}${esc(meta.label)}`, gs.length, `${esc(meta.blurb)} ${laneLegend(gs)}`, groupList(gs));
  }).join('');
}

function lane(id, title, n, legend, body) {
  return `<section class="lane is-${esc(id)}">
  <div class="lane-head"><span class="lane-title">${title}</span><span class="lane-n">${n}</span><span class="lane-legend">${legend}</span></div>
  ${body}
</section>`;
}

function laneLegend(groups) {
  const counts = {};
  for (const g of groups) counts[g.state] = (counts[g.state] || 0) + 1;
  return STATES.filter((s) => counts[s] && s !== 'ok').map((s) => `<span class="dot is-${s}">${counts[s]} ${s === 'update' ? 'other' : s}</span>`).join('');
}

const groupList = (groups) => `<ul class="groups">${groups.map(renderGroup).join('')}</ul>`;

function renderGroup(g) {
  const open = openKey() === g.key;
  const single = g.lines.length === 1;
  const users = g.lines.flatMap((l) => l.users);
  const behind = g.lines.filter((l) => l.lead.update_available).length;
  const skipped = users.reduce((n, r) => Math.max(n, (r.skipped || []).length), 0);

  const chips = [];
  const scope = g.lines.map((l) => l.lead.scope).find(Boolean);
  if (scope && scope !== 'unknown') chips.push(chip(scope, `scope ${scope}`, 'size of the largest available update'));
  if (skipped) chips.push(chip(`${skipped} skipped`, 'skipped', 'tags that looked newer but were rejected as strays — open for the reasons', '\uf05e'));
  if (users.some((r) => r.digest_changed)) chips.push(chip('digest moved', 'drift', 'the tag points at a different image than in the previous scan', '\uf1b8'));
  // A single tag shows its own "lookup failed" next to the version.
  const kinds = [...new Set(users.filter((r) => r.check_error).map((r) => r.check_error_kind || 'other'))];
  if (!single && kinds.length) chips.push(chip(kinds.length === 1 ? errLabel(kinds[0]) : 'lookup failed', 'err', kinds.map(errLabel).join(', '), '\uf071'));

  const summary = single
    ? lineVersion(g.lines[0])
    : `<span class="grp-multi">${plural(g.lines.length, 'tag')}${behind ? ` · <b>${behind} behind</b>` : ' · all current'}</span>`;

  return `<li class="grp is-${g.state}${open ? ' is-open' : ''}">
  <button class="grp-head" type="button" data-key="${esc(g.key)}" aria-expanded="${open}">
    <span class="grp-mark">${ic(MARK[g.state])}</span>
    <span class="grp-id">
      <span class="grp-ref">${logo(g.type)}${refHTML(g)}</span>
      <span class="grp-sub">${usedBy(users)}</span>
    </span>
    <span class="grp-ver">${summary}</span>
    <span class="grp-chips">${chips.join('')}</span>
    ${ic(open ? '\uf078' : '\uf054', 'caret')}
  </button>
  ${single ? '' : `<ul class="lines">${g.lines.map(renderLine).join('')}</ul>`}
  ${open ? renderDrawer(g) : ''}
</li>`;
}

function renderLine(l) {
  return `<li class="line is-${l.state}">
  <span class="line-ver">${lineVersion(l)}</span>
  <span class="line-users">${usedBy(l.users)}</span>
</li>`;
}

function lineVersion(l) {
  const r = l.lead;
  if (r.check_error) return `<span class="ver err">${esc(r.current_version || 'unknown')}</span>${chip(errLabel(r.check_error_kind), 'err', r.check_error, '\uf071')}`;
  if (!r.update_available) {
    return `<span class="ver cur">${esc(r.current_version)}</span>${ic('\uf00c', 'ok')}<span class="ver same">current</span>`;
  }
  const dist = [distance(r), r.latest_pushed_at ? `pushed ${relTime(r.latest_pushed_at)}` : ''].filter(Boolean).join(' · ');
  return `<span class="ver cur">${esc(r.current_version)}</span>${ic('\uf178', 'arrow')}`
    + `<span class="ver new">${diffHTML(r.current_version, r.latest_version, r.scope)}</span>`
    + (dist ? `<span class="dist">${esc(dist)}</span>` : '');
}

function usedBy(users) {
  const names = [...new Set(users.map((r) => r.chart).filter(Boolean))];
  const shown = names.slice(0, 3).map(esc).join(', ');
  const more = names.length > 3 ? ` +${names.length - 3}` : '';
  const sources = [...new Set(users.map((r) => r.source))];
  return `${shown}${more}${sources.length ? ` <span class="src">· ${esc(sources.join(', '))}</span>` : ''}`;
}

// Highlight from the first token that differs: v1.9.1 -> v1.[10.0].
function diffHTML(cur, latest, scope) {
  const a = String(cur).split(/(\d+)/);
  const b = String(latest).split(/(\d+)/);
  let i = 0;
  while (i < b.length && a[i] === b[i]) i++;
  if (i >= b.length) return esc(latest);
  return esc(b.slice(0, i).join('')) + `<mark class="is-${esc(scope || 'update')}">${esc(b.slice(i).join(''))}</mark>`;
}

// How far behind: "+436 builds" for build counters, "+1 minor" for versions.
function distance(r) {
  if (r.tag_kind === 'build') {
    const last = (v) => Number((String(v).match(/(\d+)\D*$/) || [])[1]);
    const d = last(r.latest_version) - last(r.current_version);
    return d > 0 ? `+${plural(d, 'build')}` : '';
  }
  if (r.tag_kind !== 'semver') return '';
  const core = (v) => ((String(v).match(/\d+(?:\.\d+)*/) || [''])[0]).split('.').map(Number);
  const a = core(r.current_version);
  const b = core(r.latest_version);
  for (let i = 0; i < Math.max(a.length, b.length); i++) {
    const d = (b[i] || 0) - (a[i] || 0);
    if (d) return d > 0 ? `+${d} ${['major', 'minor', 'patch'][i] || 'patch'}` : '';
  }
  return '';
}

// ---------- drawer ----------

function renderDrawer(g) {
  return `<div class="drawer" role="region" aria-label="details for ${esc(g.dependency)}">
  ${g.lines.map(drawerLine).join('')}
</div>`;
}

function drawerLine(l) {
  const r = l.lead;
  const cands = (r.skipped || []).map((s) => `<div class="cand is-skipped">
    <div class="cand-head">${ic('\uf05e')}<span class="ver">${esc(s.tag)}</span><span class="cand-verdict">skipped</span></div>
    <ul class="signals">${(s.reasons || []).map((why) => `<li>${ic('\uf00d')}${esc(why)}</li>`).join('')}</ul>
  </div>`);
  if (r.update_available) {
    cands.push(`<div class="cand is-accepted"><div class="cand-head">${ic('\uf00c')}<span class="ver new">${esc(r.latest_version)}</span>`
      + `<span class="cand-verdict">offered${r.latest_pushed_at ? ` · pushed ${esc(day(r.latest_pushed_at))}` : ''}</span></div></div>`);
  }
  cands.push(`<div class="cand is-current"><div class="cand-head">${ic('\uf111')}<span class="ver cur">${esc(r.current_version || '(no tag)')}</span>`
    + `<span class="cand-verdict">in use${r.current_pushed_at ? ` · pushed ${esc(day(r.current_pushed_at))}` : ''}</span></div></div>`);

  const facts = [
    r.image_digest ? fact('Digest', `<code>${esc(shortDigest(r.image_digest))}…</code> <button class="copy" type="button" data-copy="${esc(r.image_digest)}" title="copy the full digest">${ic('\uf0c5')}copy</button>`) : '',
    r.tag_kind ? fact('Kind', `${ic(KIND_ICON[r.tag_kind] || KIND_ICON.unknown)} ${esc(r.tag_kind)} <span class="muted">— ${esc(KIND_HELP[r.tag_kind] || '')}</span>`) : '',
    r.current_resolved && r.current_resolved !== r.current_version ? fact('Serves', esc(r.current_resolved)) : '',
    r.scope ? fact('Scope', esc(r.scope)) : '',
    r.final_stage ? fact('Stage', `final <span class="muted">— the image this build publishes</span>`) : '',
    !r.tag_kind && r.protocol ? fact('Source', esc(r.protocol)) : '',
  ].join('');

  const notes = [
    r.check_error ? `<p class="note is-error">${ic('\uf071')}<b>${esc(errLabel(r.check_error_kind))}</b> · ${esc(r.check_error)}</p>` : '',
    r.check_error && errHint(r.check_error_kind) ? `<p class="note">${ic('\uf05a')}${esc(errHint(r.check_error_kind))}</p>` : '',
    otherNote(r) ? `<p class="note">${ic('\uf05a')}${esc(otherNote(r))}</p>` : '',
    l.users.some((u) => u.digest_changed) ? `<p class="note is-drift">${ic('\uf1b8')}the tag points at a different image than in the previous scan</p>` : '',
  ].join('');

  return `<section class="dl is-${l.state}">
  <h4 class="dl-title">${ic(MARK[l.state])}${esc(r.current_version || '(no tag)')}${r.update_available ? ` ${ic('\uf178', 'arrow')} ${esc(r.latest_version)}` : ''}</h4>
  ${(r.skipped || []).length || r.update_available ? `<div class="cands"><div class="dl-label">Candidates checked, newest first</div>${cands.join('')}</div>` : ''}
  ${notes}
  <dl class="facts">${facts}</dl>
  <div class="dl-label">Used by</div>
  <ul class="users">${l.users.map((u) => `<li><span class="user-name">${esc(u.chart || '—')}</span>`
    + `<span class="muted">${esc(u.source)}${u.path ? ` · ${esc(u.path)}` : ''}</span></li>`).join('')}</ul>
</section>`;
}

const fact = (k, v) => `<dt>${k}</dt><dd>${v}</dd>`;

// logo is the category's project logo; decorative next to its own label,
// otherwise it names the kind of file the row came from.
function logo(type, decorative) {
  const cat = CATS.find((c) => c.id === type);
  if (!cat) return '';
  const label = decorative ? 'alt="" aria-hidden="true"' : `alt="${esc(cat.kind)}" title="${esc(cat.kind)}"`;
  return `<img class="logo" src="/assets/logos/${cat.logo}" ${label}>`;
}

// The note repeats each skipped tag as "skipped <tag>: <reasons>"; the drawer
// already lists those, so only the rest of the note is worth showing.
function otherNote(r) {
  let note = r.note || '';
  for (const s of r.skipped || []) {
    note = note.replace(`skipped ${s.tag}: ${(s.reasons || []).join('; ')}`, '');
  }
  return note.split('; ').map((p) => p.trim()).filter(Boolean).join('; ');
}

// ---------- interaction ----------

// The open group lives in the URL hash so a link opens straight to it.
function openKey() {
  const m = location.hash.match(/^#dep=(.+)$/);
  if (!m) return '';
  try { return decodeURIComponent(m[1]); } catch { return ''; }
}

function setOpen(key) {
  if (key) {
    location.hash = `dep=${encodeURIComponent(key)}`;
  } else {
    history.replaceState(null, '', location.pathname + location.search);
    render();
  }
}

async function onGroupsClick(e) {
  const copy = e.target.closest('.copy');
  if (copy) {
    try {
      await navigator.clipboard.writeText(copy.dataset.copy);
      toast('ok', 'digest copied');
    } catch {
      toast('err', 'clipboard unavailable');
    }
    return;
  }
  const toggle = e.target.closest('[data-toggle="current"]');
  if (toggle) {
    prefs.showCurrent = toggle.getAttribute('aria-expanded') !== 'true';
    savePrefs();
    render();
    return;
  }
  const head = e.target.closest('.grp-head');
  if (head) setOpen(openKey() === head.dataset.key ? '' : head.dataset.key);
}

// ---------- helpers ----------

// Images keep the registry host muted so the image name reads first.
function refHTML(g) {
  const dep = String(g.dependency);
  const at = dep.indexOf('/');
  if (g.type !== 'image' || at <= 0 || !dep.slice(0, at).includes('.')) return esc(dep);
  return `<span class="host">${esc(dep.slice(0, at))}/</span>${esc(dep.slice(at + 1))}`;
}

function shortDigest(d) {
  return String(d).replace(/^(sha256|sha512):/, '').slice(0, 12);
}

function day(iso) {
  const t = new Date(iso);
  return Number.isFinite(t.getTime()) ? t.toISOString().slice(0, 10) : iso;
}

function relTime(iso) {
  const t = new Date(iso).getTime();
  if (!Number.isFinite(t)) return iso;
  const s = Math.max(0, (Date.now() - t) / 1000);
  if (s < 45) return 'just now';
  if (s < 3600) return `${Math.round(s / 60)}m ago`;
  if (s < 172800) return `${Math.round(s / 3600)}h ago`;
  return `${Math.round(s / 86400)}d ago`;
}

function toast(kind, msg) {
  const el = document.createElement('div');
  el.className = `toast${kind === 'err' ? ' err' : kind === 'ok' ? ' ok' : ''}`;
  el.innerHTML = `${ic(kind === 'err' ? '\uf071' : '\uf00c')}${esc(msg)}`;
  $('toasts').appendChild(el);
  setTimeout(() => el.remove(), 5000);
}

init();
