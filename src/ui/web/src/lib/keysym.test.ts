import { describe, expect, it } from 'vitest';
import { keysymForChar, keysymFromEvent, XK } from './keysym';

describe('keysymFromEvent', () => {
  it('maps printable ASCII and Latin-1 to their code point', () => {
    expect(keysymFromEvent({ key: 'a' })).toBe(0x61);
    expect(keysymFromEvent({ key: 'A' })).toBe(0x41);
    expect(keysymFromEvent({ key: ' ' })).toBe(0x20);
    expect(keysymFromEvent({ key: '~' })).toBe(0x7e);
    expect(keysymFromEvent({ key: 'é' })).toBe(0xe9);
    expect(keysymFromEvent({ key: 'ÿ' })).toBe(0xff);
    expect(keysymFromEvent({ key: '£' })).toBe(0xa3);
  });

  it('maps other Unicode characters to 0x01000000 + code point', () => {
    expect(keysymFromEvent({ key: '€' })).toBe(0x01000000 + 0x20ac);
    expect(keysymFromEvent({ key: 'ж' })).toBe(0x01000000 + 0x0436);
    expect(keysymFromEvent({ key: '😀' })).toBe(0x01000000 + 0x1f600);
  });

  it('maps editing and navigation keys', () => {
    expect(keysymFromEvent({ key: 'Enter' })).toBe(0xff0d);
    expect(keysymFromEvent({ key: 'Backspace' })).toBe(0xff08);
    expect(keysymFromEvent({ key: 'Tab' })).toBe(0xff09);
    expect(keysymFromEvent({ key: 'Escape' })).toBe(0xff1b);
    expect(keysymFromEvent({ key: 'ArrowLeft' })).toBe(0xff51);
    expect(keysymFromEvent({ key: 'ArrowUp' })).toBe(0xff52);
    expect(keysymFromEvent({ key: 'ArrowRight' })).toBe(0xff53);
    expect(keysymFromEvent({ key: 'ArrowDown' })).toBe(0xff54);
    expect(keysymFromEvent({ key: 'Home' })).toBe(0xff50);
    expect(keysymFromEvent({ key: 'End' })).toBe(0xff57);
    expect(keysymFromEvent({ key: 'PageUp' })).toBe(0xff55);
    expect(keysymFromEvent({ key: 'PageDown' })).toBe(0xff56);
    expect(keysymFromEvent({ key: 'Delete' })).toBe(0xffff);
    expect(keysymFromEvent({ key: 'Insert' })).toBe(0xff63);
  });

  it('maps F1-F12', () => {
    expect(keysymFromEvent({ key: 'F1' })).toBe(0xffbe);
    expect(keysymFromEvent({ key: 'F5' })).toBe(0xffc2);
    expect(keysymFromEvent({ key: 'F12' })).toBe(0xffc9);
    expect(keysymFromEvent({ key: 'F13' })).toBeNull();
  });

  it('distinguishes left and right modifiers by location', () => {
    expect(keysymFromEvent({ key: 'Shift', location: 1 })).toBe(XK.Shift_L);
    expect(keysymFromEvent({ key: 'Shift', location: 2 })).toBe(XK.Shift_R);
    expect(keysymFromEvent({ key: 'Control', location: 1 })).toBe(0xffe3);
    expect(keysymFromEvent({ key: 'Control', location: 2 })).toBe(0xffe4);
    expect(keysymFromEvent({ key: 'Alt', location: 1 })).toBe(0xffe9);
    expect(keysymFromEvent({ key: 'Alt', location: 2 })).toBe(0xffea);
    expect(keysymFromEvent({ key: 'Meta', location: 1 })).toBe(0xffe7);
    expect(keysymFromEvent({ key: 'Meta', location: 2 })).toBe(0xffe8);
  });

  it('maps numpad Enter to KP_Enter', () => {
    expect(keysymFromEvent({ key: 'Enter', location: 3 })).toBe(XK.KP_Enter);
  });

  it('ignores dead, unidentified and unknown keys', () => {
    expect(keysymFromEvent({ key: 'Dead' })).toBeNull();
    expect(keysymFromEvent({ key: 'Unidentified' })).toBeNull();
    expect(keysymFromEvent({ key: 'Process' })).toBeNull();
    expect(keysymFromEvent({ key: 'MediaPlayPause' })).toBeNull();
    expect(keysymFromEvent({ key: '' })).toBeNull();
  });
});

describe('keysymForChar', () => {
  it('rejects control characters', () => {
    expect(keysymForChar('\u0007')).toBeNull();
    expect(keysymForChar('\u0085')).toBeNull();
  });
});
