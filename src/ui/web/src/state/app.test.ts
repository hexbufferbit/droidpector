import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { APKEntry, Status } from '../api/types';

vi.mock('../api/client', async (orig) => ({
  ...(await orig<typeof import('../api/client')>()),
  api: new Proxy({}, { get: () => () => Promise.resolve([]) }),
}));

const { appStore, applyStatus } = await import('./app');

const entry = { id: 'a1', fileName: 'shop.apk', size: 1, uploaded: '', valid: true, info: { package: 'com.example.shop' } } as unknown as APKEntry;
const status = (state: Status['state'], extra: Partial<Status> = {}): Status =>
  ({ state, message: '', accelerated: true, captureActive: false, httpsInspection: true, displayReady: false, since: new Date().toISOString(), ...extra }) as Status;

describe('one-click install card follows the sandbox status', () => {
  beforeEach(() => {
    appStore.set({ status: status('booting'), upload: { fileName: 'shop.apk', size: 1, loaded: 1, phase: 'starting', entry }, errorDialog: null });
  });

  it('clears when the installed app is running', () => {
    applyStatus(status('ready', { app: { package: 'com.example.shop', running: true } }));
    expect(appStore.get().upload).toBeNull();
  });

  it('shows a background failure instead of spinning forever', () => {
    applyStatus(status('error', { error: { title: 'The APK could not be installed.', code: 'INSTALL_FAILED' } }));
    expect(appStore.get().upload?.phase).toBe('failed');
    expect(appStore.get().upload?.error?.title).toBe('The APK could not be installed.');
  });

  it('reports an interrupted install when the sandbox stops', () => {
    applyStatus(status('stopped'));
    expect(appStore.get().upload?.phase).toBe('failed');
    expect(appStore.get().upload?.error?.code).toBe('interrupted');
  });

  it('keeps going through intermediate states', () => {
    for (const s of ['provisioning', 'ready', 'installing', 'launching'] as const) applyStatus(status(s));
    expect(appStore.get().upload?.phase).toBe('starting');
  });
});
