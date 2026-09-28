import { useRef, type ReactNode } from 'react';

export interface TabDef {
  id: string;
  label: ReactNode;
}

/** Tabs renders an ARIA tablist; arrow keys move between tabs. */
export function Tabs({ tabs, active, onChange, idPrefix, children }: { tabs: TabDef[]; active: string; onChange: (id: string) => void; idPrefix: string; children?: ReactNode }) {
  const listRef = useRef<HTMLDivElement>(null);
  const onKeyDown = (e: React.KeyboardEvent) => {
    const i = tabs.findIndex((t) => t.id === active);
    let next = -1;
    if (e.key === 'ArrowRight') next = (i + 1) % tabs.length;
    else if (e.key === 'ArrowLeft') next = (i - 1 + tabs.length) % tabs.length;
    else if (e.key === 'Home') next = 0;
    else if (e.key === 'End') next = tabs.length - 1;
    if (next >= 0) {
      e.preventDefault();
      onChange(tabs[next].id);
      listRef.current?.querySelectorAll<HTMLElement>('[role="tab"]')[next]?.focus();
    }
  };
  return (
    <div className="tabs-bar">
      <div className="tabs" role="tablist" ref={listRef} onKeyDown={onKeyDown}>
        {tabs.map((t) => (
          <button
            key={t.id}
            role="tab"
            id={`${idPrefix}-tab-${t.id}`}
            aria-selected={t.id === active}
            aria-controls={`${idPrefix}-panel`}
            tabIndex={t.id === active ? 0 : -1}
            className={`tab${t.id === active ? ' active' : ''}`}
            onClick={() => onChange(t.id)}
          >
            {t.label}
          </button>
        ))}
      </div>
      {children}
    </div>
  );
}
