import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react';
import { loadJSON, saveJSON } from '../lib/storage';

export interface SplitPaneProps {
  direction: 'horizontal' | 'vertical';
  /** localStorage key for the persisted size of the first pane (fraction 0..1) */
  storageKey: string;
  defaultFraction: number;
  minFirst?: number;
  minSecond?: number;
  first: ReactNode;
  second: ReactNode;
  label: string;
  className?: string;
}

export function clampFraction(f: number, total: number, minFirst: number, minSecond: number): number {
  if (!Number.isFinite(f)) return 0.5;
  if (total <= 0) return Math.min(0.95, Math.max(0.05, f));
  const lo = Math.min(minFirst / total, 0.5);
  const hi = Math.max(1 - minSecond / total, 0.5);
  return Math.min(hi, Math.max(lo, f));
}

/** SplitPane has a draggable (and keyboard-operable) separator; its size persists. */
export function SplitPane({ direction, storageKey, defaultFraction, minFirst = 160, minSecond = 160, first, second, label, className }: SplitPaneProps) {
  const [fraction, setFraction] = useState(() => {
    const v = loadJSON<number>(storageKey, defaultFraction);
    return typeof v === 'number' && v > 0 && v < 1 ? v : defaultFraction;
  });
  const ref = useRef<HTMLDivElement>(null);
  const horizontal = direction === 'horizontal';

  useEffect(() => saveJSON(storageKey, fraction), [storageKey, fraction]);

  const total = useCallback(() => {
    const r = ref.current?.getBoundingClientRect();
    return r ? (horizontal ? r.width : r.height) : 0;
  }, [horizontal]);

  const onPointerDown = (e: React.PointerEvent) => {
    e.preventDefault();
    const el = ref.current;
    if (!el) return;
    const rect = el.getBoundingClientRect();
    const size = horizontal ? rect.width : rect.height;
    document.body.classList.add(horizontal ? 'resizing-col' : 'resizing-row');
    const move = (ev: PointerEvent) => {
      const pos = horizontal ? ev.clientX - rect.left : ev.clientY - rect.top;
      setFraction(clampFraction(pos / size, size, minFirst, minSecond));
    };
    const up = () => {
      document.body.classList.remove('resizing-col', 'resizing-row');
      window.removeEventListener('pointermove', move);
      window.removeEventListener('pointerup', up);
    };
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', up);
  };

  const onKeyDown = (e: React.KeyboardEvent) => {
    const dec = horizontal ? 'ArrowLeft' : 'ArrowUp';
    const inc = horizontal ? 'ArrowRight' : 'ArrowDown';
    if (e.key !== dec && e.key !== inc) return;
    e.preventDefault();
    const step = e.shiftKey ? 0.1 : 0.02;
    setFraction((f) => clampFraction(f + (e.key === inc ? step : -step), total(), minFirst, minSecond));
  };

  const pct = `${(fraction * 100).toFixed(3)}%`;
  return (
    <div ref={ref} className={`split split-${direction} ${className ?? ''}`}>
      <div className="split-pane" style={{ flexBasis: pct }}>
        {first}
      </div>
      <div
        className="split-handle"
        role="separator"
        tabIndex={0}
        aria-label={label}
        aria-orientation={horizontal ? 'vertical' : 'horizontal'}
        aria-valuenow={Math.round(fraction * 100)}
        aria-valuemin={0}
        aria-valuemax={100}
        onPointerDown={onPointerDown}
        onKeyDown={onKeyDown}
        onDoubleClick={() => setFraction(defaultFraction)}
      />
      <div className="split-pane split-pane-second">{second}</div>
    </div>
  );
}
