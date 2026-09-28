import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import { Icon, type IconName } from './Icon';

export interface MenuItem {
  id: string;
  label: string;
  /** secondary line under the label */
  hint?: string;
  icon?: IconName;
  disabled?: boolean;
  title?: string;
  separatorBefore?: boolean;
  /** when defined the item is a checkbox (menuitemcheckbox) */
  checked?: boolean;
  danger?: boolean;
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
    el.querySelector<HTMLElement>('[role^="menuitem"]:not([aria-disabled="true"])')?.focus();
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
    const els = [...(ref.current?.querySelectorAll<HTMLElement>('[role^="menuitem"]:not([aria-disabled="true"])') ?? [])];
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
      {items.map((it) => {
        const checkable = it.checked !== undefined;
        const cls = ['menu-item'];
        if (it.disabled) cls.push('disabled');
        if (it.danger) cls.push('danger');
        if (checkable) cls.push('checkable');
        return (
          <div key={it.id}>
            {it.separatorBefore && <div className="menu-sep" role="separator" />}
            <div
              role={checkable ? 'menuitemcheckbox' : 'menuitem'}
              tabIndex={-1}
              aria-disabled={it.disabled || undefined}
              aria-checked={checkable ? it.checked : undefined}
              title={it.title}
              className={cls.join(' ')}
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
              <span className="menu-lead" aria-hidden="true">
                {checkable ? it.checked ? <Icon name="check" size={12} /> : null : it.icon ? <Icon name={it.icon} size={13} /> : null}
              </span>
              <span className="menu-text">
                <span className="menu-label">{it.label}</span>
                {it.hint && <span className="menu-hint">{it.hint}</span>}
              </span>
            </div>
          </div>
        );
      })}
    </div>
  );
}
