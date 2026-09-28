import { useEffect, useLayoutEffect, useRef, useState } from 'react';

export interface MenuItem {
  id: string;
  label: string;
  disabled?: boolean;
  title?: string;
  separatorBefore?: boolean;
  onSelect: () => void;
}

/** ContextMenu is a keyboard-accessible floating menu kept inside the viewport. */
export function ContextMenu({ x, y, items, onClose, label }: { x: number; y: number; items: MenuItem[]; onClose: () => void; label: string }) {
  const ref = useRef<HTMLDivElement>(null);
  const [pos, setPos] = useState({ left: x, top: y });

  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const r = el.getBoundingClientRect();
    setPos({ left: Math.max(4, Math.min(x, window.innerWidth - r.width - 4)), top: Math.max(4, Math.min(y, window.innerHeight - r.height - 4)) });
    el.querySelector<HTMLElement>('[role="menuitem"]:not([aria-disabled="true"])')?.focus();
  }, [x, y]);

  useEffect(() => {
    const onDown = (e: MouseEvent) => {
      if (!ref.current?.contains(e.target as Node)) onClose();
    };
    const onBlur = () => onClose();
    window.addEventListener('mousedown', onDown, true);
    window.addEventListener('blur', onBlur);
    window.addEventListener('resize', onBlur);
    return () => {
      window.removeEventListener('mousedown', onDown, true);
      window.removeEventListener('blur', onBlur);
      window.removeEventListener('resize', onBlur);
    };
  }, [onClose]);

  const onKeyDown = (e: React.KeyboardEvent) => {
    const els = [...(ref.current?.querySelectorAll<HTMLElement>('[role="menuitem"]:not([aria-disabled="true"])') ?? [])];
    const i = els.indexOf(document.activeElement as HTMLElement);
    if (e.key === 'Escape' || e.key === 'Tab') {
      e.preventDefault();
      onClose();
    } else if (e.key === 'ArrowDown') {
      e.preventDefault();
      els[(i + 1) % els.length]?.focus();
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      els[(i - 1 + els.length) % els.length]?.focus();
    }
  };

  return (
    <div ref={ref} className="context-menu" role="menu" aria-label={label} style={pos} onKeyDown={onKeyDown} onContextMenu={(e) => e.preventDefault()}>
      {items.map((it) => (
        <div key={it.id}>
          {it.separatorBefore && <div className="menu-sep" role="separator" />}
          <div
            role="menuitem"
            tabIndex={-1}
            aria-disabled={it.disabled || undefined}
            title={it.title}
            className={`menu-item${it.disabled ? ' disabled' : ''}`}
            onClick={() => {
              if (it.disabled) return;
              onClose();
              it.onSelect();
            }}
            onKeyDown={(e) => {
              if ((e.key === 'Enter' || e.key === ' ') && !it.disabled) {
                e.preventDefault();
                onClose();
                it.onSelect();
              }
            }}
          >
            {it.label}
          </div>
        </div>
      ))}
    </div>
  );
}
