import { describe, expect, it } from 'vitest';
import {
  buttonMask,
  canvasRotation,
  clampZoom,
  fitFrame,
  fitSize,
  frameLayout,
  keyMessage,
  normalizeOrientation,
  parseDisplayMessage,
  PLACEHOLDER_FB,
  pointerMessage,
  sanitizeZoom,
  toFramebuffer,
  toFramebufferOriented,
  visibleSize,
  zoomStep,
  type Orientation,
} from './protocol';

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

describe('fitFrame', () => {
  it('sizes the device frame around a portrait screen', () => {
    const f = fitFrame(720, 1280, 400, 800, 10);
    expect(f.screen).toEqual({ width: 380, height: 676 });
    expect(f.frame).toEqual({ width: 400, height: 696 });
    expect(f.screen.width / f.screen.height).toBeCloseTo(720 / 1280, 2);
  });
  it('adapts to landscape framebuffers', () => {
    const f = fitFrame(1280, 720, 400, 800, 10);
    expect(f.screen).toEqual({ width: 380, height: 214 });
    expect(f.frame.width).toBe(400);
  });
  it('uses the portrait placeholder until the size is known', () => {
    const f = fitFrame(0, 0, 300, 1000, 8);
    expect(f.screen.width / f.screen.height).toBeCloseTo(PLACEHOLDER_FB.w / PLACEHOLDER_FB.h, 2);
    expect(fitFrame(0, 0, 0, 0, 8).frame).toEqual({ width: 0, height: 0 });
  });
  it('keeps pointer mapping exact on the fitted screen', () => {
    const f = fitFrame(720, 1280, 400, 800, 10);
    const rect = { left: 10, top: 10, width: f.screen.width, height: f.screen.height };
    expect(toFramebuffer(10 + f.screen.width - 1, 10 + f.screen.height - 1, rect, 720, 1280)).toEqual({ x: 718, y: 1278 });
    expect(toFramebuffer(10, 10, rect, 720, 1280)).toEqual({ x: 0, y: 0 });
  });
});

describe('orientation', () => {
  it('normalizes quarter turns', () => {
    expect(normalizeOrientation(undefined)).toBe(0);
    expect(normalizeOrientation(5)).toBe(1);
    expect(normalizeOrientation(-1)).toBe(3);
    expect(canvasRotation(1)).toBe(-90);
    expect(canvasRotation(3)).toBe(-270);
  });

  it('swaps the visible size for landscape orientations', () => {
    expect(visibleSize(720, 1280, 0)).toEqual({ w: 720, h: 1280 });
    expect(visibleSize(720, 1280, 1)).toEqual({ w: 1280, h: 720 });
    expect(visibleSize(720, 1280, 2)).toEqual({ w: 720, h: 1280 });
    expect(visibleSize(720, 1280, 3)).toEqual({ w: 1280, h: 720 });
  });

  it('maps visible points back to the framebuffer for every orientation', () => {
    const fb = { w: 720, h: 1280 };
    // Visible screen at half scale, offset in the page; P is at (¼, ¼) and Q at (¾, ¼) of the visible screen.
    const at = (o: Orientation) => {
      const vis = visibleSize(fb.w, fb.h, o);
      const rect = { left: 100, top: 200, width: vis.w / 2, height: vis.h / 2 };
      const pt = (u: number, v: number) => toFramebufferOriented(rect.left + u * rect.width, rect.top + v * rect.height, rect, fb.w, fb.h, o);
      return { tl: pt(0, 0), P: pt(0.25, 0.25), Q: pt(0.75, 0.25) };
    };
    // portrait: identity
    expect(at(0)).toEqual({ tl: { x: 0, y: 0 }, P: { x: 180, y: 320 }, Q: { x: 540, y: 320 } });
    // 1: shown rotated 90° counter-clockwise → the visible top-left is the framebuffer's top-right
    expect(at(1)).toEqual({ tl: { x: 719, y: 0 }, P: { x: 540, y: 320 }, Q: { x: 540, y: 960 } });
    // 2: upside down
    expect(at(2)).toEqual({ tl: { x: 719, y: 1279 }, P: { x: 540, y: 960 }, Q: { x: 180, y: 960 } });
    // 3: shown rotated 90° clockwise → the visible top-left is the framebuffer's bottom-left
    expect(at(3)).toEqual({ tl: { x: 0, y: 1279 }, P: { x: 180, y: 960 }, Q: { x: 180, y: 320 } });
  });

  it('maps interior points through scale and rotation', () => {
    const fb = { w: 720, h: 1280 };
    // orientation 1 at 25%: visible 320×180 at (0,0). Visible (80, 45) → u=0.25, v=0.25 → fb (0.75·720, 0.25·1280)
    expect(toFramebufferOriented(80, 45, { left: 0, top: 0, width: 320, height: 180 }, fb.w, fb.h, 1)).toEqual({ x: 540, y: 320 });
    // orientation 3 at 25%: visible (80, 45) → fb (0.25·720, 0.75·1280)
    expect(toFramebufferOriented(80, 45, { left: 0, top: 0, width: 320, height: 180 }, fb.w, fb.h, 3)).toEqual({ x: 180, y: 960 });
    // orientation 2 at 200%: visible 1440×2560; visible (360, 1280) → u=0.25, v=0.5 → fb (0.75·720, 0.5·1280)
    expect(toFramebufferOriented(360, 1280, { left: 0, top: 0, width: 1440, height: 2560 }, fb.w, fb.h, 2)).toEqual({ x: 540, y: 640 });
    // portrait at 200% with an offset rect
    expect(toFramebufferOriented(10 + 720, 20 + 1280, { left: 10, top: 20, width: 1440, height: 2560 }, fb.w, fb.h, 0)).toEqual({ x: 360, y: 640 });
    // clamped and degenerate
    expect(toFramebufferOriented(-50, -50, { left: 0, top: 0, width: 320, height: 180 }, fb.w, fb.h, 1)).toEqual({ x: 719, y: 0 });
    expect(toFramebufferOriented(5, 5, { left: 0, top: 0, width: 0, height: 0 }, fb.w, fb.h, 1)).toEqual({ x: 0, y: 0 });
  });
});

describe('zoom & frame layout', () => {
  it('clamps and steps the zoom level', () => {
    expect(clampZoom(0.1)).toBe(0.25);
    expect(clampZoom(10)).toBe(3);
    expect(clampZoom(NaN)).toBe(1);
    expect(zoomStep(1, 1)).toBe(1.25);
    expect(zoomStep(1, -1)).toBe(0.8);
    expect(zoomStep(3, 1)).toBe(3);
    expect(sanitizeZoom({ mode: 'scale', scale: 2 })).toEqual({ mode: 'scale', scale: 2 });
    expect(sanitizeZoom({ mode: 'scale', scale: 99 })).toEqual({ mode: 'scale', scale: 3 });
    expect(sanitizeZoom('junk')).toEqual({ mode: 'fit' });
  });

  it('fits the rotated screen into the box', () => {
    const portrait = frameLayout(720, 1280, 0, 400, 800, 10, { mode: 'fit' });
    expect(portrait.screen).toEqual({ width: 380, height: 676 });
    expect(portrait.canvas).toEqual({ width: 380, height: 676 });
    expect(portrait.overflow).toBe(false);
    const landscape = frameLayout(720, 1280, 1, 400, 800, 10, { mode: 'fit' });
    expect(landscape.screen).toEqual({ width: 380, height: 214 });
    // the unrotated canvas is portrait: its box is the screen box swapped
    expect(landscape.canvas).toEqual({ width: 214, height: 380 });
    expect(landscape.frame).toEqual({ width: 400, height: 234 });
  });

  it('applies a fixed scale (1:1 = one framebuffer pixel per CSS pixel) and reports overflow', () => {
    const one = frameLayout(720, 1280, 0, 400, 800, 10, { mode: 'scale', scale: 1 });
    expect(one.screen).toEqual({ width: 720, height: 1280 });
    expect(one.frame).toEqual({ width: 740, height: 1300 });
    expect(one.overflow).toBe(true);
    const half = frameLayout(720, 1280, 3, 2000, 2000, 10, { mode: 'scale', scale: 0.5 });
    expect(half.screen).toEqual({ width: 640, height: 360 });
    expect(half.overflow).toBe(false);
  });

  it('returns an empty layout when nothing fits', () => {
    expect(frameLayout(720, 1280, 0, 0, 0, 10, { mode: 'fit' }).frame).toEqual({ width: 0, height: 0 });
  });
});
