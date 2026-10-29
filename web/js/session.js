// Calibration task logic (no DOM): target layout, recording and trial
// bookkeeping. Mirrors the Spidershot pattern used by PureVector: targets
// alternate between a random direction ("out") and the centre ("return").
//
// All positions are raw mouse counts (x right, y down). The crosshair
// position is the running sum of mouse deltas.

export const DEFAULTS = {
  dpi: 800,
  cm360: 40,
  fov: 103,
  rounds: 5,
  roundSeconds: 60,
  directions: 8,
  minDeg: 8,
  maxDeg: 28,
  radiusDeg: 1.4,
  bins: 16,
  timeoutMs: 3000,
};

/** Counts per degree of camera rotation for a DPI and cm/360° setting. */
export function countsPerDegree(dpi, cm360) {
  return ((dpi / 2.54) * cm360) / 360;
}

/** Deterministic PRNG (mulberry32) for reproducible tests. */
export function mulberry32(seed) {
  let a = seed >>> 0;
  return function () {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

function shuffle(arr, rng) {
  for (let i = arr.length - 1; i > 0; i--) {
    const j = Math.floor(rng() * (i + 1));
    [arr[i], arr[j]] = [arr[j], arr[i]];
  }
  return arr;
}

const r2 = (v) => Math.round(v * 100) / 100;

export class Session {
  /**
   * @param {object} opts
   * @param {number} opts.cpd counts per degree
   * @param {number} [opts.minDeg] nearest target distance (°)
   * @param {number} [opts.maxDeg] farthest target distance (°)
   * @param {number} [opts.radiusDeg] target radius (°)
   * @param {number} [opts.bins] direction strata (balanced sampling)
   * @param {number} [opts.timeoutMs] give up on a target after this time
   * @param {() => number} [opts.rng]
   */
  constructor(opts) {
    this.cpd = opts.cpd;
    this.minDeg = opts.minDeg ?? DEFAULTS.minDeg;
    this.maxDeg = opts.maxDeg ?? DEFAULTS.maxDeg;
    this.radiusDeg = opts.radiusDeg ?? DEFAULTS.radiusDeg;
    this.bins = opts.bins ?? DEFAULTS.bins;
    this.timeoutMs = opts.timeoutMs ?? DEFAULTS.timeoutMs;
    this.rng = opts.rng ?? Math.random;
    this.pos = { x: 0, y: 0 };
    this.trials = [];
    this.current = null;
    this.round = 0;
    this.bag = [];
    this.stats = { hits: 0, misses: 0, timeouts: 0, events: 0, eventTime: 0 };
    this.roundStats = { hits: 0, misses: 0 };
  }

  /** Start a round: the crosshair is re-centred and the first target spawns. */
  startRound(round, t) {
    this.round = round;
    this.pos = { x: 0, y: 0 };
    this.roundStats = { hits: 0, misses: 0 };
    this.spawn('out', t);
  }

  /** End the round; an unfinished target is discarded. */
  endRound() {
    this.current = null;
  }

  get radius() {
    return this.radiusDeg * this.cpd;
  }

  spawn(kind, t) {
    let tx = 0;
    let ty = 0;
    if (kind === 'out') {
      if (!this.bag.length) this.bag = shuffle([...Array(this.bins).keys()], this.rng);
      const bin = this.bag.pop();
      const theta = ((bin + this.rng()) * 2 * Math.PI) / this.bins;
      const logMin = Math.log(this.minDeg);
      const dDeg = Math.exp(logMin + this.rng() * (Math.log(this.maxDeg) - logMin));
      const d = dDeg * this.cpd;
      tx = d * Math.cos(theta);
      ty = -d * Math.sin(theta); // screen y grows downwards
    }
    this.current = {
      kind,
      round: this.round,
      spawnT: t,
      sx: r2(this.pos.x),
      sy: r2(this.pos.y),
      tx: r2(tx),
      ty: r2(ty),
      r: r2(this.radius),
      hit: false,
      endT: 0,
      samples: [],
      clicks: [],
    };
    return this.current;
  }

  /** Apply one mouse delta observed at time t (ms, same clock as spawn). */
  move(dx, dy, t) {
    if (!dx && !dy) return;
    this.pos.x += dx;
    this.pos.y += dy;
    this.stats.events++;
    const c = this.current;
    if (!c) return;
    const ms = Math.max(0, Math.floor(t - c.spawnT));
    const x = r2(this.pos.x - c.sx);
    const y = r2(this.pos.y - c.sy);
    const s = c.samples;
    const last = s[s.length - 1];
    if (last && last[0] >= ms) {
      last[1] = x;
      last[2] = y;
    } else {
      s.push([ms, x, y]);
    }
  }

  /** Distance from the crosshair to the current target centre (counts). */
  targetDistance() {
    const c = this.current;
    if (!c) return Infinity;
    return Math.hypot(this.pos.x - c.tx, this.pos.y - c.ty);
  }

  /** A shot at time t. Returns 'hit', 'miss' or null (no target). */
  click(t) {
    const c = this.current;
    if (!c) return null;
    const dt = Math.max(0, t - c.spawnT);
    c.clicks.push(Math.round(dt * 10) / 10);
    if (this.targetDistance() <= c.r) {
      c.hit = true;
      c.endT = Math.round(dt * 10) / 10;
      this.trials.push(c);
      this.stats.hits++;
      this.roundStats.hits++;
      this.spawn(c.kind === 'out' ? 'return' : 'out', t);
      return 'hit';
    }
    this.stats.misses++;
    this.roundStats.misses++;
    return 'miss';
  }

  /** Advance time; times out a target that was not hit. */
  tick(t) {
    const c = this.current;
    if (!c || t - c.spawnT < this.timeoutMs) return null;
    c.endT = this.timeoutMs;
    this.trials.push(c);
    this.stats.timeouts++;
    this.spawn(c.kind === 'out' ? 'return' : 'out', t);
    return 'timeout';
  }

  /** Upload payload in the format of calib.Session. */
  payload(settings, client) {
    return {
      version: 1,
      settings: { ...settings, countsPerDeg: this.cpd, minDeg: this.minDeg, maxDeg: this.maxDeg, radiusDeg: this.radiusDeg },
      client,
      trials: this.trials,
    };
  }
}
