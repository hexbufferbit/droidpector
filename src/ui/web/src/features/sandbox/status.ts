import type { SandboxState, Status } from '../../api/types';

const DEFAULT_LABELS: Record<SandboxState, string> = {
  stopped: 'Sandbox Stopped',
  preparing: 'Preparing Sandbox...',
  starting: 'Starting Android...',
  booting: 'Starting Android...',
  provisioning: 'Preparing Android...',
  ready: 'Sandbox Ready',
  installing: 'Installing APK...',
  launching: 'Launching...',
  stopping: 'Stopping Sandbox...',
  recovering: 'Recovering Sandbox...',
  error: 'Sandbox Error',
};

export type StatusTone = 'idle' | 'busy' | 'ok' | 'capture' | 'error';

export function statusLabel(st: Status | null): string {
  if (!st) return 'Connecting...';
  if (st.state === 'ready' && st.captureActive && !st.message) return 'Network Capture Active';
  return st.message || DEFAULT_LABELS[st.state] || st.state;
}

export function statusTone(st: Status | null): StatusTone {
  if (!st) return 'busy';
  switch (st.state) {
    case 'ready':
      return st.captureActive ? 'capture' : 'ok';
    case 'error':
      return 'error';
    case 'stopped':
      return 'idle';
    default:
      return 'busy';
  }
}
