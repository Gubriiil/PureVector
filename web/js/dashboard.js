import { api } from './api.js';
import { renderRadar, autoRange, COLORS } from './radar.js';
import { fmt0, fmt1, fmt2, gainText, dirShort, dirLong, nearestDirName, fmtDate, h } from './format.js';

const $ = (id) => document.getElementById(id);

let state = null;
let liveChart = null;
let liveKey = '';
let busy = false;
let devSig = '';
let profSig = '';

function profileByName(name) {
  return state?.profiles.find((p) => p.name === name);
}

function gainRingLabel(v) {
  if (v === 1) return '1,00×';
  return fmt2(v) + '×';
}

// ---- status & backend ---------------------------------------------------------

function renderStatus() {
  const pill = $('status-pill');
  const sw = $('enabled-switch');
  sw.checked = state.enabled;
  pill.className = 'pill';
  if (!state.enabled) {
    pill.classList.add('off');
    pill.textContent = 'Aus';
  } else if (state.bypassed) {
    pill.classList.add('paused');
    pill.textContent = 'Pausiert (Kalibrierung)';
  } else if (!state.backend?.running) {
    pill.classList.add('paused');
    pill.textContent = 'Ein – ohne Eingabe-Hook';
  } else {
    pill.classList.add('on');
    pill.textContent = 'Aktiv';
  }

  const box = $('backend-alert');
  const b = state.backend || {};
  box.className = 'alert';
  box.replaceChildren();
  if (b.error) {
    box.classList.add('error');
    box.append(h('strong', {}, 'Eingabe-Hook nicht aktiv'), h('div', {}, b.error), installHelp(state.platform));
  } else if (b.name === 'none' || !b.name) {
    box.classList.add('warn');
    box.append(
      h('strong', {}, 'Ohne Eingabe-Hook gestartet'),
      h('div', {}, b.detail || 'Profile werden erstellt und gespeichert, aber nicht auf die Maus angewendet.'),
    );
  } else if (b.running) {
    box.classList.add('ok');
    box.append(h('div', {}, b.detail || `Backend ${b.name} aktiv.`));
  } else {
    box.classList.add('hidden');
  }
}

function installHelp(platform) {
  if (platform === 'windows') {
    return h('ol', { class: 'small' },
      h('li', {}, 'Interception herunterladen: github.com/oblitum/Interception → Releases → Interception.zip'),
      h('li', {}, 'Eingabeaufforderung als Administrator: „install-interception.exe /install“, danach Windows neu starten.'),
      h('li', {}, 'library\\x64\\interception.dll neben omnivector.exe legen und OmniVector neu starten.'),
      h('li', {}, 'Für Spiele mit Vanguard/FACEIT stattdessen den Raw-Accel-Export nutzen (siehe Profile).'));
  }
  if (platform === 'linux') {
    return h('ol', { class: 'small' },
      h('li', {}, 'sudo modprobe uinput'),
      h('li', {}, 'Als root starten oder Benutzer zur Gruppe „input“ hinzufügen und die udev-Regel aus der README installieren.'));
  }
  return h('div', { class: 'small muted' }, 'Auf diesem System gibt es keinen Eingabe-Hook; nutze den Raw-Accel-Export oder Linux/Windows.');
}

// ---- live view ----------------------------------------------------------------

function pickLiveDevice(devices) {
  let best = null;
  for (const d of devices || []) {
    if (!d.connected) continue;
    if (!best || new Date(d.lastSeen) > new Date(best.lastSeen)) best = d;
  }
  return best;
}

function renderLive(devices) {
  const d = pickLiveDevice(devices);
  const profName = d ? d.profile : state?.defaultProfile;
  const prof = profileByName(profName) || profileByName('neutral');
  const gains = prof?.gains || [1, 1, 1, 1, 1, 1, 1, 1];
  const key = `${prof?.name}|${gains.join(',')}`;
  if (!liveChart || key !== liveKey) {
    const r = autoRange(gains, 0.1);
    liveChart = renderRadar($('live-radar'), {
      n: gains.length,
      min: r.min,
      max: r.max,
      rings: r.rings,
      ringLabel: gainRingLabel,
      labels: (k) => dirShort(k, gains.length),
      series: [{ values: gains, color: COLORS.accent, fill: COLORS.accentFill }],
      title: `Profil ${prof?.name}`,
    });
    liveKey = key;
  }
  $('live-profile').textContent = prof?.name ?? '–';
  const active = !!(d && d.active);
  const enabledNow = state?.enabled && !state?.bypassed;
  // The engine reports the gain it actually applied (1 when switched off).
  const g = active ? (enabledNow ? d.liveGain : 1) : NaN;
  liveChart.setLive(active ? d.liveAngle : NaN, g, active);
  $('live-device').textContent = d ? d.name : 'Keine Maus erkannt';
  $('live-gain').textContent = active ? gainText(g) : '–';
  $('live-dir').textContent = active ? nearestDirName(d.liveAngle) : '–';
}

async function pollLive() {
  if (document.hidden || !state) return;
  try {
    const live = await api('/api/live');
    state.enabled = live.enabled;
    state.bypassed = live.bypassed;
    renderLive(live.devices);
  } catch {
    /* server gone – the state poll shows it */
  }
}

// ---- devices ------------------------------------------------------------------

function profileOptions(selected, withDefault) {
  const opts = [];
  if (withDefault) opts.push(h('option', { value: '', selected: selected === '' }, `Standard (${state.defaultProfile})`));
  for (const p of state.profiles) {
    opts.push(h('option', { value: p.name, selected: p.name === selected }, p.builtIn ? 'neutral (unverändert)' : p.name));
  }
  return opts;
}

function renderDevices() {
  const rows = $('device-rows');
  const list = state.devices || [];
  $('dev-count').textContent = list.length ? `${list.filter((d) => d.connected).length} verbunden` : '';
  if (!list.length) {
    rows.replaceChildren(h('tr', {}, h('td', { colspan: 4, class: 'muted' },
      state.backend?.running ? 'Noch keine Maus erkannt – bewege die Maus.' : 'Ohne aktiven Eingabe-Hook werden keine Mäuse erkannt.')));
    return;
  }
  rows.replaceChildren(...list.map((d) => {
    const sel = h('select', { 'aria-label': `Profil für ${d.name}` }, profileOptions(d.assigned ? d.profile : '', true));
    sel.addEventListener('change', () => assign(d, sel.value));
    let status = 'nicht verbunden';
    let dot = 'dot';
    if (d.connected) {
      status = d.active ? 'bewegt sich' : 'verbunden';
      dot = d.active ? 'dot live' : 'dot on';
    }
    return h('tr', {},
      h('td', {}, d.name),
      h('td', { class: 'mono muted' }, d.key),
      h('td', {}, h('span', { class: dot }), status),
      h('td', {}, sel));
  }));
}

async function assign(d, profile) {
  await guarded(() => api('/api/assign', { method: 'POST', body: { device: d.key, name: d.name, profile } }));
}

// ---- profiles -----------------------------------------------------------------

function sourceText(p) {
  const s = p.source;
  if (p.builtIn) return 'Originalverhalten der Maus';
  if (!s) return '';
  const parts = [];
  if (s.kind === 'calibration') parts.push('Kalibrierung');
  else if (s.kind === 'refine') parts.push(`Verfeinert (Basis: ${s.parent})`);
  else if (s.kind === 'manual') parts.push('manuell');
  if (s.validTrials) parts.push(`${fmt0(s.validTrials)} Schüsse`);
  if (s.spreadPct) parts.push(`Richtungsstreuung ${fmt1(s.spreadPct)} %`);
  if (s.device) parts.push(s.device);
  return parts.join(' · ');
}

function renderProfiles() {
  const def = $('default-select');
  def.replaceChildren(...profileOptions(state.defaultProfile, false));
  const usage = {};
  for (const d of state.devices || []) if (d.assigned) usage[d.profile] = (usage[d.profile] || 0) + 1;

  const list = $('profile-list');
  list.replaceChildren(...state.profiles.map((p) => {
    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    const r = autoRange(p.gains, 0.1);
    renderRadar(svg, {
      n: p.gains.length, compact: true, min: r.min, max: r.max, rings: [1],
      series: [{ values: p.gains, color: COLORS.accent, fill: COLORS.accentFill, width: 3 }],
      title: `Profil ${p.name}`,
    });
    const isDefault = p.name === state.defaultProfile;
    const badges = [];
    if (isDefault) badges.push(h('span', { class: 'badge' }, 'Standard'));
    if (usage[p.name]) badges.push(h('span', { class: 'badge gray' }, `${usage[p.name]} Gerät(e)`));
    const range = p.gains.length ? `${fmt2(Math.min(...p.gains))}× … ${fmt2(Math.max(...p.gains))}×` : '';
    const actions = [];
    if (!isDefault) actions.push(h('button', { class: 'btn small', onclick: () => setDefault(p.name) }, 'Als Standard'));
    if (!p.builtIn) {
      actions.push(h('button', { class: 'btn small', onclick: () => showRawAccel(p) }, 'Raw Accel'));
      actions.push(h('button', { class: 'btn small danger', onclick: () => removeProfile(p) }, 'Löschen'));
    }
    return h('div', { class: isDefault ? 'profile active' : 'profile' },
      h('div', { class: 'row' }, h('h3', {}, p.builtIn ? 'neutral' : p.name), h('span', { class: 'spacer' }), ...badges),
      svg,
      h('div', { class: 'small muted' }, sourceText(p)),
      h('div', { class: 'small faint' }, [fmtDate(p.createdAt), range].filter(Boolean).join(' · ')),
      h('div', { class: 'actions' }, ...actions));
  }));
}

async function setDefault(name) {
  await guarded(() => api('/api/default', { method: 'POST', body: { profile: name } }));
}

async function removeProfile(p) {
  if (!confirm(`Profil „${p.name}“ löschen?`)) return;
  await guarded(() => api(`/api/profiles/${encodeURIComponent(p.name)}`, { method: 'DELETE' }));
}

// ---- Raw Accel ----------------------------------------------------------------

function openModal(title, ...content) {
  $('modal-title').textContent = title;
  $('modal-body').replaceChildren(...content);
  $('modal').classList.remove('hidden');
}

function closeModal() {
  $('modal').classList.add('hidden');
}

async function showRawAccel(p) {
  let res;
  try {
    res = await api(`/api/profiles/${encodeURIComponent(p.name)}/rawaccel`);
  } catch (e) {
    openModal('Raw-Accel-Export', h('div', { class: 'alert error' }, e.message));
    return;
  }
  const n = p.gains.length;
  const compare = h('table', {},
    h('thead', {}, h('tr', {}, h('th', {}, 'Richtung'), h('th', { class: 'r' }, 'Profil'), h('th', { class: 'r' }, 'Raw Accel'))),
    h('tbody', {}, ...p.gains.map((g, k) => h('tr', {},
      h('td', {}, dirLong(k, n)),
      h('td', { class: 'r num' }, gainText(g)),
      h('td', { class: 'r num' }, gainText(res.model[k]))))));
  openModal('Raw-Accel-Export',
    h('p', { class: 'muted' },
      'Raw Accel (signierter Treiber, mit vielen Anti-Cheats nutzbar) kennt vier Richtungsfaktoren: rechts, links, oben, unten. ' +
      'Diagonalen ergeben sich daraus. Dieses Profil wird damit zu ', h('b', {}, `${fmt0(res.coveragePct)} %`), ' abgebildet.'),
    h('table', {},
      h('thead', {}, h('tr', {}, h('th', {}, 'Raw-Accel-Feld'), h('th', { class: 'r' }, 'mit diesem Faktor multiplizieren'))),
      h('tbody', {},
        h('tr', {}, h('td', {}, 'Sensitivity multiplier / Output DPI'), h('td', { class: 'r num' }, fmt2(res.factors.sens))),
        h('tr', {}, h('td', {}, 'Y/X ratio'), h('td', { class: 'r num' }, fmt2(res.factors.yx))),
        h('tr', {}, h('td', {}, 'L/R ratio (links)'), h('td', { class: 'r num' }, fmt2(res.factors.lr))),
        h('tr', {}, h('td', {}, 'U/D ratio (oben)'), h('td', { class: 'r num' }, fmt2(res.factors.ud))))),
    h('p', { class: 'small muted' }, 'Automatisch in eine vorhandene Raw-Accel-settings.json eintragen (mit Sicherung):'),
    h('pre', { class: 'code' }, res.command),
    h('details', {}, h('summary', { class: 'muted small' }, 'Vergleich je Richtung'), compare));
}

// ---- main ---------------------------------------------------------------------

async function guarded(fn) {
  if (busy) return;
  busy = true;
  try {
    await fn();
  } catch (e) {
    alert(e.message);
  } finally {
    busy = false;
    await refresh();
  }
}

async function refresh() {
  try {
    state = await api('/api/state');
  } catch (e) {
    $('status-pill').className = 'pill off';
    $('status-pill').textContent = 'Keine Verbindung';
    return;
  }
  renderStatus();
  renderLive(state.devices);
  // Only rebuild lists when something visible changed, and never while the
  // user is working with one of their dropdowns.
  const profNames = state.profiles.map((p) => p.name + ':' + p.gains.join(',')).join('|') + '#' + state.defaultProfile;
  const ds = JSON.stringify(state.devices.map((d) => [d.key, d.name, d.connected, d.profile, d.assigned, d.active])) + profNames;
  const focusIn = (id) => $(id).contains(document.activeElement);
  if (ds !== devSig && !focusIn('device-rows')) {
    renderDevices();
    devSig = ds;
  }
  const ps = profNames + JSON.stringify(state.devices.map((d) => [d.profile, d.assigned]));
  if (ps !== profSig && !focusIn('default-select')) {
    renderProfiles();
    profSig = ps;
  }
  const hk = state.hotkey && state.platform === 'windows' ? `Umschalten: ${state.hotkey.toUpperCase().replaceAll('+', ' + ')}` : 'Umschalten: omnivector toggle';
  $('hotkey-hint').textContent = hk;
  $('config-dir').textContent = `Konfiguration: ${state.configDir}`;
  $('version').textContent = `v${state.version}`;
  const last = state.profiles.find((p) => p.source && (p.source.kind === 'calibration' || p.source.kind === 'refine'));
  $('last-cal').textContent = last ? `Letzte Kalibrierung: ${last.name} (${fmtDate(last.createdAt)})` : 'Noch keine Kalibrierung.';
}

$('enabled-switch').addEventListener('change', (e) =>
  guarded(() => api('/api/enabled', { method: 'POST', body: { enabled: e.target.checked } })));
$('default-select').addEventListener('change', (e) => setDefault(e.target.value));
$('modal-close').addEventListener('click', closeModal);
$('modal').addEventListener('click', (e) => { if (e.target.id === 'modal') closeModal(); });
document.addEventListener('keydown', (e) => { if (e.key === 'Escape') closeModal(); });
document.addEventListener('visibilitychange', () => { if (!document.hidden) refresh(); });

refresh();
setInterval(() => { if (!document.hidden && !busy) refresh(); }, 2500);
setInterval(pollLive, 100);
