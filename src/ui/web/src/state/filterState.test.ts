import { beforeEach, describe, expect, it } from 'vitest';
import { DEFAULT_FILTER_STATE, FILTER_STORAGE_KEY, loadFilterState, nextSort, sanitizeFilterState, saveFilterState } from './filterState';

describe('filter state persistence', () => {
  beforeEach(() => localStorage.clear());

  it('round-trips through localStorage', () => {
    saveFilterState({ filter: 'host:api.example.test', quick: 'image', sort: 'size', desc: true });
    expect(loadFilterState()).toEqual({ filter: 'host:api.example.test', quick: 'image', sort: 'size', desc: true });
  });

  it('falls back to defaults for missing or corrupt data', () => {
    expect(loadFilterState()).toEqual(DEFAULT_FILTER_STATE);
    localStorage.setItem(FILTER_STORAGE_KEY, '{not json');
    expect(loadFilterState()).toEqual(DEFAULT_FILTER_STATE);
  });

  it('rejects invalid values field by field', () => {
    expect(sanitizeFilterState({ filter: 42, quick: 'bogus', sort: 'nope', desc: 'yes' })).toEqual(DEFAULT_FILTER_STATE);
    expect(sanitizeFilterState({ filter: 'x', quick: 'dns' })).toEqual({ ...DEFAULT_FILTER_STATE, filter: 'x', quick: 'dns' });
  });

  it('survives unavailable storage', () => {
    const orig = Storage.prototype.getItem;
    const origSet = Storage.prototype.setItem;
    Storage.prototype.getItem = () => {
      throw new Error('SecurityError');
    };
    Storage.prototype.setItem = () => {
      throw new Error('QuotaExceededError');
    };
    try {
      expect(loadFilterState()).toEqual(DEFAULT_FILTER_STATE);
      expect(() => saveFilterState(DEFAULT_FILTER_STATE)).not.toThrow();
    } finally {
      Storage.prototype.getItem = orig;
      Storage.prototype.setItem = origSet;
    }
  });
});

describe('nextSort', () => {
  it('cycles ascending → descending → capture order', () => {
    let s = { sort: 'seq' as const, desc: false } as { sort: Parameters<typeof nextSort>[1]; desc: boolean };
    s = nextSort(s, 'status');
    expect(s).toEqual({ sort: 'status', desc: false });
    s = nextSort(s, 'status');
    expect(s).toEqual({ sort: 'status', desc: true });
    s = nextSort(s, 'status');
    expect(s).toEqual({ sort: 'seq', desc: false });
    expect(nextSort({ sort: 'size', desc: true }, 'host')).toEqual({ sort: 'host', desc: false });
  });
});
