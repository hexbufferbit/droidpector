import { describe, expect, it } from 'vitest';
import { backoffDelay } from './ws';

describe('backoffDelay', () => {
  it('grows exponentially up to the maximum', () => {
    expect(backoffDelay(0)).toBe(500);
    expect(backoffDelay(1)).toBe(1000);
    expect(backoffDelay(3)).toBe(4000);
    expect(backoffDelay(10)).toBe(10_000);
    expect(backoffDelay(1000)).toBe(10_000);
  });
});
