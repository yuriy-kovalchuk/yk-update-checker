'use strict';

// Dashboard for update-checker. No build step, no dependencies: it reads
// /api/results and /api/status. Results are tables — one row per dependency, ordered by
// what needs action — inside cards, and each dependency opens a details row holding the
// candidates, the facts and the workloads that use it. The console shell (app bar, nav
// drawer, cards, chips) lives in app.css; this file builds markup and wires controls.

// Each category carries its project's official logo (logos/README.md).
const CATS = [
  { id: 'image', label: 'Container images', logo: 'kubernetes.svg', kind: 'Kubernetes manifest', blurb: 'tags referenced by workload pod specs' },
  { id: 'dockerfile', label: 'Dockerfiles', logo: 'docker.svg', kind: 'Dockerfile', blurb: 'base images pinned in FROM lines' },
  { id: 'helm', label: 'Helm charts', logo: 'helm.svg', kind: 'Helm chart', blurb: 'pinned in Chart.yaml' },
  { id: 'fluxcd', label: 'FluxCD releases', logo: 'flux.svg', kind: 'Flux HelmRelease', blurb: 'HelmRelease chart versions' },
];

// Material Icons (Apache 2.0, google/material-design-icons, 24px grid). Every glyph comes
// from this table: index.html names its chrome icons in a data-icon attribute and
// hydrateIcons() fills them, so a path exists in exactly one place and follows the theme.
// TestDashboardIconsResolve fails on a name used but missing, or defined but unused.
const ICONS = {
  arrow_forward: '<path d="m12 4-1.41 1.41L16.17 11H4v2h12.17l-5.58 5.59L12 20l8-8z"/>',
  arrow_upward: '<path d="m4 12 1.41 1.41L11 7.83V20h2V7.83l5.58 5.59L20 12l-8-8-8 8z"/>',
  block: '<path d="M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zM4 12c0-4.42 3.58-8 8-8 1.85 0 3.55.63 4.9 1.69L5.69 16.9A7.902 7.902 0 0 1 4 12zm8 8c-1.85 0-3.55-.63-4.9-1.69L18.31 7.1A7.902 7.902 0 0 1 20 12c0 4.42-3.58 8-8 8z"/>',
  bolt: '<path d="M11 21h-1l1-7H7.5c-.58 0-.57-.32-.38-.66.19-.34.05-.08.07-.12C8.48 10.94 10.42 7.54 13 3h1l-1 7h3.5c.49 0 .56.33.47.51l-.07.15C12.96 17.55 11 21 11 21z"/>',
  build: '<path d="m22.7 19-9.1-9.1c.9-2.3.4-5-1.5-6.9-2-2-5-2.4-7.4-1.3L9 6 6 9 1.6 4.7C.4 7.1.9 10.1 2.9 12.1c1.9 1.9 4.6 2.4 6.9 1.5l9.1 9.1c.4.4 1 .4 1.4 0l2.3-2.3c.5-.4.5-1.1.1-1.4z"/>',
  calendar_today: '<path d="M20 3h-1V1h-2v2H7V1H5v2H4c-1.1 0-2 .9-2 2v16c0 1.1.9 2 2 2h16c1.1 0 2-.9 2-2V5c0-1.1-.9-2-2-2zm0 18H4V8h16v13z"/>',
  category: '<path d="m12 2-5.5 9h11z"/><path d="M3 13.5h8v8H3z"/>',
  check: '<path d="M9 16.17 4.83 12l-1.42 1.41L9 19 21 7l-1.41-1.41z"/>',
  check_circle: '<path d="M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm-2 15-5-5 1.41-1.41L10 14.17l7.59-7.59L19 8l-9 9z"/>',
  chevron_right: '<path d="M10 6 8.59 7.41 13.17 12l-4.58 4.59L10 18l6-6z"/>',
  close: '<path d="M19 6.41 17.59 5 12 10.59 6.41 5 5 6.41 10.59 12 5 17.59 6.41 19 12 13.41 17.59 19 19 17.59 13.41 12z"/>',
  compare_arrows: '<path d="M9.01 14H2v2h7.01v3L13 15l-3.99-4v3zm5.98-1v-3H22V8h-7.01V5L11 9l3.99 4z"/>',
  content_copy: '<path d="M16 1H4c-1.1 0-2 .9-2 2v14h2V3h12V1zm3 4H8c-1.1 0-2 .9-2 2v14c0 1.1.9 2 2 2h11c1.1 0 2-.9 2-2V7c0-1.1-.9-2-2-2zm0 16H8V7h11v14z"/>',
  dashboard: '<path d="M3 13h8V3H3v10zm0 8h8v-6H3v6zm10 0h8V11h-8v10zm0-18v6h8V3h-8z"/>',
  done_all: '<path d="m18 7-1.41-1.41-6.34 6.34 1.41 1.41L18 7zm4.24-1.41L11.66 16.17 7.48 12l-1.41 1.41L11.66 19l12-12-1.42-1.41zM.41 13.41 6 19l1.41-1.41L1.83 12 .41 13.41z"/>',
  error: '<path d="M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm1 15h-2v-2h2v2zm0-4h-2V7h2v6z"/>',
  expand_more: '<path d="M16.59 8.59 12 13.17 7.41 8.59 6 10l6 6 6-6z"/>',
  help_outline: '<path d="M11 18h2v-2h-2v2zm1-16C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm0 18c-4.41 0-8-3.59-8-8s3.59-8 8-8 8 3.59 8 8-3.59 8-8 8zm0-14c-2.21 0-4 1.79-4 4h2c0-1.1.9-2 2-2s2 .9 2 2c0 2-3 1.75-3 5h2c0-2.25 3-2.5 3-5 0-2.21-1.79-4-4-4z"/>',
  info: '<path d="M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm1 15h-2v-6h2v6zm0-8h-2V7h2v2z"/>',
  inventory: '<path d="M20 2H4c-1 0-2 .9-2 2v3.01c0 .72.43 1.34 1 1.69V20c0 1.1 1.1 2 2 2h14c.9 0 2-.9 2-2V8.7c.57-.35 1-.97 1-1.69V4c0-1.1-1-2-2-2zm-5 12H9v-2h6v2zm5-7H4V4h16v3z"/>',
  label: '<path d="M17.63 5.84C17.27 5.33 16.67 5 16 5L5 5.01C3.9 5.01 3 5.9 3 7v10c0 1.1.9 1.99 2 1.99L16 19c.67 0 1.27-.33 1.63-.84L22 12l-4.37-6.16z"/>',
  refresh: '<path d="M17.65 6.35A7.958 7.958 0 0 0 12 4c-4.42 0-7.99 3.58-7.99 8s3.57 8 7.99 8c3.73 0 6.84-2.55 7.73-6h-2.08A5.99 5.99 0 0 1 12 18c-3.31 0-6-2.69-6-6s2.69-6 6-6c1.66 0 3.14.69 4.22 1.78L13 11h7V4l-2.35 2.35z"/>',
  schedule: '<path d="M11.99 2C6.47 2 2 6.48 2 12s4.47 10 9.99 10C17.52 22 22 17.52 22 12S17.52 2 11.99 2zM12 20c-4.42 0-8-3.58-8-8s3.58-8 8-8 8 3.58 8 8-3.58 8-8 8z"/><path d="M12.5 7H11v6l5.25 3.15.75-1.23-4.5-2.67z"/>',
  search: '<path d="M15.5 14h-.79l-.28-.27A6.471 6.471 0 0 0 16 9.5 6.5 6.5 0 1 0 9.5 16c1.61 0 3.09-.59 4.23-1.57l.27.28v.79l5 4.99L20.49 19l-4.99-5zm-6 0C7.01 14 5 11.99 5 9.5S7.01 5 9.5 5 14 7.01 14 9.5 11.99 14 9.5 14z"/>',
  speed: '<path d="m20.38 8.57-1.23 1.85a8 8 0 0 1-.22 7.58H5.07A8 8 0 0 1 15.58 6.85l1.85-1.23A10 10 0 0 0 3.35 19a2 2 0 0 0 1.72 1h13.85a2 2 0 0 0 1.74-1 10 10 0 0 0-.27-10.44zm-9.79 6.84a2 2 0 0 0 2.83 0l5.66-8.49-8.49 5.66a2 2 0 0 0 0 2.83z"/>',
  timeline: '<path d="M23 8c0 1.1-.9 2-2 2a1.7 1.7 0 0 1-.51-.07l-3.56 3.55c.05.16.07.34.07.52 0 1.1-.9 2-2 2s-2-.9-2-2c0-.18.02-.36.07-.52l-2.55-2.55c-.16.05-.34.07-.52.07s-.36-.02-.52-.07l-4.55 4.56c.05.16.07.33.07.51 0 1.1-.9 2-2 2s-2-.9-2-2 .9-2 2-2c.18 0 .35.02.51.07l4.56-4.55C8.02 9.36 8 9.18 8 9c0-1.1.9-2 2-2s2 .9 2 2c0 .18-.02.36-.07.52l2.55 2.55c.16-.05.34-.07.52-.07s.36.02.52.07l3.55-3.56A1.7 1.7 0 0 1 19 8c0-1.1.9-2 2-2s2 .9 2 2z"/>',
  update: '<path d="M21 10.12h-6.78l2.74-2.82c-2.73-2.7-7.15-2.8-9.88-.1-2.73 2.71-2.73 7.08 0 9.79s7.15 2.71 9.88 0C18.32 15.65 19 14.08 19 12.1h2c0 1.98-.88 4.55-2.64 6.29-3.51 3.48-9.21 3.48-12.72 0-3.5-3.47-3.53-9.11-.02-12.58s9.14-3.47 12.65 0L21 3v7.12zM12.5 8v4.25l3.5 2.08-.72 1.21L11 13V8h1.5z"/>',
};

// A version's shape decides the glyph beside it in the details row.
const KIND_ICON = {
  semver: 'label', calver: 'calendar_today', date: 'calendar_today', build: 'build',
  floating: 'speed', opaque: 'block', unknown: 'help_outline',
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

// A result's state, most urgent first: it drives the row order, the dot colour and the
// card legend.
const STATES = ['error', 'major', 'drift', 'skipped', 'minor', 'patch', 'update', 'ok'];
const STATE_LABEL = {
  error: 'failed', major: 'major', drift: 'drift', skipped: 'skipped',
  minor: 'minor', patch: 'patch', update: 'update', ok: 'current',
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
// What the "Up to date" card holds, so opening it fills the table without a re-render.
let currentGroups = [];
const filters = { q: '', repo: '', cat: '', scope: '', tile: '' };
const prefs = { view: 'attention', showCurrent: false, ...loadPrefs() };

const $ = (id) => document.getElementById(id);
const esc = (v) => String(v == null ? '' : v).replace(/[&<>"']/g, (c) => (
  { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const ic = (name, cls) => {
  const paths = ICONS[name];
  return paths ? `<svg class="icon${cls ? ' ' + cls : ''}" viewBox="0 0 24 24" aria-hidden="true">${paths}</svg>` : '';
};
const plural = (n, word, many) => `${n} ${n === 1 ? word : many || `${word}s`}`;

async function init() {
  hydrateIcons();
  renderKinds();
  $('scan-btn').addEventListener('click', () => triggerScan());
  $('q').addEventListener('input', (e) => { filters.q = e.target.value.trim().toLowerCase(); render(); });
  $('f-repo').addEventListener('change', (e) => { filters.repo = e.target.value; render(); });
  $('f-scope').addEventListener('change', (e) => { filters.scope = e.target.value; render(); });
  $('f-cat').addEventListener('change', (e) => { filters.cat = e.target.value; render(); });
  for (const link of document.querySelectorAll('.nav-link[data-view]')) {
    link.addEventListener('click', () => { prefs.view = link.dataset.view; savePrefs(); render(); });
  }
  for (const link of document.querySelectorAll('.nav-link[data-cat]')) {
    link.addEventListener('click', () => { filters.cat = link.dataset.cat; render(); });
  }
  $('stats').addEventListener('click', (e) => {
    const tile = e.target.closest('[data-tile]');
    if (!tile) return;
    filters.tile = filters.tile === tile.dataset.tile ? '' : tile.dataset.tile;
    render();
  });
  const tables = $('tables');
  tables.addEventListener('click', onTablesClick);
  // <details> fires toggle on the element itself, so catch it in the capture phase.
  tables.addEventListener('toggle', (e) => onCurrentToggled(e.target), true);
  window.addEventListener('hashchange', render);

  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape' && openKey()) { setOpen(''); return; }
    if (e.metaKey || e.ctrlKey || /^(INPUT|SELECT|TEXTAREA)$/.test(e.target.tagName)) return;
    if (e.key === '/') { e.preventDefault(); $('q').focus(); }
    if (e.key === 'r' && canScan()) { e.preventDefault(); triggerScan(); }
  });

  await refreshStatus();
  await loadResults();
  setInterval(pollIdle, POLL_IDLE);
}

// One link and one picker option per CATS entry, so the kinds are spelled in one place.
function renderKinds() {
  $('kind-links').innerHTML = CATS.map((c) => `<button class="nav-link" type="button" data-cat="${c.id}">`
    + `${logo(c.id, true)}${esc(c.label)}<span class="nav-count" id="c-${c.id}"></span></button>`).join('');
  $('f-cat').innerHTML = '<option value="">all kinds</option>'
    + CATS.map((c) => `<option value="${c.id}">${esc(c.label)}</option>`).join('');
}

// The static chrome names its icons in data-icon so ICONS stays the only source of paths.
function hydrateIcons() {
  for (const el of Array.from(document.querySelectorAll('[data-icon]'))) el.innerHTML = ic(el.dataset.icon);
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
    renderRepoFilter();
    render();
  } catch (err) {
    toast('err', `could not load results: ${err.message}`);
  }
}

async function refreshStatus() {
  try {
    status = (await api('/api/status')) || {};
  } catch {
    setChip('pill-down', 'server unreachable', 'error');
    return status;
  }
  applyStatus(status);
  return status;
}

const canScan = (st = status) => st.trigger_available && !st.scanning;

// The scanner takes a one-scan-at-a-time Lease, so a second trigger would be refused anyway.
function applyScanBtn(st) {
  const btn = $('scan-btn');
  btn.hidden = !st.trigger_available;
  btn.disabled = !canScan(st);
  btn.title = st.scanning ? 'a scan is already running' : 'trigger a scan now (keyboard: r)';
}

function applyStatus(st) {
  $('version-text').textContent = st.version || 'dev';
  $('foot-version').textContent = st.version || 'dev';
  applyScanBtn(st);

  if (st.scanning) setChip('', 'scanning', 'refresh');
  else if (st.last_error) setChip('pill-down', 'last scan failed', 'error');
  else setChip('pill-ok', 'idle');

  $('last-scan-text').textContent = st.last_scan_at ? relTime(st.last_scan_at) : 'never';

  const banner = $('scan-error');
  banner.hidden = !st.last_error;
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
    status.scanning = true; // a scan is running before the next /api/status says so
    toast('ok', 'scan triggered');
    setChip('', 'scanning', 'refresh');
    startScanPoll();
  } catch (err) {
    toast('err', `trigger failed: ${err.message}`);
  } finally {
    applyScanBtn(status);
  }
}

function setChip(cls, text, iconName) {
  const chip = $('status-chip');
  chip.className = `pill${cls ? ' ' + cls : ''}`;
  // A spinning refresh replaces the dot while a scan runs, so the pill still reads at a glance.
  if (iconName) chip.classList.add('is-scanning');
  chip.innerHTML = `${iconName ? ic(iconName) : '<span class="pill-dot"></span>'}<span>${esc(text)}</span>`;
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

// The repo filter scopes the whole view, tiles and drawer counts included; the others
// narrow only the tables.
function renderRepoFilter() {
  const sel = $('f-repo');
  const repos = [...new Set(results.map((r) => r.source).filter(Boolean))].sort();
  if (filters.repo && !repos.includes(filters.repo)) filters.repo = '';
  sel.innerHTML = '<option value="">all repos</option>'
    + repos.map((s) => `<option value="${esc(s)}">${esc(s)}</option>`).join('');
  sel.value = filters.repo;
}

const scoped = () => (filters.repo ? results.filter((r) => r.source === filters.repo) : results);

function matches(r) {
  if (filters.repo && r.source !== filters.repo) return false;
  if (filters.cat && r.type !== filters.cat) return false;
  if (filters.scope && (r.scope || 'unknown') !== filters.scope) return false;
  if (filters.tile && !TILES[filters.tile](r)) return false;
  if (!filters.q) return true;
  return [r.dependency, r.source, r.chart, r.path, r.current_version, r.latest_version, r.current_resolved,
    r.tag_kind, ...(r.skipped || []).map((s) => s.tag)]
    .join(' ').toLowerCase()
    .includes(filters.q);
}

// One group per dependency; inside it one detail block per distinct current tag, each
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

// ---------- summary and filters ----------

function updateStats() {
  const rows = scoped();
  for (const chip of document.querySelectorAll('#stats [data-tile]')) {
    const tile = chip.dataset.tile;
    const n = rows.filter(TILES[tile]).length;
    $(`s-${tile}`).textContent = n;
    chip.hidden = n === 0 && filters.tile !== tile;
    chip.setAttribute('aria-pressed', String(filters.tile === tile));
  }
}

// The drawer restates the same filters the tables use, each with the count it would show: within the
// repository only, because choosing a link replaces everything else.
function updateSidebar() {
  const counted = buildGroups(scoped());
  const byCat = (id) => counted.filter((g) => !id || g.type === id).length;
  $('c-all').textContent = byCat('') || '';
  for (const c of CATS) $(`c-${c.id}`).textContent = byCat(c.id) || '';
  const kinds = $('f-cat');
  for (const opt of kinds.options) {
    const cat = CATS.find((c) => c.id === opt.value);
    if (cat) opt.textContent = `${cat.label} (${byCat(cat.id)})`;
  }
  kinds.value = filters.cat;
  for (const link of document.querySelectorAll('.nav-link[data-cat]')) {
    link.setAttribute('aria-pressed', String(link.dataset.cat === filters.cat));
  }
  for (const link of document.querySelectorAll('.nav-link[data-view]')) {
    link.setAttribute('aria-pressed', String(link.dataset.view === prefs.view));
  }
  const repos = new Set(results.map((r) => r.source).filter(Boolean));
  $('meta-scope').textContent = filters.repo || `all repositories (${repos.size})`;
}

function renderCount(rows, groups) {
  $('shown-count').textContent = (rows.length === results.length
    ? `${results.length} checks` : `${rows.length} of ${results.length} checks`)
    + ` · ${plural(groups.length, 'dependency', 'dependencies')}`;
}

function renderFooter() {
  // The scan time is the scanner's own clock; a stored dependency's timestamp only moves when that reference changes.
  $('foot-checked').textContent = status.last_scan_at ? relTime(status.last_scan_at) : 'never';
  $('foot-refresh').textContent = `${Math.round(POLL_IDLE / 1000)}s`;
}

// ---------- rendering ----------

function render() {
  updateStats();
  renderFooter();
  const rows = results.filter(matches);
  const groups = buildGroups(rows);
  currentGroups = groups.filter((g) => g.state === 'ok');
  updateSidebar();
  renderCount(rows, groups);

  if (!groups.length) {
    $('tables').innerHTML = card('ic:search', 'No results to show', '',
      `<div class="empty">${results.length ? 'nothing matches the current filters.' : 'no scan results yet — start a scan.'}</div>`);
    return;
  }
  $('tables').innerHTML = prefs.view === 'category' ? renderByCategory(groups) : renderByAttention(groups);
}

function renderByAttention(groups) {
  const attention = groups.filter((g) => g.state !== 'ok');

  const head = attention.length
    ? card('ic:bolt', 'Needs attention', plural(attention.length, 'dependency', 'dependencies'), tableHTML(attention))
    : card('ic:done_all', 'Nothing needs attention', '',
      `<div class="empty">All ${plural(checksOf(groups), 'check')} up to date.</div>`);
  return currentGroups.length ? head + currentCard(currentGroups) : head;
}

// Up to date stays reachable but out of the way: a collapsed card that fills itself on open.
function currentCard(current) {
  const open = prefs.showCurrent;
  return `<details class="card" id="card-current"${open ? ' open' : ''}>
  <summary class="card-head"><span class="title">${ic('expand_more', 'caret')}Up to date</span>
    <span class="card-sub">${plural(current.length, 'dependency', 'dependencies')} · ${plural(checksOf(current), 'check')}</span></summary>
  <div data-body>${open ? tableHTML(current) : ''}</div>
</details>`;
}

const checksOf = (groups) => groups.reduce((n, g) => n + g.lines.reduce((m, l) => m + l.users.length, 0), 0);

function renderByCategory(groups) {
  const order = (id) => { const i = CATS.findIndex((c) => c.id === id); return i < 0 ? 99 : i; };
  const types = [...new Set(groups.map((g) => g.type))].sort((a, b) => order(a) - order(b));
  return types.map((type) => {
    const meta = CATS.find((c) => c.id === type) || { label: type || 'Uncategorised', blurb: '' };
    const gs = groups.filter((g) => g.type === type);
    const sub = [meta.blurb, plural(gs.length, 'dependency', 'dependencies')].filter(Boolean).join(' · ');
    return card(logo(type, true) || 'ic:help_outline', meta.label, sub, tableHTML(gs));
  }).join('');
}

// mark is an icon name with an "ic:" prefix, or the logo markup itself.
function card(mark, title, subHTML, body) {
  const glyph = mark.startsWith('ic:') ? ic(mark.slice(3)) : mark;
  return `<section class="card">
  <div class="card-head"><span class="title">${glyph}${esc(title)}</span>${subHTML ? `<span class="card-sub">${subHTML}</span>` : ''}</div>
  ${body}
</section>`;
}

function tableHTML(groups) {
  return `<div class="table-scroll"><table>
  <thead><tr><th class="col-status">Status</th><th>Reference</th><th>Version</th><th class="col-notes">Notes</th><th class="col-caret"></th></tr></thead>
  <tbody>${groups.map(rowHTML).join('')}</tbody>
</table></div>`;
}

function rowHTML(g) {
  const open = openKey() === g.key;
  const users = g.lines.flatMap((l) => l.users);
  const behind = g.lines.filter((l) => l.lead.update_available).length;
  const caret = open ? 'expand_more' : 'chevron_right';
  const summary = g.lines.length === 1
    ? versionsHTML(g.lines[0])
    : `<span class="vers">${plural(g.lines.length, 'pinned tag')}${behind ? ` · ${behind} behind` : ' · all current'}</span>`;

  return `<tr class="grp-row is-${g.state}${open ? ' is-open' : ''}" data-key="${esc(g.key)}">
  <td class="col-status"><span class="status-cell"><span class="dot"></span>${esc(STATE_LABEL[g.state])}</span></td>
  <td>
    <button class="row-link" type="button" aria-expanded="${open}">${logo(g.type)}${refHTML(g)}</button>
    <div class="cell-sub">${usedBy(users)}</div>
  </td>
  <td>${summary}</td>
  <td class="col-notes">${badgesHTML(g, users)}</td>
  <td class="col-caret"><span class="caret">${ic(caret)}</span></td>
</tr>${open ? `<tr class="detail-row is-${g.state}"><td colspan="5">${drawerHTML(g)}</td></tr>` : ''}`;
}

// What the scope, the drift and a failed lookup add to a row.
function badgesHTML(g, users) {
  const badges = [];
  const scope = g.lines.map((l) => l.lead.scope).find(Boolean);
  if (scope && scope !== 'unknown') badges.push(badge(scope, `is-${scope}`, 'size of the largest available update'));
  const skipped = users.reduce((n, r) => Math.max(n, (r.skipped || []).length), 0);
  if (skipped) badges.push(badge(`${skipped} skipped`, 'is-skipped', 'tags that looked newer but were rejected as strays — open for the reasons', 'block'));
  if (users.some((r) => r.digest_changed)) badges.push(badge('digest moved', 'is-drift', 'the tag points at a different image than in the previous scan', 'compare_arrows'));
  const kinds = [...new Set(users.filter((r) => r.check_error).map((r) => r.check_error_kind || 'other'))];
  if (kinds.length) badges.push(badge(kinds.length === 1 ? errLabel(kinds[0]) : 'lookup failed', 'is-error', kinds.map(errLabel).join(', '), 'error'));
  return badges.join('') || '<span class="muted">—</span>';
}

const badge = (text, cls, title, iconName) =>
  `<span class="badge${cls ? ' ' + cls : ''}"${title ? ` title="${esc(title)}"` : ''}>${iconName ? ic(iconName) : ''}${esc(text)}</span>`;

function versionsHTML(l) {
  const r = l.lead;
  if (r.check_error) {
    return `<span class="vers"><span class="ver err">${esc(r.current_version || 'unknown')}</span>${ic('error')}</span>`;
  }
  if (!r.update_available) {
    return `<span class="vers"><span class="ver">${esc(r.current_version)}</span>${ic('check')}<span class="ver ok">current</span></span>`;
  }
  const dist = [distance(r), r.latest_pushed_at ? `pushed ${relTime(r.latest_pushed_at)}` : ''].filter(Boolean).join(' · ');
  return `<span class="vers"><span class="ver">${esc(r.current_version)}</span>${ic('arrow_forward')}`
    + `<span class="ver">${diffHTML(r.current_version, r.latest_version, r.scope)}</span>`
    + (dist ? `<span class="dist">${esc(dist)}</span>` : '') + '</span>';
}

function usedBy(users) {
  const names = [...new Set(users.map((r) => r.chart).filter(Boolean))];
  const shown = names.slice(0, 3).map(esc).join(', ');
  const more = names.length > 3 ? ` +${names.length - 3} more` : '';
  const sources = [...new Set(users.map((r) => r.source))];
  return `${shown}${more}${sources.length ? ` · <span class="muted">${esc(sources.join(', '))}</span>` : ''}`;
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

// ---------- details row ----------

function drawerHTML(g) {
  return `<div class="detail">${g.lines.map(detailFor).join('')}</div>`;
}

function detailFor(l) {
  const r = l.lead;
  const cands = (r.skipped || []).map((s) => `<div class="cand is-rejected">
    ${ic('block')}<span class="ver">${esc(s.tag)}</span><span class="cand-verdict">rejected</span>
    <ul class="reasons">${(s.reasons || []).map((why) => `<li>${ic('close')}<span>${esc(why)}</span></li>`).join('')}</ul>
  </div>`);
  if (r.update_available) {
    cands.push(`<div class="cand is-accepted">${ic('check')}<span class="ver">${esc(r.latest_version)}</span>`
      + `<span class="cand-verdict">offered${r.latest_pushed_at ? ` · pushed ${esc(day(r.latest_pushed_at))}` : ''}</span></div>`);
  }
  cands.push(`<div class="cand is-current">${ic('schedule')}<span class="ver">${esc(r.current_version || '(no tag)')}</span>`
    + `<span class="cand-verdict">in use${r.current_pushed_at ? ` · pushed ${esc(day(r.current_pushed_at))}` : ''}</span></div>`);

  const facts = [
    r.image_digest ? fact('Digest', `<code>${esc(shortDigest(r.image_digest))}…</code> <button class="copy" type="button" data-copy="${esc(r.image_digest)}" title="copy the full digest">${ic('content_copy')}copy</button>`) : '',
    r.pinned_digest ? fact('Pinned', `<code>${esc(shortDigest(r.pinned_digest))}…</code> <span class="muted">— the manifest pins this image</span>`) : '',
    r.tag_kind ? fact('Kind', `${ic(KIND_ICON[r.tag_kind] || KIND_ICON.unknown)} ${esc(r.tag_kind)} <span class="muted">— ${esc(KIND_HELP[r.tag_kind] || '')}</span>`) : '',
    r.current_resolved && r.current_resolved !== r.current_version ? fact('Serves', esc(r.current_resolved)) : '',
    r.scope ? fact('Scope', esc(r.scope)) : '',
    r.final_stage ? fact('Stage', `final <span class="muted">— the image this build publishes</span>`) : '',
    !r.tag_kind && r.protocol ? fact('Source', esc(r.protocol)) : '',
  ].join('');

  const notes = [
    r.check_error ? `<p class="note is-error">${ic('error')}<span><b>${esc(errLabel(r.check_error_kind))}</b> · ${esc(r.check_error)}</span></p>` : '',
    r.check_error && errHint(r.check_error_kind) ? `<p class="note">${ic('info')}<span>${esc(errHint(r.check_error_kind))}</span></p>` : '',
    otherNote(r) ? `<p class="note">${ic('info')}<span>${esc(otherNote(r))}</span></p>` : '',
    l.users.some((u) => u.digest_changed) ? `<p class="note is-drift">${ic('compare_arrows')}<span>the tag points at a different image than in the previous scan</span></p>` : '',
  ].join('');

  const title = `${esc(r.current_version || '(no tag)')}${r.update_available ? ` → ${esc(r.latest_version)}` : ''}`;
  return `<section class="is-${l.state}">
  <h4>${title}</h4>
  ${(r.skipped || []).length || r.update_available ? `<div class="detail-label">Candidates, newest first</div>${cands.join('')}` : ''}
  ${notes}
  ${facts ? `<div class="detail-label">Facts</div><dl class="facts">${facts}</dl>` : ''}
  <div class="detail-label">Used by</div>
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

// The note repeats each skipped tag as "skipped <tag>: <reasons>"; the details row
// already lists those, so only the rest of the note is worth showing.
function otherNote(r) {
  let note = r.note || '';
  for (const s of r.skipped || []) {
    note = note.replace(`skipped ${s.tag}: ${(s.reasons || []).join('; ')}`, '');
  }
  return note.split('; ').map((p) => p.trim()).filter(Boolean).join('; ');
}

// ---------- interaction ----------

// The open dependency lives in the URL hash so a link opens straight to it.
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

async function onTablesClick(e) {
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
  const row = e.target.closest('tr.grp-row');
  if (row) setOpen(openKey() === row.dataset.key ? '' : row.dataset.key);
}

function onCurrentToggled(details) {
  if (details.id !== 'card-current') return;
  prefs.showCurrent = details.open;
  savePrefs();
  details.querySelector('[data-body]').innerHTML = details.open ? tableHTML(currentGroups) : '';
}

// ---------- helpers ----------

// Images keep the registry host muted so the image name reads first.
function refHTML(g) {
  const dep = String(g.dependency);
  const at = dep.indexOf('/');
  if (g.type !== 'image' || at <= 0 || !dep.slice(0, at).includes('.')) return esc(dep);
  return `<span class="muted">${esc(dep.slice(0, at))}/</span>${esc(dep.slice(at + 1))}`;
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
  const glyph = kind === 'err' ? 'error' : 'check_circle';
  const el = document.createElement('div');
  el.className = `toast${kind === 'err' ? ' err' : kind === 'ok' ? ' ok' : ''}`;
  el.innerHTML = `${ic(glyph)}${esc(msg)}`;
  $('toasts').appendChild(el);
  setTimeout(() => el.remove(), 5000);
}

init();
