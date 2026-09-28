import { expect, type Locator, type Page } from '@playwright/test';

export function devInfo() {
  const url = process.env.E2E_URL;
  const base = process.env.E2E_BASE;
  const token = process.env.E2E_TOKEN;
  const dev = process.env.E2E_DEV;
  if (!url || !base || !token || !dev) throw new Error('E2E devserver info missing (global setup did not run)');
  return { url, base, token, dev };
}

export type TrafficKind = 'all' | 'get' | 'post' | 'json' | 'image' | 'error' | '401' | 'html' | 'http' | 'ws' | 'pinned' | 'bulk';

/** traffic generates REAL traffic through the simulated guest and the real capture pipeline. */
export async function traffic(kind: TrafficKind): Promise<number> {
  const { dev, token } = devInfo();
  const res = await fetch(`${dev}/dev/traffic?kind=${kind}`, { method: 'POST', headers: { Authorization: `Bearer ${token}` } });
  const text = await res.text();
  if (!res.ok) throw new Error(`traffic ${kind} failed: ${res.status} ${text}`);
  return (JSON.parse(text) as { requests: number }).requests;
}

/** openApp authenticates (sets the session cookie via /auth) and waits for the UI. */
export async function openApp(page: Page): Promise<void> {
  const { url } = devInfo();
  await page.goto(url);
  await expect(page.getByRole('grid', { name: 'Network requests' })).toBeVisible();
  await expect(page.locator('.session-button')).toContainText('Session #');
}

export function grid(page: Page): Locator {
  return page.getByRole('grid', { name: 'Network requests' });
}

export function rows(page: Page): Locator {
  return grid(page).locator('.net-row[data-id]');
}

export function rowWith(page: Page, text: string): Locator {
  return rows(page).filter({ hasText: text });
}

export function detailsPanel(page: Page): Locator {
  return page.getByRole('region', { name: 'Request details' });
}

export async function openTab(page: Page, name: string): Promise<Locator> {
  await detailsPanel(page).getByRole('tab', { name, exact: true }).click();
  return detailsPanel(page).getByRole('tabpanel');
}

export async function setFilter(page: Page, value: string): Promise<void> {
  const box = page.getByRole('searchbox', { name: 'Filter requests' });
  await box.fill(value);
  await box.press('Enter');
}
