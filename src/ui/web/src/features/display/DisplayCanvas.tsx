import { useEffect, useImperativeHandle, useLayoutEffect, useRef, useState, type ReactNode } from 'react';
import { api, wsUrl } from '../../api/client';
import { ReconnectingSocket } from '../../api/ws';
import { readClipboardText } from '../../lib/clipboard';
import { keysymFromEvent } from '../../lib/keysym';
import { actions, showError, toast } from '../../state/app';
import { useStore } from '../../state/store';
import { displayStore, zoomActions } from './displayState';
import {
  BUTTON,
  buttonMask,
  canvasRotation,
  clampZoom,
  frameLayout,
  keyMessage,
  parseDisplayMessage,
  pointerMessage,
  toFramebufferOriented,
  type Orientation,
} from './protocol';

export interface DisplayHandle {
  /** tap sends a key press + release. */
  tapKey(keysym: number): void;
  paste(): Promise<void>;
  focus(): void;
}

/** Bezel width of the device frame in CSS pixels (kept in sync with .phone-frame padding). */
export const BEZEL = 10;
/** Padding of the scrolling stage around the frame (kept in sync with .phone-stage padding). */
export const STAGE_PAD = 12;

export async function pasteFromClipboard(): Promise<void> {
  let text: string;
  try {
    text = await readClipboardText();
  } catch {
    showError({ title: 'The clipboard could not be read.', causes: ['Allow clipboard access for the inspector window, then try again.'] });
    return;
  }
  if (!text) {
    toast('The clipboard is empty');
    return;
  }
  try {
    await api.paste(text);
    toast('Pasted into Android');
  } catch (err) {
    showError(err);
  }
}

export interface DisplayCanvasProps {
  active: boolean;
  /** display orientation reported by the core (quarter turns clockwise) */
  orientation: Orientation;
  ref?: React.Ref<DisplayHandle>;
  /** content shown inside the phone screen while there is no picture (boot spinner, drop zone…) */
  overlay?: ReactNode;
}

/**
 * DisplayCanvas streams the Android screen inside a phone-shaped device frame and forwards
 * pointer/keyboard input. The frame keeps the framebuffer's aspect ratio, follows the zoom
 * (fit / fixed scale, Ctrl+wheel, corner grip) and counter-rotates the picture for the current
 * orientation; pointer coordinates are mapped back through the inverse transform.
 */
export function DisplayCanvas({ active, orientation, ref, overlay }: DisplayCanvasProps) {
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const screenRef = useRef<HTMLDivElement>(null);
  const frameRef = useRef<HTMLDivElement>(null);
  const stageRef = useRef<HTMLDivElement>(null);
  const sock = useRef<ReconnectingSocket | null>(null);
  const zoom = useStore(displayStore, (s) => s.zoom);
  const [fb, setFb] = useState({ w: 0, h: 0 });
  const [box, setBox] = useState({ w: 0, h: 0 });
  const [connected, setConnected] = useState(false);
  const fbRef = useRef(fb);
  fbRef.current = fb;
  const orientationRef = useRef(orientation);
  orientationRef.current = orientation;
  const mask = useRef(0);
  const down = useRef(new Set<number>());
  /** point (fraction of the frame + client position) to keep under the cursor after a zoom step */
  const anchor = useRef<{ fx: number; fy: number; cx: number; cy: number } | null>(null);

  // Stream: decode JPEG rects in parallel but draw them in arrival order.
  useEffect(() => {
    if (!active) {
      setFb({ w: 0, h: 0 });
      return;
    }
    let queue: Promise<void> = Promise.resolve();
    const s = new ReconnectingSocket({
      url: () => wsUrl('/api/display'),
      binaryType: 'arraybuffer',
      onState: (st) => setConnected(st === 'open'),
      onMessage: (data) => {
        if (typeof data === 'string') return;
        const msg = parseDisplayMessage(data);
        if (!msg) return;
        if (msg.type === 'size') {
          queue = queue.then(() => {
            const c = canvasRef.current;
            if (c) {
              c.width = msg.width;
              c.height = msg.height;
            }
            setFb({ w: msg.width, h: msg.height });
          });
          return;
        }
        const decoded = createImageBitmap(new Blob([msg.jpeg as BlobPart], { type: 'image/jpeg' })).catch(() => null);
        queue = queue.then(async () => {
          const bmp = await decoded;
          const ctx = canvasRef.current?.getContext('2d');
          if (bmp && ctx) ctx.drawImage(bmp, msg.x, msg.y);
          bmp?.close();
        });
      },
    });
    sock.current = s;
    return () => {
      s.close();
      sock.current = null;
      setConnected(false);
    };
  }, [active]);

  // Available space (the stage's content box).
  useLayoutEffect(() => {
    const el = stageRef.current;
    if (!el) return;
    const measure = () => setBox({ w: el.clientWidth - 2 * STAGE_PAD, h: el.clientHeight - 2 * STAGE_PAD });
    measure();
    if (typeof ResizeObserver === 'undefined') return;
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  const layout = frameLayout(fb.w, fb.h, orientation, box.w, box.h, BEZEL, zoom);

  // Publish the shown scale (the toolbar steps from it) and keep the zoom anchor under the cursor.
  useLayoutEffect(() => {
    displayStore.set({ effectiveScale: layout.scale });
    const a = anchor.current;
    const stage = stageRef.current;
    const frame = frameRef.current;
    if (a && stage && frame) {
      anchor.current = null;
      const sr = stage.getBoundingClientRect();
      stage.scrollLeft = frame.offsetLeft + a.fx * frame.offsetWidth - (a.cx - sr.left);
      stage.scrollTop = frame.offsetTop + a.fy * frame.offsetHeight - (a.cy - sr.top);
    }
  }, [layout.scale, layout.frame.width, layout.frame.height]);

  // Ctrl+wheel (and trackpad pinch) zooms the phone; plain wheel scrolls the stage or Android.
  useEffect(() => {
    const stage = stageRef.current;
    if (!stage) return;
    const onWheel = (e: WheelEvent) => {
      if (!e.ctrlKey && !e.metaKey) return;
      e.preventDefault();
      const frame = frameRef.current;
      if (frame) {
        const r = frame.getBoundingClientRect();
        anchor.current = { fx: (e.clientX - r.left) / r.width, fy: (e.clientY - r.top) / r.height, cx: e.clientX, cy: e.clientY };
      }
      zoomActions.step(e.deltaY < 0 ? 1 : -1);
    };
    stage.addEventListener('wheel', onWheel, { passive: false });
    return () => stage.removeEventListener('wheel', onWheel);
  }, []);

  const send = (msg: string) => sock.current?.send(msg);

  const pos = (e: { clientX: number; clientY: number }) => {
    const s = screenRef.current;
    if (!s) return { x: 0, y: 0 };
    return toFramebufferOriented(e.clientX, e.clientY, s.getBoundingClientRect(), fbRef.current.w, fbRef.current.h, orientationRef.current);
  };

  const tapKey = (k: number) => {
    send(keyMessage(k, true));
    send(keyMessage(k, false));
  };

  useImperativeHandle(ref, () => ({
    tapKey,
    paste: pasteFromClipboard,
    focus: () => canvasRef.current?.focus(),
  }));

  const onPointer = (e: React.PointerEvent<HTMLCanvasElement>) => {
    if (!fbRef.current.w) return;
    if (e.type === 'pointerdown') {
      canvasRef.current?.focus();
      canvasRef.current?.setPointerCapture?.(e.pointerId);
    }
    const m = buttonMask(e.buttons);
    const p = pos(e);
    mask.current = m;
    send(pointerMessage(p.x, p.y, m));
    e.preventDefault();
  };

  const onWheel = (e: React.WheelEvent<HTMLCanvasElement>) => {
    if (e.ctrlKey || e.metaKey) return; // zoom, handled on the stage
    if (!fbRef.current.w || e.deltaY === 0) return;
    const p = pos(e);
    const bit = e.deltaY < 0 ? BUTTON.wheelUp : BUTTON.wheelDown;
    send(pointerMessage(p.x, p.y, mask.current | bit));
    send(pointerMessage(p.x, p.y, mask.current));
  };

  // Native non-passive wheel listener so wheel over Android never scrolls the stage.
  useEffect(() => {
    const c = canvasRef.current;
    if (!c) return;
    const prevent = (e: WheelEvent) => e.preventDefault();
    c.addEventListener('wheel', prevent, { passive: false });
    return () => c.removeEventListener('wheel', prevent);
  }, []);

  const onKey = (e: React.KeyboardEvent<HTMLCanvasElement>) => {
    const isDown = e.type === 'keydown';
    if (isDown && (e.ctrlKey || e.metaKey) && !e.altKey) {
      const key = e.key.toLowerCase();
      if (key === 'v') {
        e.preventDefault();
        void pasteFromClipboard();
        return;
      }
      if (e.key === 'ArrowLeft' || e.key === 'ArrowRight') {
        e.preventDefault();
        void actions.rotate(orientationRef.current + (e.key === 'ArrowRight' ? 1 : -1));
        return;
      }
    }
    const k = keysymFromEvent(e);
    if (k === null) return;
    e.preventDefault();
    e.stopPropagation();
    if (isDown) down.current.add(k);
    else down.current.delete(k);
    send(keyMessage(k, isDown));
  };

  // Release held keys when focus leaves, so Android never sees a stuck key.
  const onBlur = () => {
    for (const k of down.current) send(keyMessage(k, false));
    down.current.clear();
    mask.current = 0;
  };

  // Corner grip: dragging resizes the phone (switches to a fixed scale).
  const onGripDown = (e: React.PointerEvent) => {
    e.preventDefault();
    e.stopPropagation();
    const startScale = layout.scale || 1;
    const startW = layout.screen.width || 1;
    const startH = layout.screen.height || 1;
    const sx = e.clientX;
    const sy = e.clientY;
    document.body.classList.add('resizing-phone');
    const move = (ev: PointerEvent) => {
      const ratio = Math.max((startW + ev.clientX - sx) / startW, (startH + ev.clientY - sy) / startH);
      zoomActions.set(clampZoom(startScale * ratio));
    };
    const up = () => {
      document.body.classList.remove('resizing-phone');
      window.removeEventListener('pointermove', move);
      window.removeEventListener('pointerup', up);
    };
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', up);
  };

  const live = active && fb.w > 0;
  const landscape = orientation % 2 === 1;
  const rotation = canvasRotation(orientation);
  return (
    <div className={`phone-stage${layout.overflow ? ' overflow' : ''}`} ref={stageRef}>
      <div
        ref={frameRef}
        className={`phone-frame${live ? ' live' : ''}${landscape ? ' landscape' : ''}`}
        style={layout.frame.width ? { width: layout.frame.width, height: layout.frame.height } : { visibility: 'hidden' }}
        data-testid="phone-frame"
        data-orientation={orientation}
        data-scale={layout.scale ? layout.scale.toFixed(3) : undefined}
      >
        <div className="phone-screen" ref={screenRef} style={{ width: layout.screen.width || undefined, height: layout.screen.height || undefined }}>
          <canvas
            ref={canvasRef}
            className="display-canvas"
            tabIndex={live ? 0 : -1}
            role="img"
            aria-label="Android screen. Focus it to type into Android; Ctrl+V pastes text, Ctrl+wheel zooms, Ctrl+arrows rotate."
            style={{
              width: layout.canvas.width || undefined,
              height: layout.canvas.height || undefined,
              transform: `translate(-50%, -50%) rotate(${rotation}deg)`,
              visibility: live ? 'visible' : 'hidden',
            }}
            onPointerDown={onPointer}
            onPointerMove={onPointer}
            onPointerUp={onPointer}
            onWheel={onWheel}
            onContextMenu={(e) => e.preventDefault()}
            onKeyDown={onKey}
            onKeyUp={onKey}
            onBlur={onBlur}
          />
          {!live && overlay ? <div className="screen-overlay">{overlay}</div> : null}
          {active && !connected && fb.w > 0 && (
            <div className="display-reconnecting" role="status">
              <span className="spinner" aria-hidden="true" /> Reconnecting to the display…
            </div>
          )}
        </div>
        <div className="phone-grip" role="presentation" title="Drag to resize" onPointerDown={onGripDown} />
      </div>
    </div>
  );
}
