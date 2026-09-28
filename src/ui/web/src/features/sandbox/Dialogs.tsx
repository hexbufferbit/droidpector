import { useCallback, useEffect, useState } from 'react';
import { api, links, toApiError } from '../../api/client';
import type { ErrorInfo, Snapshot } from '../../api/types';
import { ConfirmDialog } from '../../components/ConfirmDialog';
import { Dialog } from '../../components/Dialog';
import { ErrorView } from '../../components/ErrorView';
import { Icon } from '../../components/Icon';
import { formatDateTime } from '../../lib/format';
import { actions, appStore, run, toast, viewedSessionId } from '../../state/app';
import { useStore } from '../../state/store';

export function ErrorDialog() {
  const d = useStore(appStore, (s) => s.errorDialog);
  if (!d) return null;
  const close = () => appStore.set({ errorDialog: null });
  return (
    <Dialog
      title="Something went wrong"
      tone="error"
      onClose={close}
      footer={
        <>
          <a className="btn" href={links.diagnosticsBundle()} download>
            <Icon name="download" /> Save diagnostic bundle
          </a>
          <span className="spacer" />
          {d.retryStart && (
            <button
              className="btn primary"
              onClick={() => {
                close();
                void actions.start();
              }}
            >
              <Icon name="restart" /> Retry
            </button>
          )}
          <button className="btn" onClick={close} data-autofocus>
            Close
          </button>
        </>
      }
    >
      <ErrorView error={d.error} />
    </Dialog>
  );
}

function SnapshotsDialog({ onClose }: { onClose: () => void }) {
  const [list, setList] = useState<Snapshot[] | null>(null);
  const [error, setError] = useState<ErrorInfo | null>(null);
  const [name, setName] = useState('');
  const [busy, setBusy] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      setList((await api.snapshots()) ?? []);
      setError(null);
    } catch (err) {
      setError(toApiError(err).toInfo());
      setList(null);
    }
  }, []);
  useEffect(() => void load(), [load]);

  const act = async (fn: () => Promise<unknown>, done: string) => {
    setBusy(true);
    const ok = await run(async () => {
      await fn();
      return true;
    });
    setBusy(false);
    if (ok) {
      toast(done);
      await load();
    }
  };

  const valid = /^[A-Za-z0-9._-]{1,64}$/.test(name);
  return (
    <Dialog title="Snapshots" onClose={onClose} width={560}>
      <p className="muted">Snapshots save the complete state of the running Android sandbox so you can return to it later.</p>
      {error ? (
        <ErrorView error={error.code === 'not_running' ? { ...error, title: 'Start the sandbox to manage snapshots.' } : error} compact />
      ) : !list ? (
        <p className="loading">Loading…</p>
      ) : list.length === 0 ? (
        <p className="empty-state">No snapshots yet.</p>
      ) : (
        <table className="kv-table snapshots">
          <thead>
            <tr>
              <th scope="col">Name</th>
              <th scope="col">Created</th>
              <th scope="col">Size</th>
              <th scope="col">
                <span className="sr-only">Actions</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {list.map((s) => (
              <tr key={s.tag}>
                <td className="mono">{s.tag}</td>
                <td>{formatDateTime(s.created)}</td>
                <td>{s.vmSize}</td>
                <td className="row-actions">
                  <button className="btn small" disabled={busy} onClick={() => void act(() => api.restoreSnapshot(s.tag), `Restoring snapshot ${s.tag}…`)}>
                    Restore
                  </button>
                  <button className="btn small danger" disabled={busy} onClick={() => setConfirmDelete(s.tag)}>
                    Delete
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      <form
        className="inline-form"
        onSubmit={(e) => {
          e.preventDefault();
          if (valid) void act(() => api.saveSnapshot(name), `Snapshot ${name} saved`).then(() => setName(''));
        }}
      >
        <label htmlFor="snap-name">New snapshot</label>
        <input id="snap-name" value={name} placeholder="e.g. logged-in" onChange={(e) => setName(e.target.value)} disabled={!!error} />
        <button className="btn primary" type="submit" disabled={!valid || busy || !!error}>
          <Icon name="camera" /> Save snapshot
        </button>
      </form>
      {name && !valid && <p className="field-error">Use letters, digits, dot, dash or underscore (up to 64).</p>}
      {confirmDelete && (
        <ConfirmDialog
          title="Delete snapshot?"
          confirmLabel="Delete"
          danger
          onClose={() => setConfirmDelete(null)}
          onConfirm={() => void act(() => api.deleteSnapshot(confirmDelete), `Snapshot ${confirmDelete} deleted`)}
        >
          <p>
            Snapshot <strong>{confirmDelete}</strong> will be permanently deleted.
          </p>
        </ConfirmDialog>
      )}
    </Dialog>
  );
}

function DiagnosticsDialog({ onClose }: { onClose: () => void }) {
  const [data, setData] = useState<string | null>(null);
  const [error, setError] = useState<ErrorInfo | null>(null);
  const info = useStore(appStore, (s) => s.info);
  useEffect(() => {
    api.diagnostics().then(
      (d) => setData(JSON.stringify(d, null, 2)),
      (err) => setError(toApiError(err).toInfo()),
    );
  }, []);
  return (
    <Dialog
      title="Diagnostics"
      onClose={onClose}
      width={640}
      footer={
        <>
          <a className="btn primary" href={links.diagnosticsBundle()} download>
            <Icon name="download" /> Save diagnostic bundle
          </a>
          <button className="btn" onClick={onClose}>
            Close
          </button>
        </>
      }
    >
      <p className="muted">droidpector {info?.version ?? ''}. The diagnostic bundle contains logs and settings (no captured traffic) to attach to a bug report.</p>
      {error ? <ErrorView error={error} compact /> : <pre className="code-view mono diag">{data ?? 'Loading…'}</pre>}
    </Dialog>
  );
}

function SaveSessionDialog({ onClose }: { onClose: () => void }) {
  const sessionId = useStore(appStore, viewedSessionId);
  const session = useStore(appStore, (s) => s.sessions.find((x) => x.id === sessionId));
  const [name, setName] = useState(session?.name ?? '');
  if (!session) return null;
  return (
    <Dialog
      title={`Save session #${session.number}`}
      onClose={onClose}
      width={420}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button
            className="btn primary"
            form="save-session"
            type="submit"
          >
            Save
          </button>
        </>
      }
    >
      <form
        id="save-session"
        onSubmit={(e) => {
          e.preventDefault();
          onClose();
          void actions.saveSession(session.id, name.trim());
        }}
      >
        <label htmlFor="session-name">Name</label>
        <input id="session-name" className="full" value={name} maxLength={120} placeholder="e.g. Login flow" onChange={(e) => setName(e.target.value)} />
        <p className="muted small">Saved sessions are kept when old sessions are cleaned up automatically.</p>
      </form>
    </Dialog>
  );
}

export function Dialogs() {
  const dialog = useStore(appStore, (s) => s.dialog);
  const close = () => actions.openDialog(null);
  switch (dialog) {
    case 'snapshots':
      return <SnapshotsDialog onClose={close} />;
    case 'diagnostics':
      return <DiagnosticsDialog onClose={close} />;
    case 'saveSession':
      return <SaveSessionDialog onClose={close} />;
    case 'reset':
      return (
        <ConfirmDialog title="Reset sandbox?" confirmLabel="Reset sandbox" danger onClose={close} onConfirm={() => void actions.reset()}>
          <p>
            <strong>This deletes all installed apps and data</strong> inside the Android sandbox and returns it to a fresh state. Captured network
            sessions are kept.
          </p>
        </ConfirmDialog>
      );
    default:
      return null;
  }
}
