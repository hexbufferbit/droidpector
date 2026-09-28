// Window chrome: header status pill, app-only shield chip, toolbar, banners.
import { useState } from 'react';
import { links } from '../../api/client';
import { ContextMenu, type MenuItem } from '../../components/ContextMenu';
import { Icon } from '../../components/Icon';
import { pluralize } from '../../lib/format';
import { actions, appStore, BUSY_STATES, RUNNING_STATES } from '../../state/app';
import { useStore } from '../../state/store';
import { pickApk } from '../apk/files';
import { downloadHref } from '../network/rowActions';
import { statusLabel, statusTone } from './status';

export function StatusPill() {
  const status = useStore(appStore, (s) => s.status);
  const connection = useStore(appStore, (s) => s.connection);
  const tone = connection !== 'open' && status ? 'idle' : statusTone(status);
  const title = status
    ? [status.runtime && `Runtime: ${status.runtime}`, status.android && `Android ${status.android}`, status.accelerator && `Acceleration: ${status.accelerator}`]
        .filter(Boolean)
        .join(' · ')
    : '';
  return (
    <div className={`status-pill tone-${tone}`} role="status" aria-live="polite" title={title || undefined} data-state={status?.state ?? 'unknown'}>
      <span className="status-dot" aria-hidden="true" />
      <span className="status-text">{statusLabel(status)}</span>
    </div>
  );
}

/** AppOnlyChip shows that the sandbox firewall only lets the app under test reach the network. */
export function AppOnlyChip() {
  const on = useStore(appStore, (s) => s.status?.appOnlyTraffic ?? false);
  const blocked = useStore(appStore, (s) => s.status?.blockedFlows ?? 0);
  if (!on) return null;
  const title = `Only the app under test may reach the network; Android system traffic is blocked${blocked ? ` · ${pluralize('blocked flow', blocked)}` : ''}`;
  return (
    <span className="chip shield" title={title} data-testid="app-only-chip">
      <Icon name="shieldCheck" size={12} /> App-only
      {blocked > 0 && <span className="chip-count">{blocked.toLocaleString('en-US')}</span>}
    </span>
  );
}

export function ProgressBar() {
  const state = useStore(appStore, (s) => s.status?.state);
  const uploading = useStore(appStore, (s) => s.upload?.phase === 'uploading');
  if (!uploading && !(state && BUSY_STATES.has(state))) return null;
  return <div className="top-progress" role="progressbar" aria-label="Working" />;
}

export function Toolbar() {
  const state = useStore(appStore, (s) => s.status?.state ?? 'stopped');
  const connected = useStore(appStore, (s) => s.connection === 'open');
  const appOnly = useStore(appStore, (s) => s.status?.appOnlyTraffic ?? false);
  const [menu, setMenu] = useState<{ x: number; y: number } | null>(null);
  const canStart = connected && (state === 'stopped' || state === 'error');
  const canStop = connected && state !== 'stopped' && state !== 'stopping';
  const canRestart = connected && (RUNNING_STATES.has(state) || state === 'error');

  const items: MenuItem[] = [
    {
      id: 'app-only',
      label: 'Only capture the app under test',
      hint: 'Block Android system traffic (sandbox firewall)',
      checked: appOnly,
      onSelect: () => void actions.setAppOnly(!appOnly),
    },
    { id: 'snapshots', label: 'Snapshots…', icon: 'camera', separatorBefore: true, onSelect: () => actions.openDialog('snapshots') },
    { id: 'reset', label: 'Reset sandbox…', icon: 'trash', onSelect: () => actions.openDialog('reset') },
    { id: 'diagnostics', label: 'Diagnostics…', icon: 'info', separatorBefore: true, onSelect: () => actions.openDialog('diagnostics') },
    { id: 'bundle', label: 'Save diagnostic bundle', icon: 'download', onSelect: () => downloadHref(links.diagnosticsBundle()) },
  ];

  return (
    <div className="toolbar" role="toolbar" aria-label="Sandbox">
      <button className="btn primary" onClick={() => pickApk((f) => void actions.uploadApk(f))} title="Install and run an APK (or drop one anywhere)">
        <Icon name="upload" /> Install APK
      </button>
      <span className="toolbar-sep" />
      <button className="btn ghost" disabled={!canStart} onClick={() => void actions.start()} title="Start the Android sandbox">
        <Icon name="play" /> Start
      </button>
      <button className="btn ghost" disabled={!canStop} onClick={() => void actions.stop()} title="Stop the Android sandbox">
        <Icon name="stop" /> Stop
      </button>
      <button className="btn ghost" disabled={!canRestart} onClick={() => void actions.restart()} title="Restart the Android sandbox">
        <Icon name="restart" /> Restart
      </button>
      <span className="spacer" />
      <button
        className="icon-btn more-btn"
        aria-label="More sandbox actions"
        title="Settings and more"
        aria-haspopup="menu"
        aria-expanded={!!menu}
        onClick={(e) => {
          const r = e.currentTarget.getBoundingClientRect();
          setMenu(menu ? null : { x: r.right - 300, y: r.bottom + 4 });
        }}
      >
        <Icon name="more" />
      </button>
      {menu && <ContextMenu x={menu.x} y={menu.y} items={items} onClose={() => setMenu(null)} label="More sandbox actions" />}
    </div>
  );
}

export function Banners() {
  const warnings = useStore(appStore, (s) => s.status?.warnings);
  const dismissed = useStore(appStore, (s) => s.dismissedWarnings);
  const connection = useStore(appStore, (s) => s.connection);
  const everConnected = useStore(appStore, (s) => s.everConnected);
  const visible = (warnings ?? []).filter((w) => !dismissed.includes(w));
  return (
    <>
      {connection !== 'open' && (everConnected || connection === 'closed') && (
        <div className="banner reconnect" role="status">
          <span className="spinner" aria-hidden="true" /> Reconnecting to the droidpector core…
        </div>
      )}
      {visible.map((w) => (
        <div key={w} className="banner warning" role="alert">
          <Icon name="warning" />
          <span className="banner-text">{w}</span>
          <button className="icon-btn small" aria-label="Dismiss warning" title="Dismiss" onClick={() => actions.dismissWarning(w)}>
            <Icon name="close" size={12} />
          </button>
        </div>
      ))}
    </>
  );
}
