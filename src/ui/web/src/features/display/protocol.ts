// Display stream wire format (see src/display/stream.go):
//   size: [1][w u16 BE][h u16 BE]
//   rect: [2][x u16][y u16][w u16][h u16][JPEG bytes]
// Input (JSON text): pointer {"t":"p","x","y","b":mask}, key {"t":"k","k":keysym,"d":bool}

export type DisplayMessage =
  | { type: 'size'; width: number; height: number }
  | { type: 'rect'; x: number; y: number; width: number; height: number; jpeg: Uint8Array };

export function parseDisplayMessage(buf: ArrayBuffer): DisplayMessage | null {
  const v = new DataView(buf);
  if (buf.byteLength < 1) return null;
  const t = v.getUint8(0);
  if (t === 1 && buf.byteLength >= 5) return { type: 'size', width: v.getUint16(1), height: v.getUint16(3) };
  if (t === 2 && buf.byteLength >= 9) {
    return { type: 'rect', x: v.getUint16(1), y: v.getUint16(3), width: v.getUint16(5), height: v.getUint16(7), jpeg: new Uint8Array(buf, 9) };
  }
  return null;
}

/** X11/RFB pointer button mask bits. */
export const BUTTON = { left: 1, middle: 2, right: 4, wheelUp: 8, wheelDown: 16 } as const;

/** buttonMask converts MouseEvent.buttons (1 left, 2 right, 4 middle) to the RFB mask. */
export function buttonMask(domButtons: number): number {
  return (domButtons & 1 ? BUTTON.left : 0) | (domButtons & 4 ? BUTTON.middle : 0) | (domButtons & 2 ? BUTTON.right : 0);
}

export function pointerMessage(x: number, y: number, mask: number): string {
  return JSON.stringify({ t: 'p', x, y, b: mask });
}

export function keyMessage(keysym: number, down: boolean): string {
  return JSON.stringify({ t: 'k', k: keysym, d: down });
}

export interface Rect {
  left: number;
  top: number;
  width: number;
  height: number;
}

/** toFramebuffer maps client coordinates on the scaled canvas to framebuffer pixels (clamped). */
export function toFramebuffer(clientX: number, clientY: number, rect: Rect, fbWidth: number, fbHeight: number): { x: number; y: number } {
  if (rect.width <= 0 || rect.height <= 0) return { x: 0, y: 0 };
  const x = Math.floor(((clientX - rect.left) / rect.width) * fbWidth);
  const y = Math.floor(((clientY - rect.top) / rect.height) * fbHeight);
  return { x: Math.max(0, Math.min(fbWidth - 1, x)), y: Math.max(0, Math.min(fbHeight - 1, y)) };
}

/** fitSize scales (w,h) to fit inside (maxW,maxH) keeping the aspect ratio. */
export function fitSize(w: number, h: number, maxW: number, maxH: number): { width: number; height: number } {
  if (w <= 0 || h <= 0 || maxW <= 0 || maxH <= 0) return { width: 0, height: 0 };
  const s = Math.min(maxW / w, maxH / h);
  return { width: Math.floor(w * s), height: Math.floor(h * s) };
}

// ---- orientation --------------------------------------------------------------------

/** Orientation in quarter turns clockwise (0 = portrait), as reported by the core. */
export type Orientation = 0 | 1 | 2 | 3;

export function normalizeOrientation(o: number | undefined): Orientation {
  if (!Number.isFinite(o)) return 0;
  return ((((o as number) % 4) + 4) % 4) as Orientation;
}

/**
 * Android renders the rotated UI into the same (portrait) framebuffer, so the picture must be
 * counter-rotated on screen: the visible screen has swapped dimensions for odd orientations.
 */
export function visibleSize(fbW: number, fbH: number, orientation: Orientation): { w: number; h: number } {
  return orientation % 2 ? { w: fbH, h: fbW } : { w: fbW, h: fbH };
}

/** canvasRotation is the CSS rotation (degrees, clockwise) that shows the framebuffer upright. */
export function canvasRotation(orientation: Orientation): number {
  return -90 * orientation;
}

/**
 * toFramebufferOriented maps a client point on the *visible* (counter-rotated, scaled) screen to
 * natural-panel framebuffer pixels: the inverse of the screen rotation, then the inverse of the
 * scale. Touches are injected in the guest as a real touchscreen aligned with the panel, so the
 * pixel under the cursor is exactly the point to send, whatever the orientation.
 */
export function toFramebufferOriented(clientX: number, clientY: number, screenRect: Rect, fbW: number, fbH: number, orientation: Orientation): { x: number; y: number } {
  if (screenRect.width <= 0 || screenRect.height <= 0 || fbW <= 0 || fbH <= 0) return { x: 0, y: 0 };
  const u = (clientX - screenRect.left) / screenRect.width;
  const v = (clientY - screenRect.top) / screenRect.height;
  let fx: number;
  let fy: number;
  switch (orientation) {
    case 1: // canvas rotated 90° counter-clockwise on screen
      fx = 1 - v;
      fy = u;
      break;
    case 2:
      fx = 1 - u;
      fy = 1 - v;
      break;
    case 3: // canvas rotated 90° clockwise on screen
      fx = v;
      fy = 1 - u;
      break;
    default:
      fx = u;
      fy = v;
  }
  const x = Math.floor(fx * fbW);
  const y = Math.floor(fy * fbH);
  return { x: Math.max(0, Math.min(fbW - 1, x)), y: Math.max(0, Math.min(fbH - 1, y)) };
}

// ---- zoom & frame layout ---------------------------------------------------------------

/** Portrait placeholder aspect used for the device frame before the first size message arrives. */
export const PLACEHOLDER_FB = { w: 720, h: 1280 } as const;

export const ZOOM_MIN = 0.25;
export const ZOOM_MAX = 3;

export type ZoomMode = { mode: 'fit' } | { mode: 'scale'; scale: number };

export function clampZoom(s: number): number {
  if (!Number.isFinite(s) || s <= 0) return 1;
  return Math.min(ZOOM_MAX, Math.max(ZOOM_MIN, s));
}

/** zoomStep returns the next zoom level in the given direction (geometric steps of 1.25). */
export function zoomStep(scale: number, dir: 1 | -1): number {
  const next = dir > 0 ? scale * 1.25 : scale / 1.25;
  return clampZoom(Math.round(next * 100) / 100);
}

/** sanitizeZoom accepts only a well-formed persisted zoom mode. */
export function sanitizeZoom(v: unknown): ZoomMode {
  if (v && typeof v === 'object') {
    const o = v as Record<string, unknown>;
    if (o.mode === 'fit') return { mode: 'fit' };
    if (o.mode === 'scale' && typeof o.scale === 'number') return { mode: 'scale', scale: clampZoom(o.scale) };
  }
  return { mode: 'fit' };
}

export interface FrameLayout {
  /** CSS pixels per framebuffer pixel */
  scale: number;
  /** visible (rotated) screen in CSS px; the pointer rect */
  screen: { width: number; height: number };
  /** outer device frame */
  frame: { width: number; height: number };
  /** unrotated canvas box in CSS px (fbW × scale, fbH × scale) */
  canvas: { width: number; height: number };
  /** true when the frame is larger than the box (the stage scrolls) */
  overflow: boolean;
}

/**
 * frameLayout computes the device frame for a framebuffer at the given orientation and zoom.
 * Fit mode scales the (rotated) screen into the box minus the bezel; a fixed scale ignores the box.
 * Unknown framebuffers use the portrait placeholder so the empty frame already has a phone shape.
 */
export function frameLayout(fbW: number, fbH: number, orientation: Orientation, boxW: number, boxH: number, bezel: number, zoom: ZoomMode): FrameLayout {
  const known = fbW > 0 && fbH > 0;
  const w = known ? fbW : PLACEHOLDER_FB.w;
  const h = known ? fbH : PLACEHOLDER_FB.h;
  const vis = visibleSize(w, h, orientation);
  let scale: number;
  if (zoom.mode === 'scale') {
    scale = clampZoom(zoom.scale);
  } else {
    const fit = fitSize(vis.w, vis.h, boxW - 2 * bezel, boxH - 2 * bezel);
    scale = fit.width > 0 ? fit.width / vis.w : 0;
  }
  if (scale <= 0) return { scale: 0, screen: { width: 0, height: 0 }, frame: { width: 0, height: 0 }, canvas: { width: 0, height: 0 }, overflow: false };
  const screen = { width: Math.round(vis.w * scale), height: Math.round(vis.h * scale) };
  const canvas = orientation % 2 ? { width: screen.height, height: screen.width } : { width: screen.width, height: screen.height };
  const frame = { width: screen.width + 2 * bezel, height: screen.height + 2 * bezel };
  return { scale, screen, frame, canvas, overflow: frame.width > boxW || frame.height > boxH };
}

/** fitFrame is frameLayout in fit mode at portrait orientation (kept for the simple case). */
export function fitFrame(fbW: number, fbH: number, boxW: number, boxH: number, bezel: number): { frame: FrameLayout['frame']; screen: FrameLayout['screen'] } {
  const l = frameLayout(fbW, fbH, 0, boxW, boxH, bezel, { mode: 'fit' });
  return { frame: l.frame, screen: l.screen };
}
