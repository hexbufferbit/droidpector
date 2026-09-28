import { useEffect, useRef, useState } from 'react';
import { links } from '../../api/client';
import type { Session } from '../../api/types';
import { ConfirmDialog } from '../../components/ConfirmDialog';
import { Icon } from '../../components/Icon';
import { formatBytes, formatElapsed, pluralize } from '../../lib/format';
import { actions, appStore, liveSessionId, RUNNING_STATES, viewedSessionId } from '../../state/app';
import { useStore } from '../../state/store';

export function sessionDuration(s: Session, now = Date.now()): number {
  const start = new Date(s.startedAt).getTime();
  const end = s.endedAt ? new Date(s.endedAt).getTime() : now;
  return Math.max(0, end - start);
}

/** sessionLabel: "Session #42 · example.apk · 12m · 382 requests · 17 domains". */
export function sessionLabel(s: Session, now = Date.now()): string {
  const parts = [`Session #${s.number}`];
  if (s.name) parts.push(s.name);
  if (s.apk) parts.push(s.apk);
  parts.push(formatElapsed(sessionDuration(s, now)));
  parts.push(pluralize('request', s.requests));
  parts.push(pluralize('domain', s.domains));
  return parts.join(' · ');
}

export function SessionSelector() {
  const sessions = useStore(appStore, (s) => s.sessions);
  const viewed = useStore(appStore, viewedSessionId);
  const live = useStore(appStore, liveSessionId);
  const captureActive = useStore(appStore, (s) => s.status?.captureActive ?? false);
  const running = useStore(appStore, (s) => RUNNING_STATES.has(s.status?.state ?? 'stopped'));
  const [open, setOpen] = useState(false);
  const [confirm, setConfirm] = useState<'delete' | 'clear' | null>(null);
  const ref = useRef<HTMLDivElement>(null);
  const current = sessions.find((s) => s.id === viewed);

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => !ref.current?.contains(e.target as Node) && setOpen(false);
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && setOpen(false);
    window.addEventListener('mousedown', onDown);
    window.addEventListener('keydown', onKey);
    return () => {
      window.removeEventListener('mousedown', onDown);
      window.removeEventListener('keydown', onKey);
    };
  }, [open]);

  const isLive = !!current && current.id === live && !current.endedAt;

  return (
    <div className="session-selector" ref={ref}>
      <button className="session-button" aria-haspopup="listbox" aria-expanded={open} onClick={() => setOpen(!open)} title="Choose a capture session">
        {isLive ? <span className="live-dot" aria-label="Live" /> : <Icon name="clock" size={12} className="muted" />}
        <span className="session-text">{current ? sessionLabel(current) : 'No capture session'}</span>
        <Icon name="chevronDown" size={10} className="muted" />
      </button>
      {open && (
        <div className="popover session-popover">
          <div className="session-list" role="listbox" aria-label="Capture sessions">
            {sessions.length === 0 && <p className="muted pad">No sessions yet. Start the sandbox to capture traffic.</p>}
            {sessions.map((s) => (
              <div
                key={s.id}
                role="option"
                tabIndex={0}
                aria-selected={s.id === viewed}
                className={`session-item${s.id === viewed ? ' selected' : ''}`}
                onClick={() => {
                  actions.selectSession(s.id);
                  setOpen(false);
                }}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' || e.key === ' ') {
                    e.preventDefault();
                    actions.selectSession(s.id);
                    setOpen(false);
                  }
                }}
              >
                <div className="session-line">
                  {s.id === live && !s.endedAt && <span className="live-dot" aria-label="Live" />}
                  {s.saved && <Icon name="save" size={11} aria-label="Saved" className="muted" />}
                  <span className="session-line-text">{sessionLabel(s)}</span>
                </div>
                <div className="muted small num">
                  {new Date(s.startedAt).toLocaleString()} · {formatBytes(s.bytes)}
                  {s.package ? ` · ${s.package}` : ''}
                </div>
              </div>
            ))}
          </div>
          <div className="session-actions">
            <button className="btn small" disabled={!running} title={running ? 'Start a new capture session' : 'Start the sandbox first'} onClick={() => { setOpen(false); void actions.newSession(); }}>
              <Icon name="plus" /> New session
            </button>
            <button className="btn small ghost" disabled={!captureActive} onClick={() => { setOpen(false); void actions.stopCapture(); }}>
              <Icon name="stop" /> Stop capture
            </button>
            <span className="spacer" />
            <button className="btn small ghost" disabled={!current} onClick={() => { setOpen(false); setConfirm('clear'); }}>
              Clear
            </button>
            <button className="btn small ghost" disabled={!current} onClick={() => { setOpen(false); actions.openDialog('saveSession'); }}>
              <Icon name="save" /> Save
            </button>
            {current ? (
              <a className="btn small ghost" href={links.sessionHar(current.id)} download onClick={() => setOpen(false)}>
                <Icon name="download" /> Export HAR
              </a>
            ) : null}
            <button
              className="btn small ghost danger"
              disabled={!current || isLive}
              title={isLive ? 'The live session cannot be deleted' : 'Delete this session'}
              onClick={() => { setOpen(false); setConfirm('delete'); }}
            >
              <Icon name="trash" /> Delete
            </button>
          </div>
        </div>
      )}
      {confirm === 'delete' && current && (
        <ConfirmDialog title={`Delete session #${current.number}?`} confirmLabel="Delete" danger onClose={() => setConfirm(null)} onConfirm={() => void actions.deleteSession(current.id)}>
          <p>All {pluralize('request', current.requests)} captured in this session are permanently deleted.</p>
        </ConfirmDialog>
      )}
      {confirm === 'clear' && current && (
        <ConfirmDialog title={`Clear session #${current.number}?`} confirmLabel="Clear" danger onClose={() => setConfirm(null)} onConfirm={() => void actions.clearSession(current.id)}>
          <p>All captured requests of this session are removed. Capture continues.</p>
        </ConfirmDialog>
      )}
    </div>
  );
}
