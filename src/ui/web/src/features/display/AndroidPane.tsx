import { useRef, useState } from 'react';
import { ConfirmDialog } from '../../components/ConfirmDialog';
import { ContextMenu, type MenuItem } from '../../components/ContextMenu';
import { Icon } from '../../components/Icon';
import { XK } from '../../lib/keysym';
import { actions, appStore, BUSY_STATES } from '../../state/app';
import { useStore } from '../../state/store';
import { ApkCard } from '../apk/ApkCard';
import { DropZone } from '../apk/DropZone';
import { pickApk } from '../apk/files';
import { DisplayCanvas, pasteFromClipboard, type DisplayHandle } from './DisplayCanvas';
import { displayStore, zoomActions } from './displayState';
import { normalizeOrientation, ZOOM_MAX, ZOOM_MIN } from './protocol';

/** AppCard is the compact app identity strip above the phone (label, package, version, actions). */
function AppCard() {
  const app = useStore(appStore, (s) => s.status?.app);
  const state = useStore(appStore, (s) => s.status?.state ?? 'stopped');
  const apks = useStore(appStore, (s) => s.apks);
  const [confirm, setConfirm] = useState<'uninstall' | 'clear' | null>(null);
  const [menu, setMenu] = useState<{ x: number; y: number } | null>(null);
  const busy = BUSY_STATES.has(state);
  const apk = app ? apks.find((a) => a.info?.package === app.package) : undefined;
  const pkg = app?.package ?? '';

  const items: MenuItem[] = [
    { id: 'clear', label: 'Clear app data…', icon: 'trash', disabled: busy, onSelect: () => setConfirm('clear') },
    {
      id: 'reinstall',
      label: 'Reinstall APK',
      icon: 'restart',
      disabled: busy || !apk,
      title: apk ? 'Install the APK again (keeps nothing)' : 'Add the APK again to reinstall it',
      onSelect: () => apk && void actions.reinstall(apk.id),
    },
    { id: 'uninstall', label: 'Uninstall…', icon: 'close', danger: true, separatorBefore: true, disabled: busy, onSelect: () => setConfirm('uninstall') },
  ];

  return (
    <div className="app-card" role="region" aria-label="App under test">
      <div className="app-card-icon" aria-hidden="true">
        <Icon name="android" size={18} />
      </div>
      <div className="app-card-text">
        {app ? (
          <>
            <div className="app-card-title">
              <span className={`dot ${app.running ? 'on' : 'off'}`} title={app.running ? 'Running' : 'Not running'} aria-label={app.running ? 'Running' : 'Not running'} />
              <span className="app-label">{app.label || app.package}</span>
            </div>
            <div className="app-card-sub mono">
              {app.package}
              {app.version ? <span className="app-version"> · v{app.version}</span> : null}
            </div>
          </>
        ) : (
          <>
            <div className="app-card-title">
              <span className="app-label muted">No app installed</span>
            </div>
            <div className="app-card-sub muted">Drop an APK anywhere to install and run it.</div>
          </>
        )}
      </div>
      <div className="app-card-actions" role="toolbar" aria-label="App controls">
        {app ? (
          <>
            <button className="btn pill small" disabled={busy} onClick={() => void actions.appAction(pkg, 'launch')} title="Launch the app">
              <Icon name="play" size={12} /> Launch
            </button>
            <button className="btn pill small" disabled={busy || !app.running} onClick={() => void actions.appAction(pkg, 'stop')} title="Force-stop the app">
              <Icon name="stop" size={12} /> Stop
            </button>
            <button
              className="icon-btn"
              aria-label="More app actions"
              title="More"
              aria-haspopup="menu"
              aria-expanded={!!menu}
              onClick={(e) => {
                const r = e.currentTarget.getBoundingClientRect();
                setMenu(menu ? null : { x: r.right - 220, y: r.bottom + 4 });
              }}
            >
              <Icon name="more" />
            </button>
            {menu && <ContextMenu x={menu.x} y={menu.y} items={items} onClose={() => setMenu(null)} label="More app actions" />}
          </>
        ) : (
          <button className="btn pill small" onClick={() => pickApk((f) => void actions.uploadApk(f))} title="Install and run an APK">
            <Icon name="upload" size={12} /> Install APK
          </button>
        )}
      </div>
      {confirm === 'uninstall' && (
        <ConfirmDialog title="Uninstall app?" confirmLabel="Uninstall" danger onConfirm={() => void actions.appAction(pkg, 'uninstall')} onClose={() => setConfirm(null)}>
          <p>
            <strong>{app?.label || pkg}</strong> and all of its data will be removed from the sandbox.
          </p>
        </ConfirmDialog>
      )}
      {confirm === 'clear' && (
        <ConfirmDialog title="Clear app data?" confirmLabel="Clear data" danger onConfirm={() => void actions.appAction(pkg, 'clear')} onClose={() => setConfirm(null)}>
          <p>All data of {app?.label || pkg} (accounts, settings, caches) is deleted, as if it was just installed.</p>
        </ConfirmDialog>
      )}
    </div>
  );
}

const ORIENTATION_NAMES = ['Portrait', 'Landscape', 'Portrait (upside down)', 'Landscape (reversed)'];

function NavBar({ display, enabled, orientation }: { display: React.RefObject<DisplayHandle | null>; enabled: boolean; orientation: number }) {
  const zoom = useStore(displayStore, (s) => s.zoom);
  const scale = useStore(displayStore, (s) => s.effectiveScale);
  const pct = scale ? `${Math.round(scale * 100)}%` : '—';
  const fit = zoom.mode === 'fit';
  return (
    <div className="phone-nav" role="toolbar" aria-label="Android navigation">
      <div className="phone-nav-group">
        <button className="btn pill" disabled={!enabled} aria-label="Back" title="Back" onClick={() => display.current?.tapKey(XK.Escape)}>
          <Icon name="back" size={13} /> Back
        </button>
        <button className="btn pill" disabled={!enabled} aria-label="Home" title="Home" onClick={() => display.current?.tapKey(XK.Home)}>
          <Icon name="home" size={13} /> Home
        </button>
        <button
          className="btn pill"
          disabled={!enabled}
          aria-label="Paste clipboard text into Android"
          title="Paste text (Ctrl+V)"
          onClick={() => void pasteFromClipboard()}
        >
          <Icon name="paste" size={13} /> Paste
        </button>
      </div>
      <div className="phone-nav-group zoom-group" role="group" aria-label="Zoom">
        <button className="icon-btn" aria-label="Zoom out" title="Zoom out (Ctrl+wheel)" disabled={scale <= ZOOM_MIN} onClick={() => zoomActions.step(-1)}>
          <span className="zoom-glyph" aria-hidden="true">−</span>
        </button>
        <span className="zoom-readout num" aria-live="polite" title={fit ? 'Scaled to fit the pane' : 'Fixed zoom'}>
          {pct}
        </span>
        <button className="icon-btn" aria-label="Zoom in" title="Zoom in (Ctrl+wheel)" disabled={scale >= ZOOM_MAX} onClick={() => zoomActions.step(1)}>
          <span className="zoom-glyph" aria-hidden="true">+</span>
        </button>
        <button className={`btn pill small${fit ? ' active' : ''}`} aria-pressed={fit} title="Scale the phone to the available space" onClick={zoomActions.fit}>
          Fit
        </button>
        <button
          className={`btn pill small${!fit && zoom.scale === 1 ? ' active' : ''}`}
          aria-pressed={!fit && zoom.scale === 1}
          title="100%: one framebuffer pixel per screen pixel"
          onClick={zoomActions.oneToOne}
        >
          1:1
        </button>
        <button
          className="btn pill small rotate-btn"
          disabled={!enabled}
          aria-label={`Rotate (${ORIENTATION_NAMES[orientation]})`}
          title={`Rotate the display (Ctrl+→ / Ctrl+←) · ${ORIENTATION_NAMES[orientation]}`}
          data-orientation={orientation}
          onClick={() => void actions.rotate(orientation + 1)}
        >
          <Icon name="phone" size={13} style={{ transform: `rotate(${orientation * 90}deg)`, transition: 'transform 200ms ease' }} /> Rotate
        </button>
      </div>
    </div>
  );
}

export function AndroidPane() {
  const display = useRef<DisplayHandle>(null);
  const status = useStore(appStore, (s) => s.status);
  const upload = useStore(appStore, (s) => s.upload);
  const hideDropZone = useStore(appStore, (s) => s.hideDropZone);
  const state = status?.state ?? 'stopped';
  const displayReady = status?.displayReady ?? false;
  const orientation = normalizeOrientation(status?.orientation);
  const busy = BUSY_STATES.has(state);
  const showDrop = !status?.app && !upload && !hideDropZone;

  let overlay: React.ReactNode;
  if (busy)
    overlay = (
      <div className="screen-message" role="status">
        <span className="spinner large" aria-hidden="true" />
        <p className="screen-title">Starting Android…</p>
        <p className="screen-sub">{status?.message || 'The first start can take a few minutes.'}</p>
      </div>
    );
  else if (showDrop) overlay = <DropZone />;
  else if (state === 'error')
    overlay = (
      <div className="screen-message">
        <Icon name="warning" size={28} className="text-error" />
        <p className="screen-title">Sandbox error</p>
        <p className="screen-sub">{status?.error?.title ?? status?.message}</p>
        <button className="btn primary pill" onClick={() => void actions.start()}>
          <Icon name="restart" /> Retry
        </button>
      </div>
    );
  else
    overlay = (
      <div className="screen-message">
        <Icon name="android" size={28} className="text-muted" />
        <p className="screen-title">Android is not running</p>
        <button className="btn primary pill" onClick={() => void actions.start()}>
          <Icon name="play" /> Start sandbox
        </button>
      </div>
    );

  const onKeyDown = (e: React.KeyboardEvent) => {
    if (!(e.ctrlKey || e.metaKey) || e.altKey || !displayReady) return;
    if (e.key === 'ArrowLeft' || e.key === 'ArrowRight') {
      e.preventDefault();
      void actions.rotate(orientation + (e.key === 'ArrowRight' ? 1 : -1));
    }
  };

  return (
    <section className="android-pane" aria-label="Android" onKeyDown={onKeyDown}>
      <AppCard />
      <div className="android-stage">
        <DisplayCanvas ref={display} active={displayReady} orientation={orientation} overlay={overlay} />
        {upload && (
          <div className="apk-card-wrap">
            <ApkCard upload={upload} />
          </div>
        )}
      </div>
      <NavBar display={display} enabled={displayReady} orientation={orientation} />
    </section>
  );
}
