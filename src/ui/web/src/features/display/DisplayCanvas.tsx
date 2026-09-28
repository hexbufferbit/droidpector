import { useEffect, useImperativeHandle, useLayoutEffect, useRef, useState } from 'react';
import { api, wsUrl } from '../../api/client';
import { ReconnectingSocket } from '../../api/ws';
import { readClipboardText } from '../../lib/clipboard';
import { keysymFromEvent } from '../../lib/keysym';
import { showError, toast } from '../../state/app';
import { BUTTON, buttonMask, fitSize, keyMessage, parseDisplayMessage, pointerMessage, toFramebuffer } from './protocol';

export interface DisplayHandle {
  /** tap sends a key press + release. */
  tapKey(keysym: number): void;
  paste(): Promise<void>;
  focus(): void;
}

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

/** DisplayCanvas streams the Android screen and forwards pointer/keyboard input. */
export function DisplayCanvas({ active, ref }: { active: boolean; ref?: React.Ref<DisplayHandle> }) {
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const boxRef = useRef<HTMLDivElement>(null);
  const sock = useRef<ReconnectingSocket | null>(null);
  const [fb, setFb] = useState({ w: 0, h: 0 });
  const [box, setBox] = useState({ w: 0, h: 0 });
  const [connected, setConnected] = useState(false);
  const fbRef = useRef(fb);
  fbRef.current = fb;
  const mask = useRef(0);
  const down = useRef(new Set<number>());

  // Stream: decode JPEG rects in parallel but draw them in arrival order.
  useEffect(() => {
    if (!active) return;
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

  useLayoutEffect(() => {
    const el = boxRef.current;
    if (!el) return;
    const measure = () => setBox({ w: el.clientWidth, h: el.clientHeight });
    measure();
    if (typeof ResizeObserver === 'undefined') return;
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  const send = (msg: string) => sock.current?.send(msg);

  const pos = (e: { clientX: number; clientY: number }) => {
    const c = canvasRef.current;
    if (!c) return { x: 0, y: 0 };
    return toFramebuffer(e.clientX, e.clientY, c.getBoundingClientRect(), fbRef.current.w, fbRef.current.h);
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
    if (!fbRef.current.w || e.deltaY === 0) return;
    const p = pos(e);
    const bit = e.deltaY < 0 ? BUTTON.wheelUp : BUTTON.wheelDown;
    send(pointerMessage(p.x, p.y, mask.current | bit));
    send(pointerMessage(p.x, p.y, mask.current));
  };

  // Native non-passive wheel listener so the page does not scroll.
  useEffect(() => {
    const c = canvasRef.current;
    if (!c) return;
    const prevent = (e: WheelEvent) => e.preventDefault();
    c.addEventListener('wheel', prevent, { passive: false });
    return () => c.removeEventListener('wheel', prevent);
  }, []);

  const onKey = (e: React.KeyboardEvent<HTMLCanvasElement>) => {
    const isDown = e.type === 'keydown';
    if (isDown && (e.ctrlKey || e.metaKey) && !e.altKey && e.key.toLowerCase() === 'v') {
      e.preventDefault();
      void pasteFromClipboard();
      return;
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

  const size = fitSize(fb.w, fb.h, box.w, box.h);
  return (
    <div className="display-box" ref={boxRef}>
      <canvas
        ref={canvasRef}
        className="display-canvas"
        tabIndex={0}
        role="img"
        aria-label="Android screen. Focus it to type into Android; Ctrl+V pastes text."
        style={{ width: size.width || undefined, height: size.height || undefined, visibility: fb.w ? 'visible' : 'hidden' }}
        onPointerDown={onPointer}
        onPointerMove={onPointer}
        onPointerUp={onPointer}
        onWheel={onWheel}
        onContextMenu={(e) => e.preventDefault()}
        onKeyDown={onKey}
        onKeyUp={onKey}
        onBlur={onBlur}
      />
      {active && !connected && fb.w > 0 && <div className="display-reconnecting">Reconnecting to the display…</div>}
    </div>
  );
}
