// German number formatting and direction names shared by all pages.

const nf1 = new Intl.NumberFormat('de-DE', { minimumFractionDigits: 1, maximumFractionDigits: 1 });
const nf2 = new Intl.NumberFormat('de-DE', { minimumFractionDigits: 2, maximumFractionDigits: 2 });
const nf0 = new Intl.NumberFormat('de-DE', { maximumFractionDigits: 0 });

export const fmt0 = (v) => (Number.isFinite(v) ? nf0.format(v) : '–');
export const fmt1 = (v) => (Number.isFinite(v) ? nf1.format(v) : '–');
export const fmt2 = (v) => (Number.isFinite(v) ? nf2.format(v) : '–');

/** Signed percentage of a ratio: 1.062 → "+6,2 %". */
export function pctDelta(ratio) {
  if (!Number.isFinite(ratio)) return '–';
  const p = (ratio - 1) * 100;
  const s = nf1.format(Math.abs(p));
  if (Math.abs(p) < 0.05) return '±0,0 %';
  return (p > 0 ? '+' : '−') + s + ' %';
}

/** Gain as multiplier: 0.957 → "0,96×". */
export const gainText = (g) => (Number.isFinite(g) ? nf2.format(g) + '×' : '–');

const ARROWS = ['→', '↗', '↑', '↖', '←', '↙', '↓', '↘'];
const NAMES = ['rechts', 'rechts oben', 'oben', 'links oben', 'links', 'links unten', 'unten', 'rechts unten'];

/** Arrow (or degrees) for anchor k of n. */
export function dirShort(k, n) {
  if (n === 8) return ARROWS[k];
  return `${fmt0((k * 360) / n)}°`;
}

/** Long label for anchor k of n, e.g. "↗ rechts oben". */
export function dirLong(k, n) {
  if (n === 8) return `${ARROWS[k]} ${NAMES[k]}`;
  return `${fmt1((k * 360) / n)}°`;
}

/** Name of the 8-way direction closest to an angle in degrees. */
export function nearestDirName(deg) {
  const k = ((Math.round(deg / 45) % 8) + 8) % 8;
  return `${ARROWS[k]} ${NAMES[k]}`;
}

export function fmtDate(iso) {
  if (!iso) return '';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime()) || d.getFullYear() < 2000) return '';
  return d.toLocaleString('de-DE', { dateStyle: 'medium', timeStyle: 'short' });
}

export function escapeHTML(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]);
}

/** Tiny DOM helper: h('div', {class: 'x'}, child, 'text'). */
export function h(tag, attrs = {}, ...children) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === undefined || v === null || v === false) continue;
    if (k === 'class') el.className = v;
    else if (k.startsWith('on') && typeof v === 'function') el.addEventListener(k.slice(2), v);
    else if (k === 'dataset') Object.assign(el.dataset, v);
    else if (v === true) el.setAttribute(k, '');
    else el.setAttribute(k, v);
  }
  for (const c of children.flat()) {
    if (c === undefined || c === null || c === false) continue;
    el.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return el;
}
