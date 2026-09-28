import { describe, expect, it } from 'vitest';
import { ancestors, childPath, containerPaths, defaultExpanded, searchJson, splitHighlight, summarize, type Json } from './jsonTree';

const doc: Json = { id: 42, name: 'droidpector', tags: ['android', 'network'], nested: { ok: true, n: 1.5, deep: { token: 'secret-Token' } }, 'a/b': null };

describe('paths', () => {
  it('builds JSON pointer paths with escaping', () => {
    expect(childPath('', 'a/b')).toBe('/a~1b');
    expect(childPath('/x', 'm~n')).toBe('/x/m~0n');
    expect(childPath('/arr', 3)).toBe('/arr/3');
  });

  it('lists ancestors root first', () => {
    expect(ancestors('')).toEqual([]);
    expect(ancestors('/a')).toEqual(['']);
    expect(ancestors('/a/0/b')).toEqual(['', '/a', '/a/0']);
  });

  it('collects every container for expand all', () => {
    expect(containerPaths(doc)).toEqual(['', '/tags', '/nested', '/nested/deep']);
  });

  it('expands to a default depth', () => {
    expect([...defaultExpanded(doc, 0)]).toEqual(['']);
    expect([...defaultExpanded(doc, 1)].sort()).toEqual(['', '/nested', '/tags']);
  });
});

describe('searchJson', () => {
  it('finds keys and values case-insensitively and expands their ancestors', () => {
    const r = searchJson(doc, 'TOKEN');
    expect(r.matches).toEqual(['/nested/deep/token']);
    expect([...r.expand].sort()).toEqual(['', '/nested', '/nested/deep']);
  });

  it('matches array values, numbers and null', () => {
    expect(searchJson(doc, 'network').matches).toEqual(['/tags/1']);
    expect(searchJson(doc, '1.5').matches).toEqual(['/nested/n']);
    expect(searchJson(doc, 'null').matches).toEqual(['/a~1b']);
  });

  it('returns nothing for an empty query and honours the limit', () => {
    expect(searchJson(doc, '  ').matches).toEqual([]);
    const big: Json = Array.from({ length: 50 }, () => 'x');
    const r = searchJson(big, 'x', 10);
    expect(r.matches).toHaveLength(10);
    expect(r.limited).toBe(true);
  });
});

describe('helpers', () => {
  it('summarizes containers', () => {
    expect(summarize([1, 2, 3])).toBe('Array(3)');
    expect(summarize({ a: 1 })).toBe('{1}');
  });

  it('splits text around matches', () => {
    expect(splitHighlight('Hello hello', 'ell')).toEqual([
      { text: 'H', hit: false },
      { text: 'ell', hit: true },
      { text: 'o h', hit: false },
      { text: 'ell', hit: true },
      { text: 'o', hit: false },
    ]);
    expect(splitHighlight('abc', '')).toEqual([{ text: 'abc', hit: false }]);
  });
});
