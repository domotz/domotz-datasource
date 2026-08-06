import { test, expect } from '@grafana/plugin-e2e';

import { API_KEY, API_URL, HAS_CREDENTIALS, SKIP_REASON } from './credentials';

test('renders the config editor', async ({ createDataSourceConfigPage, readProvisionedDataSource, page }) => {
  const ds = await readProvisionedDataSource({ fileName: 'datasources.yml' });
  await createDataSourceConfigPage({ type: ds.type });

  await expect(page.getByRole('textbox', { name: 'API URL' })).toBeVisible();
  await expect(page.getByLabel('API key')).toBeVisible();
});

test('"Save & test" succeeds with a valid URL and key', async ({
  createDataSourceConfigPage,
  readProvisionedDataSource,
  page,
}) => {
  test.skip(!HAS_CREDENTIALS, SKIP_REASON);

  const ds = await readProvisionedDataSource({ fileName: 'datasources.yml' });
  const configPage = await createDataSourceConfigPage({ type: ds.type });

  await page.getByRole('textbox', { name: 'API URL' }).fill(API_URL);
  await page.getByLabel('API key').fill(API_KEY);

  await expect(configPage.saveAndTest()).toBeOK();
  await expect(configPage).toHaveAlert('success', { hasText: 'Connected to Domotz' });
});

test('"Save & test" reports a missing key rather than a generic failure', async ({
  createDataSourceConfigPage,
  readProvisionedDataSource,
  page,
}) => {
  const ds = await readProvisionedDataSource({ fileName: 'datasources.yml' });
  const configPage = await createDataSourceConfigPage({ type: ds.type });

  await page.getByRole('textbox', { name: 'API URL' }).fill(API_URL);

  await expect(configPage.saveAndTest()).not.toBeOK();
  await expect(configPage).toHaveAlert('error', { hasText: 'API key is missing' });
});

// A rejected key must stay distinguishable from an unreachable API: the old
// backend collapsed every upstream failure into one opaque message.
test('"Save & test" reports a rejected key distinctly', async ({
  createDataSourceConfigPage,
  readProvisionedDataSource,
  page,
}) => {
  const ds = await readProvisionedDataSource({ fileName: 'datasources.yml' });
  const configPage = await createDataSourceConfigPage({ type: ds.type });

  await page.getByRole('textbox', { name: 'API URL' }).fill(API_URL);
  await page.getByLabel('API key').fill('definitely-not-a-valid-key');

  await expect(configPage.saveAndTest()).not.toBeOK();
  await expect(configPage).toHaveAlert('error', { hasText: 'API key was rejected by Domotz' });
});
