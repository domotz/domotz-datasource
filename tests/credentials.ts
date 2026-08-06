/**
 * Credentials for the E2E suite.
 *
 * Deliberately NOT read via `readProvisionedDataSource`: provisioning holds
 * `$DOMOTZ_API_KEY` so no real key is committed, and that helper parses the
 * YAML file rather than the values Grafana expanded at boot - so it hands back
 * the literal placeholder. Tests take the same environment variable the server
 * does instead.
 */
export const API_URL = process.env.DOMOTZ_API_URL ?? 'https://api-eu-west-1-cell-1.domotz.com/public-api/v1/';

export const API_KEY = process.env.DOMOTZ_TEST_API_KEY ?? process.env.DOMOTZ_API_KEY ?? '';

export const HAS_CREDENTIALS = API_KEY.length > 0;

export const SKIP_REASON = 'set DOMOTZ_API_KEY (or DOMOTZ_TEST_API_KEY) to run tests that talk to the Domotz API';
