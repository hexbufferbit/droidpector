import { useRef, useState } from 'react';
import { ConfirmDialog } from '../../components/ConfirmDialog';
import { Icon } from '../../components/Icon';
import { XK } from '../../lib/keysym';
import { actions, appStore, BUSY_STATES } from '../../state/app';
import { useStore } from '../../state/store';
import { ApkCard } from '../apk/ApkCard';
import { DropZone } from '../apk/DropZone';
import { pickApk } from '../apk/files';
import { DisplayCanvas, pasteFromClipboard, type DisplayHandle } from './DisplayCanvas';

function AppBar({ display }: { display: React.RefObject<DisplayHandle | null> }) {
  const app = useStore(appStore, (s) => s.status?.app);
  const state = useStore(appStore, (s) => s.status?.state ?? 'stopped');
  const displayReady = useStore(appStore, (s) => s.status?.displayReady ?? false);
  const apks = useStore(appStore, (s) => s.apks);
  const [confirm, setConfirm] = useState<'uninstall' | 'clear' | null>(null);
  const busy = BUSY_STATES.has(state);
  const apk = app ? apks.find((a) => a.info?.package === app.package) : undefined;
  const pkg = app?.package ?? '';

  return (
    <div className="app-bar">
      <div className="app-id">
        {app ? (
          <>
            <span className={`dot ${app.running ? 'on' : 'off'}`} title={app.running ? 'Running' : 'Not running'} aria-label={app.running ? 'Running' : 'Not running'} />
            <div className="app-names">
              <span className="app-label">{app.label || app.package}</span>
              <span className="muted mono small">
                {app.package}
                {app.version ? ` · v${app.version}` : ''}
              </span>
            </div>
          </>
        ) : (
          <span className="muted">No app installed</span>
        )}
      </div>
      <div className="app-actions" role="toolbar" aria-label="App controls">
        {app ? (
          <>
            <button className="btn small" disabled={busy} onClick={() => void actions.appAction(pkg, 'launch')} title="Launch the app">
              <Icon name="play" /> Launch
            </button>
            <button className="btn small" disabled={busy || !app.running} onClick={() => void actions.appAction(pkg, 'stop')} title="Force-stop the app">
              <Icon name="stop" /> Stop
            </button>
            <button className="btn small" disabled={busy} onClick={() => setConfirm('clear')} title="Clear the app's data">
              Clear data
            </button>
            <button className="btn small" disabled={busy} onClick={() => setConfirm('uninstall')} title="Uninstall the app">
              Uninstall
            </button>
            <button
              className="btn small"
              disabled={busy || !apk}
              onClick={() => apk && void actions.reinstall(apk.id)}
              title={apk ? 'Install the APK again (keeps nothing)' : 'Add the APK again to reinstall it'}
            >
              Reinstall
            </button>
          </>
        ) : (
          <button className="btn small" onClick={() => pickApk((f) => void actions.uploadApk(f))}>
            <Icon name="upload" /> Install APK
          </button>
        )}
      </div>
      <div className="display-actions" role="toolbar" aria-label="Android navigation">
        <button className="icon-btn" disabled={!displayReady} aria-label="Back" title="Back" onClick={() => display.current?.tapKey(XK.Escape)}>
          <Icon name="back" />
        </button>
        <button className="icon-btn" disabled={!displayReady} aria-label="Home" title="Home" onClick={() => display.current?.tapKey(XK.Home)}>
          <Icon name="home" />
        </button>
        <button className="icon-btn" disabled={!displayReady} aria-label="Paste clipboard text into Android" title="Paste text (Ctrl+V)" onClick={() => void pasteFromClipboard()}>
          <Icon name="paste" />
        </button>
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

export function AndroidPane() {
  const display = useRef<DisplayHandle>(null);
  const status = useStore(appStore, (s) => s.status);
  const upload = useStore(appStore, (s) => s.upload);
  const hideDropZone = useStore(appStore, (s) => s.hideDropZone);
  const state = status?.state ?? 'stopped';
  const displayReady = status?.displayReady ?? false;
  const busy = BUSY_STATES.has(state);
  const showDrop = !status?.app && !upload && !hideDropZone;

  let overlay: React.ReactNode = null;
  if (!displayReady) {
    if (busy)
      overlay = (
        <div className="stage-overlay">
          <span className="spinner large" aria-hidden="true" />
          <p className="overlay-title">Starting Android…</p>
          <p className="muted">{status?.message ? `${status.message} ` : ''}The first start can take a few minutes.</p>
        </div>
      );
    else if (!showDrop)
      overlay = (
        <div className="stage-overlay">
          <Icon name="android" size={40} />
          <p className="overlay-title">Android is not running</p>
          <button className="btn primary" onClick={() => void actions.start()}>
            <Icon name="play" /> Start sandbox
          </button>
        </div>
      );
  }

  return (
    <section className="android-pane" aria-label="Android">
      <AppBar display={display} />
      <div className="android-stage">
        <DisplayCanvas ref={display} active={displayReady} />
        {overlay}
        {showDrop && <DropZone canShowAndroid={displayReady} />}
        {upload && (
          <div className="apk-card-wrap">
            <ApkCard upload={upload} />
          </div>
        )}
      </div>
    </section>
  );
}
