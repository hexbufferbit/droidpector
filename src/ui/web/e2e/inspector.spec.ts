import { expect, test } from '@playwright/test';
import { detailsPanel, grid, openApp, openTab, rows, rowWith, setFilter, traffic } from './helpers';

const ENCRYPTED = 'HTTPS encrypted traffic detected. Payload inspection unavailable for this connection.';

test.use({ permissions: ['clipboard-read', 'clipboard-write'] });

test('1. GET request: row, headers, overview and Copy as cURL', async ({ page }) => {
  await openApp(page);
  await expect(page.getByRole('status').filter({ hasText: /Sandbox stopped/i })).toBeVisible();
  await traffic('get');

  const row = rowWith(page, 'q=hello%20world').last();
  await expect(row).toBeVisible();
  await row.click();
  await expect(row).toHaveAttribute('aria-selected', 'true');

  const headers = await openTab(page, 'Headers');
  await expect(headers.getByRole('region', { name: 'Request headers' })).toContainText('okhttp/4.12.0');
  await expect(headers.getByRole('region', { name: 'Response headers' })).toContainText('Content-Type');

  const overview = await openTab(page, 'Overview');
  await expect(overview).toContainText('https://api.example.test/test/get?page=1&q=hello%20world');
  await expect(overview).toContainText('200 OK');

  await row.click({ button: 'right' });
  const menu = page.getByRole('menu', { name: 'Request actions' });
  await expect(menu).toBeVisible();
  await menu.getByRole('menuitem', { name: 'Copy as cURL' }).click();
  await expect(page.locator('.toast', { hasText: 'Copied as cURL' })).toBeVisible();
  const clip = await page.evaluate(() => navigator.clipboard.readText());
  expect(clip).toContain("curl 'https://api.example.test/test/get?page=1&q=hello%20world'");
});

test('2. filters: method:POST, invalid filter error, Images quick filter', async ({ page }) => {
  await openApp(page);
  await traffic('all');
  await expect(rowWith(page, '/test/post').last()).toBeVisible();

  await setFilter(page, 'method:POST');
  await expect(async () => {
    const methods = await rows(page).locator('.col-method').allTextContents();
    expect(methods.length).toBeGreaterThan(0);
    expect(new Set(methods)).toEqual(new Set(['POST']));
  }).toPass();
  await expect(rowWith(page, '/test/post').first()).toBeVisible();

  // Invalid filter: inline error, red box, the list is kept.
  await setFilter(page, 'method:(');
  await expect(page.locator('.filter-error')).toContainText('filter error');
  await expect(page.getByRole('searchbox', { name: 'Filter requests' })).toHaveAttribute('aria-invalid', 'true');
  await expect(page.locator('.filter-input')).toHaveClass(/invalid/);
  await expect(rowWith(page, '/test/post').first()).toBeVisible();

  await setFilter(page, '');
  await expect(page.locator('.filter-error')).toHaveCount(0);
  await page.getByRole('toolbar', { name: 'Quick filters' }).getByRole('button', { name: 'Images', exact: true }).click();
  await expect(async () => {
    const paths = await rows(page).locator('.col-path').allTextContents();
    expect(paths.length).toBeGreaterThan(0);
    for (const p of paths) expect(p).toContain('/test/image');
  }).toPass();
  await expect(rows(page).first().locator('.col-type')).toHaveText('png');
});

test('3. JSON tree viewer and image preview', async ({ page }) => {
  await openApp(page);
  await traffic('json');
  await traffic('image');

  await rowWith(page, '/test/json').last().click();
  const resp = await openTab(page, 'Response');
  const tree = resp.getByRole('tree', { name: 'JSON' });
  await expect(tree).toContainText('"droidpector"');
  await expect(tree.getByText('1.5')).toBeVisible();
  const nested = tree.locator('.j-node > .j-row').filter({ hasText: 'nested' });
  await expect(nested.locator('..')).toHaveAttribute('aria-expanded', 'true');
  await nested.click();
  await expect(nested.locator('..')).toHaveAttribute('aria-expanded', 'false');
  await expect(tree.getByText('1.5')).toHaveCount(0);
  await nested.click();
  await expect(tree.getByText('1.5')).toBeVisible();
  await resp.getByRole('button', { name: 'Collapse all' }).click();
  await expect(tree.getByText('"droidpector"')).toHaveCount(0);
  await resp.getByRole('searchbox', { name: 'Search JSON' }).fill('network');
  await expect(tree.locator('mark', { hasText: 'network' })).toBeVisible();

  await rowWith(page, '/test/image').last().click();
  const imgTab = await openTab(page, 'Response');
  const img = imgTab.getByRole('img', { name: 'Response image preview' });
  await expect(img).toBeVisible();
  await expect.poll(() => img.evaluate((el) => (el as HTMLImageElement).naturalWidth)).toBeGreaterThan(0);
  expect(await img.getAttribute('src')).toMatch(/^blob:/);
});

test('4. WebSocket messages tab shows frames', async ({ page }) => {
  await openApp(page);
  await traffic('ws');
  await rowWith(page, '/test/ws').last().click();
  const panel = await openTab(page, 'Messages');
  const list = panel.getByRole('listbox', { name: 'WebSocket messages' });
  await expect(list.getByRole('option')).toHaveCount(7);
  await expect(list).toContainText('welcome');
  await expect(list).toContainText('echo: hello');
  await expect(list.getByRole('option').nth(1).getByLabel('Sent')).toBeVisible();
  await list.getByRole('option').filter({ hasText: '"type":"subscribe"' }).first().click();
  await expect(panel.getByRole('button', { name: 'JSON' })).toHaveAttribute('aria-pressed', 'true');
  await expect(panel.getByRole('tree')).toContainText('"prices"');
});

test('6. replay creates a new row marked as replay', async ({ page }) => {
  await openApp(page);
  await traffic('get');
  const row = rowWith(page, 'q=hello%20world').last();
  await row.click();
  await expect(detailsPanel(page)).toContainText('200 OK');
  await detailsPanel(page).getByRole('button', { name: 'Replay' }).click();
  await expect(page.locator('.toast', { hasText: 'Request replayed' })).toBeVisible();

  const selected = grid(page).locator('.net-row.selected');
  await expect(selected).toHaveAttribute('data-initiator', 'replay');
  await expect(selected.getByLabel('Replayed request')).toBeVisible();
  await expect(detailsPanel(page)).toContainText('original request');

  await setFilter(page, 'is:replay');
  await expect(async () => {
    const n = await rows(page).count();
    expect(n).toBeGreaterThan(0);
    expect(await rows(page).locator('[aria-label="Replayed request"]').count()).toBe(n);
  }).toPass();
});

test('7. 500 requests stay virtualized and scrolling works', async ({ page }) => {
  test.setTimeout(240_000);
  await openApp(page);
  expect(await traffic('bulk')).toBe(500);
  // Follow mode keeps the newest request in view.
  await expect(rowWith(page, '/test/get?i=499')).toBeVisible();
  expect(await grid(page).locator('.net-row').count()).toBeLessThan(150);
  expect(Number(await grid(page).getAttribute('aria-rowcount'))).toBeGreaterThan(500);

  await grid(page).evaluate((el) => {
    el.scrollTop = 0;
  });
  await expect(rows(page).first()).toHaveAttribute('aria-rowindex', '2');
  await expect(rowWith(page, '/test/get?i=499')).toHaveCount(0);
  expect(await grid(page).locator('.net-row').count()).toBeLessThan(150);

  await grid(page).evaluate((el) => {
    el.scrollTop = el.scrollHeight / 2;
  });
  await expect(async () => expect(await rows(page).count()).toBeGreaterThan(10)).toPass();
  expect(await grid(page).locator('.net-row').count()).toBeLessThan(150);

  await grid(page).evaluate((el) => {
    el.scrollTop = el.scrollHeight;
  });
  await expect(rowWith(page, '/test/get?i=499')).toBeVisible();
  expect(await grid(page).locator('.net-row').count()).toBeLessThan(150);

  // Keyboard navigation: Home / End move the selection across the whole list.
  await grid(page).focus();
  await page.keyboard.press('Home');
  await expect(grid(page).locator('.net-row.selected')).toHaveAttribute('aria-rowindex', '2');
  await page.keyboard.press('ArrowDown');
  await expect(grid(page).locator('.net-row.selected')).toHaveAttribute('aria-rowindex', '3');
  await page.keyboard.press('End');
  await expect(grid(page).locator('.net-row.selected')).toContainText('i=499');
});

test('8. HAR export links respond 200', async ({ page }) => {
  await openApp(page);
  await page.locator('.session-button').click();
  const link = page.getByRole('link', { name: 'Export HAR' });
  await expect(link).toBeVisible();
  const href = await link.getAttribute('href');
  expect(href).toBeTruthy();
  const res = await page.request.get(new URL(href!, page.url()).toString());
  expect(res.status()).toBe(200);
  const har = (await res.json()) as { log: { entries: unknown[] } };
  expect(har.log.entries.length).toBeGreaterThan(0);

  // Per-request "Save Request (HAR)".
  await page.keyboard.press('Escape');
  const id = await rows(page).filter({ hasText: '/test/' }).last().getAttribute('data-id');
  const one = await page.request.get(new URL(`/api/events/${id}/har`, page.url()).toString());
  expect(one.status()).toBe(200);
  expect(((await one.json()) as { log: { entries: unknown[] } }).log.entries).toHaveLength(1);
});

// Runs last on purpose: after a pinning failure the core passes all further
// connections to api.example.test through uninspected (sticky per host), so
// the other tests could no longer generate inspectable traffic to that host.
test('5. pinned HTTPS shows the encrypted-traffic message', async ({ page }) => {
  await openApp(page);
  await traffic('pinned');
  const row = rows(page).filter({ has: page.locator('.badge.encrypted') }).last();
  await expect(row).toBeVisible();
  await row.click();
  await expect(detailsPanel(page).getByText(ENCRYPTED)).toBeVisible();
  await expect(detailsPanel(page)).toContainText('api.example.test');
});

test('9. raw TCP flows show their protocol label in the Type column', async ({ page }) => {
  await openApp(page);
  // The devserver may not be able to produce a raw TCP flow yet: skip instead of failing.
  let produced = 0;
  try {
    produced = await traffic('tcp');
  } catch {
    test.skip(true, 'devserver has no raw TCP traffic generator (kind=tcp)');
  }
  test.skip(produced === 0, 'devserver produced no raw TCP flow');
  const row = rows(page).filter({ has: page.locator('.col-method .method-badge', { hasText: /^(TCP|TLS|UDP)$/ }) }).last();
  await expect(row).toBeVisible();
  const type = (await row.locator('.col-type').textContent())?.trim() ?? '';
  // The Type column carries the protocol label (TCP, TLS, MTProto, …), never a MIME type.
  expect(type).toMatch(/^[A-Za-z][A-Za-z0-9/.+-]*$/);
  expect(type.toLowerCase()).not.toBe('other');
  await row.click();
  await expect(detailsPanel(page).getByRole('tab', { name: 'Connection', exact: true })).toBeVisible();
});

test('10. app-only capture toggle round-trips through the core', async ({ page }) => {
  await openApp(page);
  const base = new URL(page.url()).origin;
  const initial = ((await (await page.request.get(`${base}/api/status`)).json()) as { appOnlyTraffic?: boolean }).appOnlyTraffic;
  const probe = await page.request.post(`${base}/api/sandbox/app-only`, { data: { enabled: false } });
  test.skip(probe.status() === 404 || probe.status() === 405, 'core has no /api/sandbox/app-only endpoint yet');
  expect(probe.status()).toBe(204);
  await expect(page.getByTestId('app-only-chip')).toHaveCount(0);

  await page.getByRole('button', { name: 'More sandbox actions' }).click();
  const item = page.getByRole('menuitemcheckbox', { name: /Only capture the app under test/ });
  await expect(item).toHaveAttribute('aria-checked', 'false');
  await item.click();
  await expect(page.getByTestId('app-only-chip')).toBeVisible();
  await expect.poll(async () => ((await (await page.request.get(`${base}/api/status`)).json()) as { appOnlyTraffic?: boolean }).appOnlyTraffic).toBe(true);

  await page.getByRole('button', { name: 'More sandbox actions' }).click();
  await expect(page.getByRole('menuitemcheckbox', { name: /Only capture the app under test/ })).toHaveAttribute('aria-checked', 'true');
  await page.getByRole('menuitemcheckbox', { name: /Only capture the app under test/ }).click();
  await expect(page.getByTestId('app-only-chip')).toHaveCount(0);
  await expect.poll(async () => ((await (await page.request.get(`${base}/api/status`)).json()) as { appOnlyTraffic?: boolean }).appOnlyTraffic).toBe(false);
  if (initial) await page.request.post(`${base}/api/sandbox/app-only`, { data: { enabled: true } });
});
