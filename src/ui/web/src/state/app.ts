// Application state (sandbox status, sessions, selection, dialogs) and the
// controller actions that talk to the core.
import { api, toApiError, wsUrl, type ApiError } from '../api/client';
import type { APKEntry, ErrorInfo, Info, Session, Status, WSMessage } from '../api/types';
import { ReconnectingSocket, type SocketState } from '../api/ws';
import { createStore, Emitter } from './store';

export interface Toast {
  id: number;
  text: string;
  kind: 'info' | 'error';
}

export interface ErrorDialogState {
  error: ErrorInfo;
  /** offer "Retry" that starts the sandbox again */
  retryStart?: boolean;
}

export type UploadPhase = 'uploading' | 'analyzed' | 'starting' | 'failed';

export interface UploadState {
  fileName: string;
  size: number;
  loaded: number;
  phase: UploadPhase;
  entry?: APKEntry;
  error?: ErrorInfo;
}

export type DialogKind = 'snapshots' | 'reset' | 'diagnostics' | 'saveSession' | null;

export interface AppStateShape {
  info: Info | null;
  status: Status | null;
  connection: SocketState;
  everConnected: boolean;
  sessions: Session[];
  /** session chosen explicitly by the user; null follows the live session */
  pinnedSessionId: string | null;
  selectedEventId: string | null;
  errorDialog: ErrorDialogState | null;
  dismissedWarnings: string[];
  toasts: Toast[];
  upload: UploadState | null;
  apks: APKEntry[];
  dialog: DialogKind;
  /** the user dismissed the "no app installed" drop zone to look at Android */
  hideDropZone: boolean;
}

export const appStore = createStore<AppStateShape>({
  info: null,
  status: null,
  connection: 'connecting',
  everConnected: false,
  sessions: [],
  pinnedSessionId: null,
  selectedEventId: null,
  errorDialog: null,
  dismissedWarnings: [],
  toasts: [],
  upload: null,
  apks: [],
  dialog: null,
  hideDropZone: false,
});

/** Fired with a session id whenever its event list changed. */
export const eventsChanged = new Emitter<[string]>();
/** Fired when the core connection was (re)established: every view resyncs. */
export const resynced = new Emitter<[]>();
/** Fired when a newly created event (e.g. a replay) should be revealed. */
export const revealEvent = new Emitter<[string]>();

export const BUSY_STATES = new Set(['preparing', 'starting', 'booting', 'provisioning', 'installing', 'launching', 'stopping', 'recovering']);
export const RUNNING_STATES = new Set(['ready', 'installing', 'launching']);

/** liveSessionId is the session capture currently writes to. */
export function liveSessionId(s: Pick<AppStateShape, 'status' | 'sessions'>): string | null {
  if (s.status?.sessionId) return s.status.sessionId;
  const open = s.sessions.find((x) => !x.endedAt);
  return open?.id ?? null;
}

/** viewedSessionId is the session shown in the Network panel. */
export function viewedSessionId(s: Pick<AppStateShape, 'status' | 'sessions' | 'pinnedSessionId'>): string | null {
  if (s.pinnedSessionId && s.sessions.some((x) => x.id === s.pinnedSessionId)) return s.pinnedSessionId;
  return liveSessionId(s) ?? s.sessions[0]?.id ?? null;
}

// ---- toasts & errors -------------------------------------------------------------

let toastSeq = 0;
export function toast(text: string, kind: Toast['kind'] = 'info', ms = 2500): void {
  const id = ++toastSeq;
  appStore.set((s) => ({ toasts: [...s.toasts, { id, text, kind }] }));
  setTimeout(() => appStore.set((s) => ({ toasts: s.toasts.filter((t) => t.id !== id) })), ms);
}

export function showError(err: unknown, retryStart = false): ApiError {
  const e = toApiError(err);
  appStore.set({ errorDialog: { error: e.toInfo(), retryStart } });
  return e;
}

/** run executes an action and reports failures in the error dialog. */
export async function run<T>(fn: () => Promise<T>, opts: { retryStart?: boolean } = {}): Promise<T | undefined> {
  try {
    return await fn();
  } catch (err) {
    showError(err, opts.retryStart);
    return undefined;
  }
}

// ---- status -----------------------------------------------------------------------

export function applyStatus(next: Status): void {
  const prev = appStore.get().status;
  appStore.set({ status: next });
  if (next.state === 'error' && next.error && (prev?.state !== 'error' || prev.since !== next.since)) {
    appStore.set({ errorDialog: { error: next.error, retryStart: true } });
  }
  if (next.app && !prev?.app) appStore.set({ hideDropZone: false });
  if (prev?.sessionId !== next.sessionId) void refreshSessions();
  if (prev?.app?.package !== next.app?.package) void refreshApks();
  // Clear the upload card once the app it installed is running.
  const up = appStore.get().upload;
  if (up?.entry?.info && next.app?.package === up.entry.info.package && next.state === 'ready') {
    appStore.set({ upload: null });
  }
}

// ---- sessions ----------------------------------------------------------------------

let sessionsInflight: Promise<void> | null = null;
export function refreshSessions(): Promise<void> {
  if (sessionsInflight) return sessionsInflight;
  sessionsInflight = api
    .sessions()
    .then((list) => appStore.set({ sessions: [...(list ?? [])].sort((a, b) => b.number - a.number) }))
    .catch(() => undefined)
    .finally(() => {
      sessionsInflight = null;
    });
  return sessionsInflight;
}

export async function refreshApks(): Promise<void> {
  try {
    appStore.set({ apks: (await api.apks()) ?? [] });
  } catch {
    // non-fatal
  }
}

function handleMessage(data: string | ArrayBuffer): void {
  if (typeof data !== 'string') return;
  let msg: WSMessage;
  try {
    msg = JSON.parse(data) as WSMessage;
  } catch {
    return;
  }
  if (msg.type === 'status' && msg.status) {
    applyStatus(msg.status);
  } else if (msg.type === 'events' && msg.sessionId) {
    const { sessions } = appStore.get();
    const idx = sessions.findIndex((s) => s.id === msg.sessionId);
    if (idx < 0) {
      void refreshSessions();
    } else if (msg.stats) {
      const updated = [...sessions];
      updated[idx] = { ...sessions[idx], ...msg.stats };
      appStore.set({ sessions: updated });
    }
    eventsChanged.emit(msg.sessionId);
  }
}

let socket: ReconnectingSocket | null = null;

async function resync(): Promise<void> {
  const [info, status] = await Promise.allSettled([api.info(), api.status()]);
  if (info.status === 'fulfilled') appStore.set({ info: info.value });
  if (status.status === 'fulfilled') applyStatus(status.value);
  await Promise.allSettled([refreshSessions(), refreshApks()]);
  resynced.emit();
}

/** start connects to the core. Returns a cleanup function. */
export function startApp(): () => void {
  void resync();
  socket = new ReconnectingSocket({
    url: () => wsUrl('/api/ws'),
    onMessage: handleMessage,
    onState: (state) => {
      const { everConnected, connection } = appStore.get();
      appStore.set({ connection: state, everConnected: everConnected || state === 'open' });
      if (state === 'open' && everConnected && connection !== 'open') void resync();
    },
  });
  const timer = setInterval(() => {
    // Keep session durations fresh even when no traffic arrives.
    if (appStore.get().sessions.some((s) => !s.endedAt)) appStore.set((s) => ({ sessions: [...s.sessions] }));
  }, 30_000);
  return () => {
    socket?.close();
    socket = null;
    clearInterval(timer);
  };
}

// ---- actions --------------------------------------------------------------------------

export const actions = {
  start: () => run(() => api.startSandbox(), { retryStart: true }),
  stop: () => run(() => api.stopSandbox()),
  restart: () => run(() => api.restartSandbox(), { retryStart: true }),
  reset: () => run(() => api.resetSandbox()),

  /** rotate sets the display orientation; older cores without the endpoint keep portrait. */
  async rotate(orientation: number) {
    const o = ((orientation % 4) + 4) % 4;
    try {
      await api.rotate(o);
      appStore.set((s) => (s.status ? { status: { ...s.status, orientation: o } } : {}));
    } catch (err) {
      const e = toApiError(err);
      if (e.status === 404 || e.status === 405 || e.status === 501) toast('This droidpector core cannot rotate the display', 'error');
      else showError(err);
    }
  },

  /** setAppOnly toggles "only capture the app under test"; the status message reports the new value. */
  async setAppOnly(enabled: boolean) {
    const ok = await run(async () => {
      await api.setAppOnly(enabled);
      return true;
    });
    if (ok) {
      appStore.set((s) => (s.status ? { status: { ...s.status, appOnlyTraffic: enabled } } : {}));
      toast(enabled ? 'Only the app under test can reach the network' : 'Android system traffic is captured again');
    }
  },

  selectEvent(id: string | null) {
    appStore.set({ selectedEventId: id });
  },

  selectSession(id: string) {
    const live = liveSessionId(appStore.get());
    appStore.set({ pinnedSessionId: id === live ? null : id, selectedEventId: null });
  },

  async newSession() {
    const s = await run(() => api.startCapture());
    if (s) {
      appStore.set({ pinnedSessionId: null, selectedEventId: null });
      await refreshSessions();
      toast(`Started session #${s.number}`);
    }
  },
  async stopCapture() {
    await run(() => api.stopCapture());
    await refreshSessions();
  },
  async clearSession(id: string) {
    await run(() => api.clearSession(id));
    appStore.set({ selectedEventId: null });
    await refreshSessions();
    eventsChanged.emit(id);
  },
  async saveSession(id: string, name: string) {
    const s = await run(() => api.saveSession(id, name));
    if (s) {
      toast('Session saved');
      await refreshSessions();
    }
  },
  async deleteSession(id: string) {
    const ok = await run(async () => {
      await api.deleteSession(id);
      return true;
    });
    if (ok) {
      appStore.set((s) => ({ pinnedSessionId: s.pinnedSessionId === id ? null : s.pinnedSessionId, selectedEventId: null }));
      await refreshSessions();
      toast('Session deleted');
    }
  },

  appAction(pkg: string, action: 'launch' | 'stop' | 'clear' | 'uninstall') {
    return run(async () => {
      await api.appAction(pkg, action);
      const done = { launch: 'App launched', stop: 'App stopped', clear: 'App data cleared', uninstall: 'App uninstalled' }[action];
      toast(done);
      if (action === 'uninstall') await refreshApks();
    });
  },

  reinstall(apkId: string) {
    return run(() => api.reinstallApk(apkId), { retryStart: true });
  },

  /** uploadApk uploads, analyses and — when there are no blocking problems — runs an APK (the one-click flow). */
  async uploadApk(file: File) {
    appStore.set({ upload: { fileName: file.name, size: file.size, loaded: 0, phase: 'uploading' }, hideDropZone: false });
    let entry: APKEntry;
    try {
      entry = await api.uploadApk(file, (p) =>
        appStore.set((s) => (s.upload ? { upload: { ...s.upload, loaded: p.loaded } } : {})),
      );
    } catch (err) {
      const e = toApiError(err);
      appStore.set({ upload: { fileName: file.name, size: file.size, loaded: file.size, phase: 'failed', error: e.toInfo() } });
      return;
    }
    const blocking = !entry.valid || (entry.problems ?? []).some((p) => p.severity === 'error');
    appStore.set({ upload: { fileName: file.name, size: file.size, loaded: file.size, phase: blocking ? 'analyzed' : 'starting', entry } });
    void refreshApks();
    if (blocking) return;
    try {
      await api.runApk(entry.id);
    } catch (err) {
      const e = showError(err, true);
      appStore.set((s) => (s.upload ? { upload: { ...s.upload, phase: 'failed', error: e.toInfo() } } : {}));
    }
  },

  dismissUpload() {
    appStore.set({ upload: null });
  },

  dismissWarning(w: string) {
    appStore.set((s) => ({ dismissedWarnings: [...s.dismissedWarnings, w] }));
  },

  openDialog(dialog: DialogKind) {
    appStore.set({ dialog });
  },
};
