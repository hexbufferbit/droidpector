import { describe, expect, it } from 'vitest';
import { computeWindow, isAtBottom, pagesFor, scrollTopToReveal } from './virtual';

describe('computeWindow', () => {
  it('renders only the visible rows plus overscan', () => {
    const w = computeWindow(0, 240, 24, 100_000, 5);
    expect(w.start).toBe(0);
    expect(w.end).toBe(16); // 11 visible + 5 overscan
    expect(w.totalHeight).toBe(2_400_000);
  });

  it('windows in the middle of a huge list', () => {
    const w = computeWindow(24 * 50_000, 480, 24, 100_000, 10);
    expect(w.start).toBe(49_990);
    expect(w.end).toBe(50_031);
    expect(w.end - w.start).toBeLessThan(100);
  });

  it('clamps at the end of the list', () => {
    const w = computeWindow(24 * 99_990, 480, 24, 100_000, 10);
    expect(w.end).toBe(100_000);
    expect(w.start).toBe(99_980);
  });

  it('handles empty lists and bad input', () => {
    expect(computeWindow(0, 500, 24, 0)).toEqual({ start: 0, end: 0, totalHeight: 0 });
    expect(computeWindow(-50, 500, 24, 3)).toMatchObject({ start: 0, end: 3 });
  });
});

describe('pagesFor', () => {
  it('lists pages covering a range', () => {
    expect(pagesFor(0, 50, 100)).toEqual([0]);
    expect(pagesFor(90, 130, 100)).toEqual([0, 1]);
    expect(pagesFor(200, 200, 100)).toEqual([]);
    expect(pagesFor(250, 400, 100)).toEqual([2, 3]);
  });
});

describe('isAtBottom / scrollTopToReveal', () => {
  it('detects the bottom with slack', () => {
    expect(isAtBottom(760, 240, 1000)).toBe(true);
    expect(isAtBottom(757, 240, 1000)).toBe(true);
    expect(isAtBottom(700, 240, 1000)).toBe(false);
  });

  it('scrolls minimally to reveal a row', () => {
    expect(scrollTopToReveal(5, 24, 0, 240)).toBeNull();
    expect(scrollTopToReveal(20, 24, 0, 240)).toBe(21 * 24 - 240);
    expect(scrollTopToReveal(2, 24, 240, 240)).toBe(48);
  });
});
