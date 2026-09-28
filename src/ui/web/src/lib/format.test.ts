import { describe, expect, it } from 'vitest';
import { formatBytes, formatClock, formatDuration, formatElapsed, pluralize } from './format';

describe('formatBytes (DevTools style)', () => {
  it.each([
    [0, '0 B'],
    [999, '999 B'],
    [1000, '1.0 kB'],
    [1536, '1.5 kB'],
    [99_949, '99.9 kB'],
    [123_456, '123 kB'],
    [1_000_000, '1.0 MB'],
    [12_345_678, '12.3 MB'],
    [345_000_000, '345 MB'],
    [2_500_000_000, '2.5 GB'],
  ])('%d → %s', (n, s) => expect(formatBytes(n)).toBe(s));

  it('handles invalid input', () => {
    expect(formatBytes(-1)).toBe('—');
    expect(formatBytes(Number.NaN)).toBe('—');
  });
});

describe('formatDuration', () => {
  it.each([
    [-1, 'n/a'],
    [0, '0 ms'],
    [0.358, '0.36 ms'],
    [1, '1 ms'],
    [12.34, '12.3 ms'],
    [245.6, '246 ms'],
    [1000, '1 s'],
    [1250, '1.25 s'],
    [59_990, '59.99 s'],
    [90_000, '1.5 min'],
    [5_400_000, '1.5 h'],
  ])('%d ms → %s', (ms, s) => expect(formatDuration(ms)).toBe(s));
});

describe('formatElapsed', () => {
  it.each([
    [0, '0s'],
    [45_000, '45s'],
    [12 * 60_000 + 5_000, '12m'],
    [3_600_000, '1h'],
    [3_900_000, '1h 5m'],
    [50 * 3_600_000, '2d 2h'],
  ])('%d → %s', (ms, s) => expect(formatElapsed(ms)).toBe(s));
});

describe('formatClock', () => {
  it('formats local time with milliseconds', () => {
    const d = new Date(2026, 8, 28, 9, 5, 7, 42);
    expect(formatClock(d)).toBe('09:05:07.042');
    expect(formatClock('not a date')).toBe('');
  });
});

describe('pluralize', () => {
  it('pluralizes and groups', () => {
    expect(pluralize('request', 1)).toBe('1 request');
    expect(pluralize('request', 382)).toBe('382 requests');
    expect(pluralize('domain', 12345)).toBe('12,345 domains');
  });
});
