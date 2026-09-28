import { describe, expect, it } from 'vitest';
import { hexdump, hexdumpLine } from './hexdump';

describe('hexdump', () => {
  it('formats offset, hex and ascii columns', () => {
    const bytes = new TextEncoder().encode('Hello, World!\n\x00\x01ABC');
    const d = hexdump(bytes);
    const lines = d.text.split('\n');
    expect(lines).toHaveLength(2);
    expect(lines[0]).toBe('00000000  48 65 6c 6c 6f 2c 20 57  6f 72 6c 64 21 0a 00 01  |Hello, World!...|');
    expect(lines[1].startsWith('00000010  41 42 43 ')).toBe(true);
    expect(lines[1].endsWith('|ABC|')).toBe(true);
    // Short last line keeps the ascii column aligned.
    expect(lines[1].indexOf('|')).toBe(lines[0].indexOf('|'));
  });

  it('caps output at the limit', () => {
    const bytes = new Uint8Array(100_000);
    const d = hexdump(bytes, 64);
    expect(d.lines).toBe(4);
    expect(d.shown).toBe(64);
    expect(d.truncated).toBe(true);
  });

  it('handles empty input', () => {
    expect(hexdump(new Uint8Array()).text).toBe('');
  });

  it('formats a single line at an offset', () => {
    const bytes = Uint8Array.from({ length: 32 }, (_, i) => i + 0x30);
    expect(hexdumpLine(bytes, 16).slice(0, 8)).toBe('00000010');
  });
});
