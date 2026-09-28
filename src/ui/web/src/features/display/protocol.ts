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
