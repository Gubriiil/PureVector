// Run with: node --test web/test/
import test from 'node:test';
import assert from 'node:assert/strict';
import { Session, countsPerDegree, mulberry32 } from '../js/session.js';

const cpd = countsPerDegree(800, 40);

test('counts per degree', () => {
  assert.ok(Math.abs(cpd - 34.995) < 0.01);
});

test('targets alternate out/return and directions are balanced', () => {
  const s = new Session({ cpd, rng: mulberry32(1) });
  let t = 0;
  s.startRound(1, t);
  const bins = new Array(16).fill(0);
  for (let i = 0; i < 320; i++) {
    const c = s.current;
    // Teleport onto the target and shoot.
    s.move(c.tx - s.pos.x, c.ty - s.pos.y, (t += 300));
    assert.equal(s.click((t += 10)), 'hit');
    if (c.kind === 'out') {
      const deg = (Math.atan2(-c.ty, c.tx) * 180) / Math.PI;
      bins[Math.floor((((deg % 360) + 360) % 360) / 22.5)]++;
      const dist = Math.hypot(c.tx, c.ty) / cpd;
      assert.ok(dist >= 8 - 1e-9 && dist <= 28 + 1e-9, `distance ${dist}`);
    }
  }
  assert.equal(s.trials.length, 320);
  for (let i = 0; i < s.trials.length; i++) assert.equal(s.trials[i].kind, i % 2 === 0 ? 'out' : 'return');
  // 160 outward targets over 16 bins: exactly 10 per bin (stratified).
  assert.deepEqual(bins, new Array(16).fill(10));
});

test('samples are relative to the spawn position, one per millisecond', () => {
  const s = new Session({ cpd, rng: mulberry32(2) });
  s.startRound(1, 1000);
  s.move(3, -1, 1000.2);
  s.move(2, 0, 1000.7); // same ms → merged
  s.move(5, 5, 1003.1);
  const c = s.current;
  assert.deepEqual(c.samples, [[0, 5, -1], [3, 10, 4]]);
  assert.equal(s.click(1004), 'miss');
  assert.deepEqual(c.clicks, [4]);
});

test('timeouts end a target and continue with the next one', () => {
  const s = new Session({ cpd, rng: mulberry32(3), timeoutMs: 3000 });
  s.startRound(1, 0);
  assert.equal(s.tick(2999), null);
  assert.equal(s.tick(3000), 'timeout');
  assert.equal(s.trials[0].hit, false);
  assert.equal(s.current.kind, 'return');
});

test('payload matches the server format', () => {
  const s = new Session({ cpd, rng: mulberry32(4) });
  s.startRound(1, 0);
  const p = s.payload({ dpi: 800, cm360: 40, fov: 103, rounds: 5, roundSeconds: 60, directions: 8 }, { unadjusted: true });
  assert.equal(p.version, 1);
  assert.equal(p.settings.countsPerDeg, cpd);
  assert.ok(Array.isArray(p.trials));
});
