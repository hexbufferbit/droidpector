import { describe, expect, it } from 'vitest';
import { formatMarkup, tokenizeMarkup } from './markup';

describe('formatMarkup', () => {
  it('indents nested elements and keeps short text inline', () => {
    const out = formatMarkup('<html><head><title>Hi</title></head><body><p>Hello</p><br><img src="x"/></body></html>');
    expect(out).toBe(['<html>', '  <head>', '    <title>Hi</title>', '  </head>', '  <body>', '    <p>Hello</p>', '    <br>', '    <img src="x"/>', '  </body>', '</html>'].join('\n'));
  });

  it('keeps script content verbatim', () => {
    const out = formatMarkup('<div><script>if (a < b) { x(); }</script></div>');
    expect(out).toContain('if (a < b) { x(); }');
  });
});

describe('tokenizeMarkup', () => {
  it('is lossless and classifies tokens', () => {
    const src = '<!-- c --><a href="/x" data-y=\'1\'>text</a>';
    const toks = tokenizeMarkup(src);
    expect(toks.map((t) => t.text).join('')).toBe(src);
    expect(toks.find((t) => t.type === 'comment')?.text).toBe('<!-- c -->');
    expect(toks.filter((t) => t.type === 'tag').map((t) => t.text)).toEqual(['a', 'a']);
    expect(toks.filter((t) => t.type === 'attr').map((t) => t.text)).toEqual(['href', 'data-y']);
    expect(toks.filter((t) => t.type === 'value').map((t) => t.text)).toEqual(['"/x"', "'1'"]);
  });
});
