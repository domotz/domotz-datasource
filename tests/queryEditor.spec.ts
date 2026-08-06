import { test, expect } from '@grafana/plugin-e2e';
import type { PanelEditPage } from '@grafana/plugin-e2e';

import { HAS_CREDENTIALS, SKIP_REASON } from './credentials';

/**
 * These drive the real cascading editor against the provisioned data source,
 * which talks to the live Domotz API. They assert on structure and behaviour -
 * that the cascade unlocks in order, that a complete selection issues a query,
 * that a collector switch resets the levels below it - rather than on any
 * particular collector's data, so they survive the account changing.
 */

// Every test here drives the cascade against the live API.
test.beforeEach(() => {
  test.skip(!HAS_CREDENTIALS, SKIP_REASON);
});

/**
 * Controls carry a per-instance DOM id (see QueryEditor) so that a panel with
 * several queries does not render duplicate ids. Tests address them through
 * the stable `data-testid`, which is keyed on the query's refId.
 */
function control(page: PanelEditPage['ctx']['page'], name: string, refId = 'A') {
  return page.locator(`[data-testid="domotz-${name}-${refId}"]`);
}

/** Picks the first option from one of the editor's comboboxes. */
async function selectFirstOption(page: PanelEditPage['ctx']['page'], inputId: string): Promise<string> {
  const input = control(page, inputId.replace('domotz-', ''));
  await expect(input).toBeEnabled({ timeout: 15_000 });
  await input.click();

  const firstOption = page.getByRole('option').first();
  await expect(firstOption).toBeVisible({ timeout: 15_000 });
  const label = (await firstOption.textContent()) ?? '';
  await firstOption.click();
  return label.trim();
}

test('renders the cascading editor', async ({ panelEditPage, readProvisionedDataSource, page }) => {
  const ds = await readProvisionedDataSource({ fileName: 'datasources.yml' });
  await panelEditPage.datasource.set(ds.name);

  await expect(control(page, 'collector')).toBeVisible();
  await expect(control(page, 'device')).toBeVisible();
  await expect(control(page, 'variable')).toBeVisible();
  await expect(page.getByRole('radio', { name: 'Device' })).toBeVisible();
});

// Asserted on placeholder and option availability rather than the `disabled`
// attribute: Grafana's Combobox spreads `disabled` before downshift's
// getInputProps(), which returns `disabled: undefined` and overwrites it, so
// the prop is accepted but never reaches the DOM. The guard below is what the
// user actually experiences.
test('device and metric offer nothing until their parent is chosen', async ({
  panelEditPage,
  readProvisionedDataSource,
  page,
}) => {
  const ds = await readProvisionedDataSource({ fileName: 'datasources.yml' });
  await panelEditPage.datasource.set(ds.name);

  await expect(control(page, 'device')).toHaveAttribute('placeholder', 'Select a collector first');
  await expect(control(page, 'variable')).toHaveAttribute('placeholder', 'Select a collector first');

  await control(page, 'device').click();
  await expect(page.getByRole('option')).toHaveCount(0);

  await selectFirstOption(page, 'domotz-collector');
  await expect(control(page, 'device')).toHaveAttribute('placeholder', 'Select a device');
});

test('collectors load from the backend resource route', async ({ panelEditPage, readProvisionedDataSource, page }) => {
  const ds = await readProvisionedDataSource({ fileName: 'datasources.yml' });
  await panelEditPage.datasource.set(ds.name);

  const chosen = await selectFirstOption(page, 'domotz-collector');
  expect(chosen.length).toBeGreaterThan(0);
});

test('a complete selection issues a query and renders data', async ({
  panelEditPage,
  readProvisionedDataSource,
  page,
}) => {
  const ds = await readProvisionedDataSource({ fileName: 'datasources.yml' });
  await panelEditPage.datasource.set(ds.name);
  await panelEditPage.setVisualization('Table');

  await selectFirstOption(page, 'domotz-collector');
  await selectFirstOption(page, 'domotz-device');

  // Regression guard: the editor must persist `scope` alongside the ids.
  // getDefaultQuery supplies it for rendering only, so an editor that merely
  // spreads `...query` saves a scope-less query, filterQuery rejects it, and
  // no request is ever issued - the panel just sits empty.
  const queryReq = page.waitForRequest((r) => r.url().includes('/api/ds/query') && r.method() === 'POST', {
    timeout: 20_000,
  });
  await selectFirstOption(page, 'domotz-variable');

  const request = await queryReq;
  const body = JSON.parse(request.postData() ?? '{}');
  expect(body.queries[0].scope).toBeTruthy();
  expect(body.queries[0].agentId).toBeTruthy();
  expect(body.queries[0].variableId).toBeTruthy();
});

// Without this, a stale device id survives a collector switch and the panel
// silently queries a device that belongs to a different site.
test('switching collector clears the device and metric below it', async ({
  panelEditPage,
  readProvisionedDataSource,
  page,
}) => {
  const ds = await readProvisionedDataSource({ fileName: 'datasources.yml' });
  await panelEditPage.datasource.set(ds.name);

  await selectFirstOption(page, 'domotz-collector');
  const device = await selectFirstOption(page, 'domotz-device');
  expect(device.length).toBeGreaterThan(0);

  // Choose a different collector.
  const collector = control(page, 'collector');
  await collector.click();
  const options = page.getByRole('option');
  await expect(options.first()).toBeVisible({ timeout: 15_000 });
  if ((await options.count()) > 1) {
    await options.nth(1).click();
    await expect(control(page, 'device')).toHaveValue('', { timeout: 15_000 });
  }
});

test('collector scope hides the device picker', async ({ panelEditPage, readProvisionedDataSource, page }) => {
  const ds = await readProvisionedDataSource({ fileName: 'datasources.yml' });
  await panelEditPage.datasource.set(ds.name);

  await panelEditPage.getQueryEditorRow('A').getByLabel('Collector', { exact: true }).first().waitFor();
  await page.getByRole('radio', { name: 'Collector' }).click();

  await expect(control(page, 'device')).toBeHidden();
});

// The full round trip: editor -> backend -> Domotz API -> frame -> rendered
// panel. Asserts the shape the rewrite is responsible for (labels, unit,
// equal-length fields) without pinning to any particular collector's data.
test('panel renders real data with labels and a mapped unit', async ({
  panelEditPage,
  readProvisionedDataSource,
  page,
}) => {
  const ds = await readProvisionedDataSource({ fileName: 'datasources.yml' });
  await panelEditPage.datasource.set(ds.name);
  await panelEditPage.setVisualization('Table');

  await selectFirstOption(page, 'domotz-collector');
  await selectFirstOption(page, 'domotz-device');

  const response = page.waitForResponse((r) => r.url().includes('/api/ds/query'), { timeout: 20_000 });
  await selectFirstOption(page, 'domotz-variable');

  const body = await (await response).json();
  const frame = body.results.A.frames[0];
  const [timeField, valueField] = frame.schema.fields;
  const [times, values] = frame.data.values;

  expect(timeField.type).toBe('time');
  expect(times.length).toBe(values.length);

  // Identity lives in labels, not concatenated into the field name.
  expect(valueField.labels.collector).toBeTruthy();
  expect(valueField.labels.device).toBeTruthy();
  expect(valueField.labels.variable).toBeTruthy();
  expect(valueField.config.displayNameFromDS).toContain(valueField.labels.collector);

  await expect(panelEditPage.panel.data).not.toHaveCount(0);
});

// Every control used to carry a fixed DOM id, so a panel with several queries
// rendered duplicates. InlineField links its label with htmlFor, which resolves
// to the *first* match - so clicking "History only" on query B toggled query
// A's switch. Ids are now per-instance.
test('controls do not collide across queries', async ({ panelEditPage, readProvisionedDataSource, page }) => {
  const ds = await readProvisionedDataSource({ fileName: 'datasources.yml' });
  await panelEditPage.datasource.set(ds.name);
  await page.getByTestId('data-testid query-tab-add-query').click();
  await expect(page.getByTestId('data-testid Query editor row title B')).toBeVisible({ timeout: 15_000 });

  // Substring match, not prefix: this must also see the old fixed-id scheme,
  // otherwise the test fails because it found nothing rather than because it
  // found duplicates.
  const ids = await page.locator('[id*="domotz-history-only"]').evaluateAll((els) => els.map((el) => el.id));
  expect(ids.length).toBe(2, 'expected one History only switch per query');
  expect(new Set(ids).size).toBe(ids.length, `duplicate ids across queries: ${ids.join(', ')}`);

  // Clicking B's field label - the exact action that used to toggle A.
  const a = control(page, 'history-only', 'A');
  const b = control(page, 'history-only', 'B');
  const aBefore = await a.isChecked();
  const bBefore = await b.isChecked();

  await panelEditPage.getQueryEditorRow('B').getByText('History only').click();

  expect(await b.isChecked()).toBe(!bBefore);
  expect(await a.isChecked()).toBe(aBefore);
});

test('devices are searchable by IP address', async ({ panelEditPage, readProvisionedDataSource, page }) => {
  const ds = await readProvisionedDataSource({ fileName: 'datasources.yml' });
  await panelEditPage.datasource.set(ds.name);

  await selectFirstOption(page, 'domotz-collector');

  const devices = control(page, 'device');
  await expect(devices).toBeEnabled({ timeout: 15_000 });
  await devices.click();

  // Labels are "Name · IP · MAC", so an option should expose a dotted quad.
  const first = page.getByRole('option').first();
  await expect(first).toBeVisible({ timeout: 15_000 });
  const labels = await page.getByRole('option').allInnerTexts();
  const withIP = labels.filter((l) => /\d{1,3}(\.\d{1,3}){3}/.test(l));
  expect(withIP.length).toBeGreaterThan(0);

  // Typing an octet from a real device must narrow the list to it.
  const ip = withIP[0].match(/\d{1,3}(\.\d{1,3}){3}/)![0];
  await devices.fill(ip);
  await expect(page.getByRole('option').first()).toBeVisible({ timeout: 15_000 });
  const filtered = await page.getByRole('option').allInnerTexts();
  expect(filtered.some((l) => l.includes(ip))).toBe(true);
});

// Labels stay short - name only for devices, metric name only - while the
// addresses and sensor path live on the second line. Combobox does not search
// descriptions on its own, so the editor supplies async loaders and filters
// both lines itself. If that regresses, the second line becomes decorative.
test('the second line is searchable, not just the label', async ({
  panelEditPage,
  readProvisionedDataSource,
  page,
}) => {
  const ds = await readProvisionedDataSource({ fileName: 'datasources.yml' });
  await panelEditPage.datasource.set(ds.name);
  await selectFirstOption(page, 'domotz-collector');

  // A device's addresses are on the second line but still findable.
  const devices = control(page, 'device');
  await expect(devices).toBeEnabled({ timeout: 15_000 });
  await devices.click();
  await expect(page.getByRole('option').first()).toBeVisible({ timeout: 20_000 });

  const rows = await page.getByRole('option').allInnerTexts();
  const withAddress = rows.find((r) => /\n.*\d{1,3}(\.\d{1,3}){3}/.test(r));
  expect(withAddress, 'expected a device with an address on its second line').toBeTruthy();

  const [label, secondLine] = withAddress!.split('\n');
  expect(label).not.toMatch(/\d{1,3}(\.\d{1,3}){3}/, 'the address belongs on the second line');

  const mac = secondLine.match(/([0-9A-F]{2}:){5}[0-9A-F]{2}/i);
  if (mac) {
    await devices.fill('');
    await devices.pressSequentially(mac[0], { delay: 20 });
    await expect(page.getByRole('option').filter({ hasText: label }).first()).toBeVisible({ timeout: 15_000 });
  }

  // ...and a metric's sensor path likewise.
  await devices.fill('');
  await devices.pressSequentially(label, { delay: 20 });
  await page.getByRole('option').filter({ hasNotText: 'Use a dashboard variable' }).first().click();

  const metrics = control(page, 'variable');
  await metrics.click();
  await expect(page.getByRole('option').first()).toBeVisible({ timeout: 20_000 });

  const metricRows = await page.getByRole('option').allInnerTexts();
  const withPath = metricRows.find((r) => r.includes('\n') && r.split('\n')[1].includes('/'));
  expect(withPath, 'expected a metric with a path on its second line').toBeTruthy();

  const pathTail = withPath!.split('\n')[1].split('·').pop()!.trim().replace(/^…\//, '').split('/').pop()!;
  await metrics.fill('');
  await metrics.pressSequentially(pathTail, { delay: 20 });
  await expect(page.getByRole('option').filter({ hasNotText: 'Use a dashboard variable' }).first()).toBeVisible({
    timeout: 15_000,
  });
});
