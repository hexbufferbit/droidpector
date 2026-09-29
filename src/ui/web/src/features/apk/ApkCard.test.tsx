import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import type { APKEntry, Status } from '../../api/types';
import { appStore } from '../../state/app';
import { ApkCard } from './ApkCard';
import { firstApk, isApkFile } from './files';

const entry: APKEntry = {
  id: 'a1',
  fileName: 'shop.apk',
  size: 12_345_678,
  uploaded: '2026-09-28T13:00:00Z',
  info: { size: 12_345_678, package: 'com.example.shop', versionCode: 42, versionName: '4.2.0', minSdk: 24, targetSdk: 34, label: 'Shop', abis: ['arm64-v8a'] },
  runtime: { runtime: 'x86_64', translated: true, reason: 'The APK has ARM-only native code (arm64-v8a); it runs on the x86_64 runtime through the ARM translation layer.' },
  valid: true,
  problems: [{ severity: 'warning', code: 'debuggable', message: 'The app is debuggable.' }],
};

describe('ApkCard', () => {
  it('shows the analysis, the translation badge and warnings', () => {
    render(<ApkCard upload={{ fileName: 'shop.apk', size: entry.size, loaded: entry.size, phase: 'analyzed', entry }} />);
    expect(screen.getByText('Shop')).toBeTruthy();
    expect(screen.getByText('com.example.shop')).toBeTruthy();
    expect(screen.getByText('24 / 34')).toBeTruthy();
    expect(screen.getByText('arm64-v8a')).toBeTruthy();
    expect(screen.getByText('runs through ARM translation')).toBeTruthy();
    expect(screen.getByText('The app is debuggable.').closest('li')?.className).toBe('sev-warning');
    expect(screen.queryByText(/cannot be installed/)).toBeNull();
  });

  it('reports install progress by itself while Android boots', () => {
    appStore.set({ status: { state: 'booting', since: new Date(Date.now() - 30_000).toISOString(), accelerated: true } as Status });
    render(<ApkCard upload={{ fileName: 'shop.apk', size: entry.size, loaded: entry.size, phase: 'starting', entry }} />);
    expect(screen.getByText(/Step 1 of 3: Booting Android/)).toBeTruthy();
    const bar = screen.getByRole('progressbar');
    const pct = Number(bar.getAttribute('aria-valuenow'));
    expect(pct).toBeGreaterThan(5);
    expect(pct).toBeLessThan(65);
    expect(screen.getByText(`${pct}%`)).toBeTruthy();
    // The analysis is collapsed so the card does not cover the phone.
    expect((screen.getByText('24 / 34').closest('details') as HTMLDetailsElement).open).toBe(false);
  });

  it('blocks installation when there are error problems', () => {
    const bad: APKEntry = { ...entry, valid: false, runtime: { runtime: '', translated: false, reason: '' }, info: undefined, problems: [{ severity: 'error', code: 'not_zip', message: 'The file is not a ZIP archive.' }] };
    render(<ApkCard upload={{ fileName: 'bad.apk', size: 10, loaded: 10, phase: 'analyzed', entry: bad }} />);
    expect(screen.getByText('The file is not a ZIP archive.').closest('li')?.className).toBe('sev-error');
    expect(screen.getByText(/cannot be installed/)).toBeTruthy();
    expect(screen.queryByRole('progressbar')).toBeNull();
  });

  it('shows upload progress', () => {
    render(<ApkCard upload={{ fileName: 'big.apk', size: 2_000_000, loaded: 500_000, phase: 'uploading' }} />);
    expect(screen.getByText(/Uploading big.apk · 500 kB of 2.0 MB/)).toBeTruthy();
  });
});

describe('APK file helpers', () => {
  it('prefers .apk files', () => {
    const a = new File(['x'], 'notes.txt');
    const b = new File(['x'], 'App.APK');
    expect(isApkFile(b)).toBe(true);
    expect(firstApk([a, b])).toBe(b);
    expect(firstApk([a])).toBe(a);
    expect(firstApk([])).toBeNull();
  });
});
