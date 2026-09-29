import { describe, expect, it } from 'vitest';
import type { Status } from '../api/types';
import { expectedBootMs, installProgress, recordBoot } from './installProgress';

const at = (state: Status['state'], sinceMsAgo: number, app?: string): [Status, number] => {
  const now = Date.parse('2026-09-29T10:00:00Z');
  const st = { state, since: new Date(now - sinceMsAgo).toISOString(), app: app ? { package: app, running: true } : undefined } as Status;
  return [st, now];
};

describe('installProgress', () => {
  it('advances through boot, install and launch', () => {
    const boot = 100_000;
    const seq = [
      installProgress(at("preparing", 0)[0], "p", at("preparing", 0)[1], boot),
      installProgress(at('booting', 10_000)[0], 'p', at('booting', 10_000)[1], boot),
      installProgress(at('booting', 60_000)[0], 'p', at('booting', 60_000)[1], boot),
      installProgress(at('provisioning', 5_000)[0], 'p', at('provisioning', 5_000)[1], boot),
      installProgress(at('installing', 10_000)[0], 'p', at('installing', 10_000)[1], boot),
      installProgress(at('launching', 1_000)[0], 'p', at('launching', 1_000)[1], boot),
      installProgress(at('ready', 0, 'p')[0], 'p', at('ready', 0, 'p')[1], boot),
    ];
    for (let i = 1; i < seq.length; i++) expect(seq[i].pct).toBeGreaterThan(seq[i - 1].pct);
    expect(seq.map((s) => s.step)).toEqual([1, 1, 1, 1, 2, 3, 3]);
    expect(seq[seq.length - 1].pct).toBe(100);
  });

  it('never claims completion while a slow boot overruns', () => {
    const [st, now] = at('booting', 3_600_000);
    const p = installProgress(st, 'p', now, 100_000);
    expect(p.pct).toBeLessThan(65);
    expect(p.pct).toBeGreaterThan(55);
  });

  it('uses the measured boot time when known', () => {
    expect(expectedBootMs(true)).toBe(120_000);
    recordBoot(200_000);
    expect(expectedBootMs(true)).toBe(200_000);
    recordBoot(9_000); // a quick start is not a boot
    expect(expectedBootMs(true)).toBe(200_000);
  });
});
