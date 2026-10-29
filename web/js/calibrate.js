import { api } from './api.js';
import { Session, DEFAULTS, countsPerDegree } from './session.js';
import { renderRadar, autoRange, COLORS } from './radar.js';
import { fmt0, fmt1, fmt2, pctDelta, gainText, dirLong, h } from './format.js';

const $ = (id) => document.getElementById(id);
const canvas = $('stage');
const ctx = canvas.getContext('2d', { alpha: false, desynchronized: true });

const PRESETS = { '3x45': [3, 45], '5x60': [5, 60], '8x60': [8, 60] };
const STORE_KEY = 'omnivector.calibration.v1';
const rad = (d) => (d * Math.PI) / 180;

let appState = null;
let settings = null; // {dpi, cm360, fov, rounds, roundSeconds, directions}
let session = null;
let calId = '';
let mode = 'fresh';
let phase = 'setup'; // setup | countdown | running | between | paused | analyzing | results | error
let pausedFrom = '';
let round = 0;
let roundElapsed = 0;
let lastFrame = 0;
let unadjusted = false;
let lockToken = 0;
let effects = [];
let runTime = 0;
let countdownTimer = 0;
let requireLock = true; // tests can drive the task without pointer lock
let heartbeatTimer = 0;
const moveEventType = 'onpointerrawupdate' in window ? 'pointerrawupdate' : 'pointermove';

// ---------------------------------------------------------------- overlays --

const OVERLAYS = ['ov-setup', 'ov-count', 'ov-between', 'ov-pause', 'ov-analyze', 'ov-results', 'ov-error'];
function show(id) {
  for (const o of OVERLAYS) $(o).classList.toggle('hidden', o !== id);
  $('hud').classList.toggle('hidden', !['running', 'countdown', 'between', 'paused'].includes(phase));
}

// ------------------------------------------------------------------- setup --

function loadSaved() {
  try {
    return JSON.parse(localStorage.getItem(STORE_KEY)) || {};
  } catch {
    return {};
  }
}

function storeSettings(s) {
  try {
    localStorage.setItem(STORE_KEY, JSON.stringify(s));
  } catch {
    /* private mode – not important */
  }
}

function activeProfileName(st) {
  if (!st) return 'neutral';
  let best = null;
  for (const d of st.devices || []) {
    if (d.connected && (!best || new Date(d.lastSeen) > new Date(best.lastSeen))) best = d;
  }
  return best ? best.profile : st.defaultProfile;
}

async function initSetup() {
  const saved = loadSaved();
  $('f-dpi').value = saved.dpi ?? DEFAULTS.dpi;
  $('f-cm360').value = saved.cm360 ?? DEFAULTS.cm360;
  $('f-fov').value = saved.fov ?? DEFAULTS.fov;
  if (saved.preset && PRESETS[saved.preset]) $('f-preset').value = saved.preset;
  if (saved.dirs) $('f-dirs').value = String(saved.dirs);
  if (saved.fullscreen === false) $('f-fullscreen').value = '0';

  try {
    appState = await api('/api/state');
  } catch {
    appState = null;
  }
  const warn = $('setup-warn');
  const prof = activeProfileName(appState);
  const refineOK = appState && appState.enabled && appState.backend?.running && prof && prof !== 'neutral';
  const refineInput = document.querySelector('input[name="mode"][value="refine"]');
  refineInput.disabled = !refineOK;
  $('refine-choice').classList.toggle('faint', !refineOK);
  $('refine-hint').textContent = refineOK
    ? `Misst mit deinem aktiven Profil „${prof}“ und korrigiert nur die verbleibende Abweichung.`
    : 'Erst verfügbar, wenn ein Profil aktiv ist und der Eingabe-Hook läuft.';
  if (!appState) {
    warn.textContent = 'OmniVector-Dienst nicht erreichbar – bitte omnivector starten und diese Seite neu laden.';
    warn.classList.remove('hidden');
  } else if (!appState.backend?.running) {
    warn.textContent =
      'Hinweis: OmniVector läuft ohne Eingabe-Hook. Die Messung funktioniert, das Profil wirkt aber erst mit aktivem Hook oder über den Raw-Accel-Export.';
    warn.classList.remove('hidden');
  }
}

$('setup-form').addEventListener('submit', (e) => {
  e.preventDefault();
  startCalibration();
});

function startCalibration() {
  const dpi = Number($('f-dpi').value);
  const cm360 = Number($('f-cm360').value);
  const fov = Number($('f-fov').value);
  const preset = $('f-preset').value;
  const directions = Number($('f-dirs').value);
  const fullscreen = $('f-fullscreen').value === '1';
  const warn = $('setup-warn');
  if (!(dpi >= 100 && dpi <= 64000 && cm360 >= 5 && cm360 <= 200 && fov >= 60 && fov <= 130)) {
    warn.textContent = 'Bitte gültige Werte eingeben (DPI 100–64000, cm/360° 5–200, FOV 60–130).';
    warn.classList.remove('hidden');
    return;
  }
  mode = document.querySelector('input[name="mode"]:checked').value;
  storeSettings({ dpi, cm360, fov, preset, dirs: directions, fullscreen });
  const [rounds, roundSeconds] = PRESETS[preset] || PRESETS['5x60'];
  settings = { dpi, cm360, fov, rounds, roundSeconds, directions };

  // Keep every target comfortably on screen, whatever the aspect ratio.
  const aspect = innerWidth / Math.max(1, innerHeight);
  const vHalf = (Math.atan(Math.tan(rad(fov / 2)) / aspect) * 180) / Math.PI;
  const maxDeg = Math.min(DEFAULTS.maxDeg, 0.8 * vHalf);
  const minDeg = Math.min(DEFAULTS.minDeg, 0.4 * maxDeg);
  session = new Session({ cpd: countsPerDegree(dpi, cm360), minDeg, maxDeg, radiusDeg: DEFAULTS.radiusDeg });
  runTime = 0;

  // Both requests need the click's user activation, so they come first.
  if (fullscreen && document.documentElement.requestFullscreen) {
    document.documentElement.requestFullscreen().catch(() => {});
  }
  lockPointer();
  calId = '';
  api('/api/calibration/start', { method: 'POST', body: { mode } })
    .then((info) => {
      calId = info.id;
      startHeartbeat();
    })
    .catch(() => { calId = ''; });
  round = 1;
  beginCountdown();
}

// -------------------------------------------------------------- heartbeat --
// A fresh measurement pauses the active profile on the server only while
// this page keeps checking in, so a closed tab never leaves it paused.

function heartbeat() {
  if (calId) api('/api/calibration/heartbeat', { method: 'POST', body: { id: calId } }).catch(() => {});
}

function startHeartbeat() {
  clearInterval(heartbeatTimer);
  heartbeatTimer = setInterval(heartbeat, 5000);
}

function stopHeartbeat() {
  clearInterval(heartbeatTimer);
}

document.addEventListener('visibilitychange', () => {
  if (!document.hidden && calId && phase !== 'results' && phase !== 'error') heartbeat();
});

addEventListener('pagehide', () => {
  if (!calId || ['analyzing', 'results', 'error'].includes(phase)) return;
  try {
    fetch('/api/calibration/cancel', {
      method: 'POST',
      keepalive: true,
      headers: { 'X-OmniVector': '1', 'Content-Type': 'application/json' },
      body: JSON.stringify({ id: calId }),
    });
  } catch {
    /* the heartbeat timeout covers this case */
  }
});

// ----------------------------------------------------------- pointer lock --

function lockPointer() {
  if (!requireLock) return;
  const token = ++lockToken;
  let p;
  try {
    p = canvas.requestPointerLock({ unadjustedMovement: true });
  } catch {
    p = undefined;
  }
  if (p && typeof p.then === 'function') {
    p.then(() => { if (token === lockToken) unadjusted = true; }).catch(() => {
      // Raw input not supported here (e.g. Firefox, some Linux setups).
      unadjusted = false;
      try {
        const q = canvas.requestPointerLock();
        if (q && q.catch) q.catch(() => {});
      } catch {
        /* handled by pointerlockerror */
      }
    });
  } else {
    unadjusted = false; // old API: no promise, no raw input guarantee
  }
}

const locked = () => !requireLock || document.pointerLockElement === canvas;

document.addEventListener('pointerlockchange', () => {
  if (!locked() && ['running', 'countdown', 'between'].includes(phase)) pause();
});

document.addEventListener('pointerlockerror', () => {
  if (requireLock && ['running', 'countdown', 'between'].includes(phase)) pause();
});

function pause() {
  clearTimeout(countdownTimer);
  pausedFrom = phase;
  phase = 'paused';
  if (session) session.endRound();
  show('ov-pause');
}

$('resume-btn').addEventListener('click', () => {
  lockPointer();
  if (pausedFrom === 'between') {
    phase = 'between';
    show('ov-between');
  } else {
    beginCountdown(true);
  }
});
$('finish-early-btn').addEventListener('click', () => finish());
$('abort-btn').addEventListener('click', async () => {
  stopHeartbeat();
  if (calId) await api('/api/calibration/cancel', { method: 'POST', body: { id: calId } }).catch(() => {});
  calId = '';
  location.reload();
});
$('retry-btn').addEventListener('click', () => location.reload());

// ------------------------------------------------------------------ rounds --

function beginCountdown(resume = false) {
  phase = 'countdown';
  show('ov-count');
  let n = resume ? 1 : 3;
  $('count-num').textContent = n;
  const step = () => {
    n--;
    if (n <= 0) {
      startRound(resume);
      return;
    }
    $('count-num').textContent = n;
    countdownTimer = setTimeout(step, 650);
  };
  countdownTimer = setTimeout(step, 650);
}

function startRound(resume) {
  phase = 'running';
  show(null);
  if (!resume) roundElapsed = 0;
  const keep = resume ? { ...session.roundStats } : null;
  session.startRound(round, performance.now());
  if (keep) session.roundStats = keep;
}

function endRound() {
  session.endRound();
  if (round >= settings.rounds) {
    finish();
    return;
  }
  phase = 'between';
  const rs = session.roundStats;
  const acc = rs.hits + rs.misses ? Math.round((100 * rs.hits) / (rs.hits + rs.misses)) : 0;
  $('between-title').textContent = `Runde ${round} von ${settings.rounds} geschafft`;
  $('between-text').textContent = `${rs.hits} Treffer · Genauigkeit ${acc} % · bisher ${session.trials.length} Ziele gemessen`;
  show('ov-between');
}

// ------------------------------------------------------------------- input --

canvas.addEventListener(moveEventType, (e) => {
  if (phase !== 'running' || !locked()) return;
  const list = typeof e.getCoalescedEvents === 'function' ? e.getCoalescedEvents() : null;
  if (list && list.length) {
    for (const ce of list) session.move(ce.movementX, ce.movementY, ce.timeStamp);
  } else {
    session.move(e.movementX, e.movementY, e.timeStamp);
  }
});

canvas.addEventListener('mousedown', (e) => {
  if (e.button !== 0) return;
  if (phase === 'between' && locked()) {
    round++;
    beginCountdown();
    return;
  }
  if (phase !== 'running') return;
  const target = session.current;
  const res = session.click(e.timeStamp);
  if (res === 'hit') effects.push({ kind: 'hit', x: target.tx, y: target.ty, t: performance.now() });
  else if (res === 'miss') effects.push({ kind: 'miss', t: performance.now() });
});

// ----------------------------------------------------------------- drawing --

function resize() {
  const dpr = Math.min(window.devicePixelRatio || 1, 2);
  canvas.width = Math.round(innerWidth * dpr);
  canvas.height = Math.round(innerHeight * dpr);
}
addEventListener('resize', resize);
resize();

function draw(now) {
  const W = canvas.width;
  const H = canvas.height;
  const dpr = W / Math.max(1, innerWidth);
  ctx.fillStyle = '#07090d';
  ctx.fillRect(0, 0, W, H);
  if (!session || !settings) return;

  const f = W / 2 / Math.tan(rad(settings.fov / 2)); // perspective focal length in px
  const cx = W / 2;
  const cy = H / 2;
  const cpd = session.cpd;
  const camX = session.pos.x / cpd;
  const camY = session.pos.y / cpd;
  const visX = settings.fov / 2 + 3;
  const visY = (Math.atan(H / 2 / f) * 180) / Math.PI + 3;

  // World-locked grid (5°) for a sense of camera motion.
  ctx.lineWidth = Math.max(1, dpr);
  ctx.strokeStyle = 'rgba(148,163,184,0.07)';
  ctx.beginPath();
  for (let g = Math.ceil((camX - visX) / 5) * 5; g <= camX + visX; g += 5) {
    const x = cx + f * Math.tan(rad(g - camX));
    ctx.moveTo(x, 0);
    ctx.lineTo(x, H);
  }
  for (let g = Math.ceil((camY - visY) / 5) * 5; g <= camY + visY; g += 5) {
    const y = cy + f * Math.tan(rad(g - camY));
    ctx.moveTo(0, y);
    ctx.lineTo(W, y);
  }
  ctx.stroke();

  const project = (wx, wy) => {
    const dx = wx / cpd - camX;
    const dy = wy / cpd - camY;
    if (Math.abs(dx) > 80 || Math.abs(dy) > 80) return null;
    return [cx + f * Math.tan(rad(dx)), cy + f * Math.tan(rad(dy)), Math.hypot(dx, dy)];
  };

  // Target.
  const c = session.current;
  if (c && phase !== 'between') {
    const p = project(c.tx, c.ty);
    if (p) {
      const rr = (f * Math.tan(rad(c.r / cpd))) / Math.cos(rad(Math.min(p[2], 70)));
      if (c.kind === 'out') {
        ctx.fillStyle = 'rgba(45,212,191,0.18)';
        ctx.beginPath();
        ctx.arc(p[0], p[1], rr * 1.9, 0, 2 * Math.PI);
        ctx.fill();
        ctx.fillStyle = '#2dd4bf';
        ctx.beginPath();
        ctx.arc(p[0], p[1], rr, 0, 2 * Math.PI);
        ctx.fill();
        ctx.strokeStyle = '#ecfeff';
        ctx.lineWidth = 2 * dpr;
        ctx.stroke();
      } else {
        ctx.fillStyle = 'rgba(232,237,244,0.16)';
        ctx.beginPath();
        ctx.arc(p[0], p[1], rr, 0, 2 * Math.PI);
        ctx.fill();
        ctx.strokeStyle = '#e8edf4';
        ctx.lineWidth = 2.5 * dpr;
        ctx.stroke();
      }
    }
  }

  // Hit and miss effects.
  effects = effects.filter((fx) => now - fx.t < 300);
  for (const fx of effects) {
    const age = (now - fx.t) / 300;
    if (fx.kind === 'hit') {
      const p = project(fx.x, fx.y);
      if (!p) continue;
      ctx.strokeStyle = `rgba(94,234,212,${1 - age})`;
      ctx.lineWidth = 3 * dpr;
      ctx.beginPath();
      ctx.arc(p[0], p[1], (10 + 40 * age) * dpr, 0, 2 * Math.PI);
      ctx.stroke();
    } else {
      ctx.fillStyle = `rgba(248,113,113,${0.6 * (1 - age)})`;
      ctx.beginPath();
      ctx.arc(cx, cy, 9 * dpr, 0, 2 * Math.PI);
      ctx.fill();
    }
  }

  // Crosshair.
  const s = 9 * dpr;
  const gap = 3 * dpr;
  ctx.lineCap = 'round';
  for (const [color, width] of [['rgba(0,0,0,0.85)', 4 * dpr], ['#ffffff', 2 * dpr]]) {
    ctx.strokeStyle = color;
    ctx.lineWidth = width;
    ctx.beginPath();
    ctx.moveTo(cx - s, cy); ctx.lineTo(cx - gap, cy);
    ctx.moveTo(cx + gap, cy); ctx.lineTo(cx + s, cy);
    ctx.moveTo(cx, cy - s); ctx.lineTo(cx, cy - gap);
    ctx.moveTo(cx, cy + gap); ctx.lineTo(cx, cy + s);
    ctx.stroke();
  }
}

let hudCache = '';
function updateHud() {
  if (!settings || !session) return;
  const left = Math.max(0, settings.roundSeconds - roundElapsed / 1000);
  const rs = session.roundStats;
  const acc = rs.hits + rs.misses ? `${Math.round((100 * rs.hits) / (rs.hits + rs.misses))} %` : '–';
  const key = `${round}|${Math.ceil(left)}|${rs.hits}|${acc}`;
  $('hud-bar').style.width = `${Math.min(100, (100 * roundElapsed) / (settings.roundSeconds * 1000))}%`;
  if (key === hudCache) return;
  hudCache = key;
  $('hud-round').textContent = `${round}/${settings.rounds}`;
  $('hud-time').textContent = `${Math.ceil(left)} s`;
  $('hud-hits').textContent = rs.hits;
  $('hud-acc').textContent = acc;
}

function frame(ts) {
  requestAnimationFrame(frame);
  const dt = lastFrame ? Math.min(100, ts - lastFrame) : 0;
  lastFrame = ts;
  if (phase === 'running') {
    roundElapsed += dt;
    runTime += dt;
    session.tick(performance.now());
    if (roundElapsed >= settings.roundSeconds * 1000) endRound();
  }
  draw(performance.now());
  updateHud();
}
requestAnimationFrame(frame);

// ------------------------------------------------------------------ finish --

async function finish() {
  phase = 'analyzing';
  stopHeartbeat();
  clearTimeout(countdownTimer);
  if (document.pointerLockElement) document.exitPointerLock();
  if (document.fullscreenElement) document.exitFullscreen().catch(() => {});
  show('ov-analyze');
  const rateHz = runTime > 1000 ? session.stats.events / (runTime / 1000) : 0;
  const payload = session.payload(settings, {
    userAgent: navigator.userAgent,
    unadjusted,
    eventType: moveEventType,
    rateHz: Math.round(rateHz),
  });
  payload.mode = mode;
  payload.sessionId = calId;
  await submit(payload);
}

async function submit(payload) {
  try {
    const res = await api('/api/calibration/finish', { method: 'POST', body: { id: calId, session: payload } });
    showResults(res);
  } catch (e) {
    if (calId) api('/api/calibration/cancel', { method: 'POST', body: { id: calId } }).catch(() => {});
    phase = 'error';
    $('error-text').textContent = e.message;
    show('ov-error');
  }
}

// ----------------------------------------------------------------- results --

const REF_TEXT = {
  strongest: ['Stärkste Richtung (wie PureVector)', 'Alle Richtungen landen künftig so wie deine genaueste Richtung.'],
  mean: ['Mittelwert', 'Gleicht nur die Unterschiede aus – deine Gesamtsensitivität bleibt gleich.'],
  absolute: ['Absolut', 'Korrigiert jede Richtung exakt aufs Ziel. Experimentell: Menschen unterschießen bewusst leicht.'],
};

let view = null; // { res, n, reference, strength, suggestion, final }

function pText(p) {
  if (!(p >= 0)) return 'nicht berechenbar';
  if (p < 0.001) return 'p < 0,001';
  if (p < 0.01) return `p = ${p.toFixed(3).replace('.', ',')}`;
  return `p = ${fmt2(p)}`;
}

function qualityBadge(q) {
  if (q === 'good') return h('span', { class: 'badge' }, 'Datenqualität: gut');
  if (q === 'fair') return h('span', { class: 'badge warn' }, 'Datenqualität: ausreichend');
  return h('span', { class: 'badge warn' }, 'Datenqualität: unsicher');
}

function summaryText(r) {
  const ds = r.perDirection;
  const n = ds.length;
  if (r.model.pValue > 0.2) {
    return 'Keine deutlichen Richtungsunterschiede: Deine Flicks landen in alle Richtungen ähnlich. Das Profil bleibt nahezu neutral.';
  }
  let best = 0;
  ds.forEach((d, k) => { if (Math.abs(d.logErr) < Math.abs(ds[best].logErr)) best = k; });
  const over = [...ds].sort((a, b) => b.shrunk - a.shrunk)[0];
  const under = [...ds].sort((a, b) => a.shrunk - b.shrunk)[0];
  const parts = [`Am genauesten triffst du nach ${dirLong(best, n)} (${pctDelta(ds[best].shrunk)}).`];
  if (over.shrunk > 1.005) parts.push(`Nach ${dirLong(over.index, n)} überschießt du um ${pctDelta(over.shrunk).replace('+', '')}.`);
  if (under.shrunk < 0.995) parts.push(`Nach ${dirLong(under.index, n)} unterschießt du um ${pctDelta(under.shrunk).replace('−', '')}.`);
  return parts.join(' ');
}

function showResults(res) {
  phase = 'results';
  const r = res.result;
  view = {
    res,
    n: r.directions,
    reference: r.suggested.reference,
    strength: r.suggested.strength,
    suggestion: r.suggested,
    final: res.final,
  };
  const box = $('results');
  const ctxInfo = res.context || {};
  const head = h('div', { class: 'card-head' },
    h('h1', {}, 'Dein Richtungsprofil'), qualityBadge(r.quality), h('span', { class: 'spacer' }),
    h('a', { class: 'btn ghost', href: '/' }, 'Dashboard'));
  const sub = h('p', { class: 'muted' },
    `Gemessen mit: ${ctxInfo.deviceName || 'Maus nicht zugeordnet (kein Eingabe-Hook aktiv)'}`,
    ctxInfo.baseProfile ? ` · verfeinert auf Basis von „${ctxInfo.baseProfile}“` : '');

  const chartMeasure = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  const chartGain = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  chartMeasure.id = 'chart-measure';
  chartGain.id = 'chart-gain';

  const kpis = h('div', { class: 'kpis', id: 'kpis' });
  const table = h('div', { class: 'table-wrap', id: 'dir-table' });
  const warnings = r.warnings && r.warnings.length
    ? h('div', { class: 'alert warn' }, h('strong', {}, 'Hinweise'), h('ul', {}, ...r.warnings.map((w) => h('li', {}, w))))
    : null;

  const refBox = h('fieldset', {}, h('legend', {}, 'Bezugsrichtung'),
    ...Object.entries(REF_TEXT).map(([key, [label, hint]]) => {
      const input = h('input', { type: 'radio', name: 'ref', value: key, checked: key === view.reference });
      input.addEventListener('change', () => { view.reference = key; schedulePreview(); });
      return h('label', { class: 'choice' }, input, h('span', {}, label, h('small', {}, hint)));
    }));
  const strength = h('input', { type: 'range', min: 0, max: 100, step: 5, value: Math.round(view.strength * 100), id: 'strength' });
  const strengthLabel = h('b', { id: 'strength-label' }, `${Math.round(view.strength * 100)} %`);
  strength.addEventListener('input', () => {
    view.strength = Math.max(0.05, Number(strength.value) / 100);
    strengthLabel.textContent = `${strength.value} %`;
    schedulePreview();
  });

  const nameInput = h('input', { type: 'text', id: 'save-name', value: res.suggestedName, maxlength: 64, 'aria-label': 'Profilname' });
  const targets = [
    ['default', 'Für alle Mäuse (als Standardprofil)'],
    ...(ctxInfo.deviceKey ? [['device', `Nur für ${ctxInfo.deviceName}`]] : []),
    ['none', 'Nur speichern, nicht aktivieren'],
  ];
  const targetBox = h('fieldset', {}, h('legend', {}, 'Anwenden'),
    ...targets.map(([v, label], i) => h('label', { class: 'choice' },
      h('input', { type: 'radio', name: 'target', value: v, checked: i === 0 }), h('span', {}, label))));
  const saveBtn = h('button', { class: 'btn primary big', id: 'save-btn' }, 'Speichern & aktivieren');
  const saveMsg = h('div', { id: 'save-msg' });
  saveBtn.addEventListener('click', () => saveProfile(nameInput.value, saveBtn, saveMsg));

  box.replaceChildren(
    head, sub,
    h('p', { class: 'summary' }, summaryText(r)),
    kpis,
    h('div', { class: 'res-grid' },
      h('div', { class: 'stack' }, h('h2', {}, 'Messung: Wo landen deine Flicks?'), chartMeasure,
        h('div', { class: 'legend' },
          h('span', {}, h('i', { class: 'sw-over' }), 'überschossen'),
          h('span', {}, h('i', { class: 'sw-under' }), 'zu kurz'),
          h('span', {}, h('i', { class: 'sw-text' }), 'Mittel je Richtung'),
          h('span', {}, h('i', { class: 'sw-dash' }), 'exakt im Ziel'))),
      h('div', { class: 'stack' }, h('h2', {}, 'Korrektur: Sensitivität je Richtung'), chartGain,
        h('div', { class: 'legend' },
          h('span', {}, h('i', { class: 'sw-accent' }), 'neues Profil'),
          h('span', {}, h('i', { class: 'sw-dash' }), 'neutral 1,00×')))),
    table,
    warnings,
    h('div', { class: 'res-grid' },
      h('div', { class: 'stack' }, refBox,
        h('label', { class: 'field' }, h('span', {}, 'Stärke der Korrektur: ', strengthLabel), strength)),
      h('div', { class: 'stack' }, h('label', { class: 'field' }, 'Profilname', nameInput), targetBox,
        h('div', { class: 'row' }, saveBtn), saveMsg)),
  );
  show('ov-results');
  renderMeasure();
  renderCorrection();
}

function renderMeasure() {
  const r = view.res.result;
  const n = view.n;
  const shr = r.perDirection.map((d) => d.shrunk);
  const range = autoRange(shr.concat([0.9, 1.1]), 0.1);
  const points = (view.res.points || [])
    .filter((p) => p[2] === 1)
    .map(([deg, ratio]) => ({ deg, value: ratio, color: ratio >= 1 ? COLORS.over : COLORS.under, opacity: 0.32 }));
  renderRadar($('chart-measure'), {
    n, min: range.min, max: range.max, rings: range.rings,
    ringLabel: (v) => (v === 1 ? 'exakt' : pctDelta(v)),
    labels: (k) => (n === 8 ? ['→', '↗', '↑', '↖', '←', '↙', '↓', '↘'][k] : `${fmt0((k * 360) / n)}°`),
    points,
    series: [{ values: shr, color: COLORS.text, width: 2.5 }],
    title: 'Landung der Primärbewegung je Richtung',
  });
}

function renderCorrection() {
  const r = view.res.result;
  const n = view.n;
  const range = autoRange(view.final.concat([0.95, 1.05]), 0.08);
  renderRadar($('chart-gain'), {
    n, min: range.min, max: range.max, rings: range.rings,
    ringLabel: (v) => `${fmt2(v)}×`,
    labels: (k) => (n === 8 ? ['→', '↗', '↑', '↖', '←', '↙', '↓', '↘'][k] : `${fmt0((k * 360) / n)}°`),
    series: [{ values: view.final, color: COLORS.accent, fill: COLORS.accentFill, width: 2.6 }],
    title: 'Sensitivitätsfaktor je Richtung',
  });

  const s = view.suggestion;
  const kp = $('kpis');
  kp.replaceChildren(
    h('div', { class: 'kpi' }, h('b', {}, `${fmt0(r.counts.valid)} / ${fmt0(r.counts.total)}`), h('span', {}, 'auswertbare Ziele')),
    h('div', { class: 'kpi' }, h('b', {}, `${fmt1(r.model.spreadPct)} %`), h('span', {}, 'Richtungsstreuung')),
    h('div', { class: 'kpi' }, h('b', {}, pText(r.model.pValue)), h('span', {}, 'Signifikanz der Unterschiede')),
    h('div', { class: 'kpi' }, h('b', {}, pctDelta(1 + s.overallChangePct / 100)), h('span', {}, 'Gesamtsensitivität')),
  );

  const rows = r.perDirection.map((d, k) => h('tr', {},
    h('td', {}, dirLong(k, n), k === s.referenceIndex ? h('span', { class: 'badge', title: 'Bezugsrichtung' }, ' Bezug') : ''),
    h('td', { class: 'r num' }, fmt0(d.n)),
    h('td', { class: `r num ${d.shrunk > 1.005 ? 'over' : d.shrunk < 0.995 ? 'under' : ''}` }, pctDelta(d.shrunk)),
    h('td', { class: 'r num muted' }, `${pctDelta(d.ratioLow)} … ${pctDelta(d.ratioHigh)}`),
    h('td', { class: 'r num' }, gainText(view.final[k])),
    h('td', { class: 'r num muted' }, pctDelta(s.predictedRatio[k])),
  ));
  $('dir-table').replaceChildren(h('table', {},
    h('thead', {}, h('tr', {},
      h('th', {}, 'Richtung'), h('th', { class: 'r' }, 'Ziele'), h('th', { class: 'r' }, 'Landung'),
      h('th', { class: 'r' }, '95 %-Bereich'), h('th', { class: 'r' }, 'Faktor'), h('th', { class: 'r' }, 'danach'))),
    h('tbody', {}, ...rows)));
}

let previewTimer = 0;
function schedulePreview() {
  clearTimeout(previewTimer);
  previewTimer = setTimeout(async () => {
    try {
      const p = await api('/api/calibration/preview', {
        method: 'POST',
        body: { id: view.res.id, reference: view.reference, strength: view.strength },
      });
      view.suggestion = p.suggestion;
      view.final = p.final;
      renderCorrection();
    } catch (e) {
      $('save-msg').replaceChildren(h('div', { class: 'alert error' }, e.message));
    }
  }, 120);
}

async function saveProfile(name, btn, msg) {
  const target = document.querySelector('input[name="target"]:checked')?.value || 'default';
  btn.disabled = true;
  try {
    const p = await api('/api/calibration/save', {
      method: 'POST',
      body: { id: view.res.id, reference: view.reference, strength: view.strength, name: name.trim(), target },
    });
    const where = target === 'device' ? 'für diese Maus aktiv' : target === 'none' ? 'gespeichert (nicht aktiviert)' : 'für alle Mäuse aktiv';
    msg.replaceChildren(h('div', { class: 'alert ok stack' },
      h('strong', {}, `„${p.name}“ ist ${where}.`),
      h('div', {}, 'Teste es im Spiel. Für Feinschliff später „Verfeinern / prüfen“ wählen.'),
      h('div', { class: 'row' },
        h('a', { class: 'btn primary', href: '/' }, 'Zum Dashboard'),
        h('a', { class: 'btn', href: '/calibrate' }, 'Neue Messung'))));
    btn.textContent = 'Gespeichert';
  } catch (e) {
    btn.disabled = false;
    msg.replaceChildren(h('div', { class: 'alert error' }, e.message));
  }
}

// --------------------------------------------------------------- test hook --

if (new URLSearchParams(location.search).has('test')) {
  // Lets automated tests submit synthetic sessions through the normal path.
  window.__omniTest = {
    submit(payload) {
      settings = payload.settings;
      return submit(payload);
    },
    startWithoutLock() {
      requireLock = false;
      startCalibration();
    },
    state() {
      return { phase, round, pos: session?.pos, current: session?.current, cpd: session?.cpd, trials: session?.trials.length };
    },
  };
}

show('ov-setup');
initSetup();
