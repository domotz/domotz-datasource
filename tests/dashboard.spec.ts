import { test, expect } from '@grafana/plugin-e2e';

import { HAS_CREDENTIALS, SKIP_REASON } from './credentials';

/**
 * Smoke tests for the provisioned starter dashboard.
 *
 * It pins nothing, so unlike the account-specific dashboard it replaced these
 * run against any account with a valid key - no sample-collector guard needed.
 */
test.use({ viewport: { width: 1500, height: 1000 } });

test.beforeEach(() => {
  test.skip(!HAS_CREDENTIALS, SKIP_REASON);
});

test('starter dashboard resolves its variables from nothing', async ({ gotoDashboardPage, page }) => {
  test.setTimeout(120_000);
  await gotoDashboardPage({ uid: 'domotz-starter' });

  const bar = page.locator('[data-testid^="data-testid Dashboard template variables"]');
  await expect(bar.first()).toBeVisible({ timeout: 30_000 });

  // Nothing is pinned in the JSON, so a populated Collector proves the variable
  // query actually ran rather than replaying a saved value.
  await expect(page.getByText('Collector', { exact: true }).first()).toBeVisible({ timeout: 30_000 });
  const text = (await bar.allInnerTexts()).join(' ').replace(/\s+/g, ' ');
  expect(text).toContain('Collector');
  expect(text).toContain('Device');
  expect(text).not.toContain('None');
});

test('no panel errors, even where a variable resolves to nothing', async ({ gotoDashboardPage, page }) => {
  test.setTimeout(120_000);
  await gotoDashboardPage({ uid: 'domotz-starter' });
  await expect(page.locator('[data-testid^="data-testid Panel header"]').first()).toBeVisible({ timeout: 30_000 });

  // A device with no metrics leaves $deviceMetric unresolved. That must read as
  // "no data", not a red panel saying `expected a numeric id`.
  await expect(page.locator('[data-testid="data-testid Panel status error"]')).toHaveCount(0, { timeout: 45_000 });
});

test('carries no account-specific identifiers', async ({ readProvisionedDashboard }) => {
  const dashboard = await readProvisionedDashboard({ fileName: 'starter.json' });
  const json = JSON.stringify(dashboard);

  // Every id must come from a variable; a literal numeric id would mean the
  // dashboard only works on the account it was authored against.
  for (const field of ['agentId', 'deviceId', 'variableId']) {
    const values = [...json.matchAll(new RegExp(`"${field}":"([^"]*)"`, 'g'))].map((m) => m[1]);
    for (const v of values) {
      expect(v === '' || v.startsWith('$')).toBe(true);
    }
  }
});
