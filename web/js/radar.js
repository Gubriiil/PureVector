// SVG polar chart for per-direction values (landing ratios or gains).
// Angles follow the profile convention: 0° = right, counter-clockwise, up = 90°.

const NS = 'http://www.w3.org/2000/svg';

/** Chart colours (mirrors the CSS variables in app.css). */
export const COLORS = {
  accent: '#2dd4bf',
  accentFill: 'rgba(45,212,191,0.12)',
  over: '#fb923c',
  under: '#38bdf8',
  muted: '#95a1b2',
  faint: '#5f6b7d',
  text: '#e8edf4',
};

function el(tag, attrs, parent) {
  const e = document.createElementNS(NS, tag);
  for (const [k, v] of Object.entries(attrs)) e.setAttribute(k, v);
  if (parent) parent.appendChild(e);
  return e;
}

/**
 * Periodic Catmull-Rom interpolation in the log domain – the same curve the
 * OmniVector engine uses between anchor directions.
 */
export function interpolate(values, deg) {
  const n = values.length;
  const logs = values.map((v) => Math.log(Math.max(v, 1e-6)));
  const step = 360 / n;
  let a = deg % 360;
  if (a < 0) a += 360;
  const s = a / step;
  const k = Math.floor(s);
  const t = s - k;
  const p = (i) => logs[((i % n) + n) % n];
  const p0 = p(k - 1), p1 = p(k), p2 = p(k + 1), p3 = p(k + 2);
  const t2 = t * t, t3 = t2 * t;
  const v = 0.5 * (2 * p1 + (p2 - p0) * t + (2 * p0 - 5 * p1 + 4 * p2 - p3) * t2 + (3 * p1 - p0 - 3 * p2 + p3) * t3);
  return Math.exp(v);
}

/**
 * Render a chart into an <svg>. Returns an object with setLive(deg, value).
 *
 * opts.series: [{ values, color, fill, width, dash, smooth = true, dots = true }]
 * opts.points: [{ deg, value, color, opacity }]
 */
export function renderRadar(svg, opts) {
  const n = opts.n;
  const compact = !!opts.compact;
  const size = 400;
  const c = size / 2;
  const R = compact ? 178 : 148;
  const min = opts.min ?? 0.8;
  const max = opts.max ?? 1.2;
  const rings = opts.rings ?? [0.9, 1.0, 1.1];
  const ref = opts.ref ?? 1;
  svg.setAttribute('viewBox', `0 0 ${size} ${size}`);
  svg.classList.add('radar');
  svg.replaceChildren();
  if (opts.title) el('title', {}, svg).textContent = opts.title;

  const radius = (v) => R * Math.min(1.05, Math.max(0, (v - min) / (max - min)));
  const xy = (deg, v) => {
    const a = (deg * Math.PI) / 180;
    const r = radius(v);
    return [c + r * Math.cos(a), c - r * Math.sin(a)];
  };

  el('circle', { cx: c, cy: c, r: R, fill: 'rgba(255,255,255,0.015)', stroke: 'none' }, svg);
  // Label only the inner, reference and outer ring, between two spokes.
  const labelled = opts.labelRings ?? [rings[0], ref, rings[rings.length - 1]];
  const labelDeg = 90 + 180 / n;
  for (const rv of rings) {
    el('circle', { cx: c, cy: c, r: radius(rv), class: rv === ref ? 'ring ref' : 'ring' }, svg);
    if (!compact && opts.ringLabel && labelled.includes(rv)) {
      const [x, y] = xy(labelDeg, rv);
      const t = el('text', { x, y: y - 3, class: 'ringlabel', 'text-anchor': 'middle' }, svg);
      t.textContent = opts.ringLabel(rv);
    }
  }
  for (let k = 0; k < n; k++) {
    const deg = (k * 360) / n;
    const [x, y] = xy(deg, max);
    el('line', { x1: c, y1: c, x2: x, y2: y, class: 'spoke' }, svg);
    if (!compact && opts.labels) {
      const a = (deg * Math.PI) / 180;
      const t = el('text', { x: c + (R + 24) * Math.cos(a), y: c - (R + 24) * Math.sin(a), class: 'label' }, svg);
      t.textContent = opts.labels(k);
    }
  }

  for (const p of opts.points ?? []) {
    const [x, y] = xy(p.deg, p.value);
    el('circle', { cx: x, cy: y, r: compact ? 2 : 2.4, fill: p.color, 'fill-opacity': p.opacity ?? 0.45 }, svg);
  }

  for (const s of opts.series ?? []) {
    if (!s.values || s.values.length !== n) continue;
    let d = '';
    if (s.smooth === false) {
      s.values.forEach((v, k) => {
        const [x, y] = xy((k * 360) / n, v);
        d += (k ? 'L' : 'M') + x.toFixed(1) + ' ' + y.toFixed(1);
      });
    } else {
      for (let i = 0; i <= 180; i++) {
        const deg = i * 2;
        const [x, y] = xy(deg, interpolate(s.values, deg));
        d += (i ? 'L' : 'M') + x.toFixed(1) + ' ' + y.toFixed(1);
      }
    }
    d += 'Z';
    el('path', {
      d,
      fill: s.fill ?? 'none',
      stroke: s.color,
      'stroke-width': s.width ?? 2.2,
      'stroke-dasharray': s.dash ?? 'none',
      'stroke-linejoin': 'round',
    }, svg);
    if (s.dots !== false && !compact) {
      s.values.forEach((v, k) => {
        const [x, y] = xy((k * 360) / n, v);
        el('circle', { cx: x, cy: y, r: 3.6, fill: s.color }, svg);
      });
    }
  }

  // Live indicator.
  const live = el('g', { opacity: 0 }, svg);
  const arrow = el('line', { x1: c, y1: c, x2: c, y2: c, class: 'live-arrow' }, live);
  const knob = el('circle', { cx: c, cy: c, r: 6, fill: opts.liveColor ?? COLORS.text }, live);
  el('circle', { cx: c, cy: c, r: 3, class: 'center-dot' }, svg);

  return {
    setLive(deg, value, visible = true) {
      if (!visible || !Number.isFinite(deg)) {
        live.setAttribute('opacity', 0);
        return;
      }
      const [x, y] = xy(deg, value);
      arrow.setAttribute('x2', x);
      arrow.setAttribute('y2', y);
      knob.setAttribute('cx', x);
      knob.setAttribute('cy', y);
      live.setAttribute('opacity', 1);
    },
  };
}

/** Pick a symmetric value range that fits all values around 1. */
export function autoRange(values, minSpan = 0.12) {
  let dev = minSpan;
  for (const v of values) if (Number.isFinite(v)) dev = Math.max(dev, Math.abs(v - 1) * 1.15);
  dev = Math.ceil(dev * 20) / 20; // round up to 5 % steps
  return { min: 1 - 2 * dev, max: 1 + dev * 1.0, rings: ringSet(dev) };
}

function ringSet(dev) {
  const r = (v) => Math.round(v * 1000) / 1000;
  return [r(1 - dev), r(1 - dev / 2), 1, r(1 + dev / 2), r(1 + dev)];
}
