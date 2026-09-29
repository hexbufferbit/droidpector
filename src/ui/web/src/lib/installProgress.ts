import type { Status } from '../api/types';
import { loadJSON, saveJSON } from './storage';

/** Where the one-click install is, derived from the sandbox status. */
export interface InstallProgress {
  /** 0–100 */
  pct: number;
  /** 1-based step of STEPS */
  step: number;
  label: string;
}

export const STEPS = ['Start Android', 'Install app', 'Launch app'] as const;

const BOOT_KEY = 'droidpector.bootMs';

/** Expected Android boot time: the last measured one, or a default by acceleration. */
export function expectedBootMs(accelerated: boolean): number {
  const v = loadJSON<number>(BOOT_KEY, 0, (x): x is number => typeof x === 'number' && x > 0);
  return v || (accelerated ? 120_000 : 480_000);
}

/**
 * Remembers how long the last full boot took so the next estimate is accurate.
 * Quick starts (restoring the saved state, seconds) are not boots: recording
 * them would make the next cold boot look stuck near the end of its phase.
 */
export function recordBoot(ms: number): void {
  if (ms > 30_000 && ms < 3_600_000) saveJSON(BOOT_KEY, Math.round(ms));
}

// A phase that runs longer than expected creeps toward its end but never reaches it.
function ease(elapsed: number, expected: number): number {
  const x = Math.max(0, elapsed) / Math.max(1, expected);
  return x < 0.8 ? x : 0.8 + 0.19 * (1 - Math.exp(-(x - 0.8) * 2));
}

/** installProgress maps the sandbox state (and time spent in it) to a percentage. */
export function installProgress(status: Status | null, pkg: string | undefined, now: number, bootMs: number): InstallProgress {
  const elapsed = status ? now - Date.parse(status.since) : 0;
  const span = (from: number, to: number, expected: number) => Math.round(from + (to - from) * ease(elapsed, expected));
  switch (status?.state) {
    case 'preparing':
    case 'starting':
      return { pct: span(1, 5, 10_000), step: 1, label: 'Preparing the Android sandbox' };
    case 'booting':
      return { pct: span(5, 65, bootMs), step: 1, label: 'Booting Android' };
    case 'recovering':
      return { pct: span(5, 65, bootMs), step: 1, label: 'Restarting Android' };
    case 'provisioning':
      return { pct: span(65, 75, 30_000), step: 1, label: 'Preparing network inspection' };
    case 'installing':
      return { pct: span(75, 92, 45_000), step: 2, label: 'Installing the app' };
    case 'launching':
      return { pct: span(92, 99, 10_000), step: 3, label: 'Launching the app' };
    case 'ready':
      if (pkg && status.app?.package === pkg) return { pct: 100, step: 3, label: 'App running' };
      return { pct: 75, step: 2, label: 'Installing the app' };
    default:
      return { pct: 1, step: 1, label: 'Starting' };
  }
}
