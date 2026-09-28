import { describe, expect, it } from 'vitest';
import { buttonMask, fitSize, keyMessage, parseDisplayMessage, pointerMessage, toFramebuffer } from './protocol';

describe('display protocol', () => {
  it('parses size and rect messages (big endian)', () => {
    expect(parseDisplayMessage(new Uint8Array([1, 0x04, 0x38, 0x07, 0x80]).buffer)).toEqual({ type: 'size', width: 1080, height: 1920 });
    const rect = new Uint8Array([2, 0, 10, 0, 20, 0x01, 0x00, 0, 50, 0xff, 0xd8, 0xff]);
    const m = parseDisplayMessage(rect.buffer);
    expect(m).toMatchObject({ type: 'rect', x: 10, y: 20, width: 256, height: 50 });
    expect(m && m.type === 'rect' && Array.from(m.jpeg)).toEqual([0xff, 0xd8, 0xff]);
    expect(parseDisplayMessage(new Uint8Array([9]).buffer)).toBeNull();
    expect(parseDisplayMessage(new Uint8Array([1, 0]).buffer)).toBeNull();
  });

  it('converts DOM buttons to the RFB mask', () => {
    expect(buttonMask(0)).toBe(0);
    expect(buttonMask(1)).toBe(1); // left
    expect(buttonMask(2)).toBe(4); // right
    expect(buttonMask(4)).toBe(2); // middle
    expect(buttonMask(3)).toBe(5);
  });

  it('encodes input messages', () => {
    expect(JSON.parse(pointerMessage(5, 6, 1))).toEqual({ t: 'p', x: 5, y: 6, b: 1 });
    expect(JSON.parse(keyMessage(0xff0d, true))).toEqual({ t: 'k', k: 0xff0d, d: true });
  });

  it('maps scaled canvas coordinates to the framebuffer', () => {
    const rect = { left: 100, top: 50, width: 540, height: 960 };
    expect(toFramebuffer(100, 50, rect, 1080, 1920)).toEqual({ x: 0, y: 0 });
    expect(toFramebuffer(370, 530, rect, 1080, 1920)).toEqual({ x: 540, y: 960 });
    expect(toFramebuffer(5000, -20, rect, 1080, 1920)).toEqual({ x: 1079, y: 0 });
  });

  it('fits the framebuffer keeping the aspect ratio', () => {
    expect(fitSize(1080, 1920, 400, 400)).toEqual({ width: 225, height: 400 });
    expect(fitSize(1920, 1080, 960, 1000)).toEqual({ width: 960, height: 540 });
    expect(fitSize(0, 0, 100, 100)).toEqual({ width: 0, height: 0 });
  });
});
