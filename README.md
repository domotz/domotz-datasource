# Domotz data source for Grafana

Query and visualise [Domotz](https://www.domotz.com) network monitoring data in Grafana —
collector and device metrics, with full history.

Built on the [Domotz Public API](https://portal.domotz.com/api). Your API key is
stored encrypted on the Grafana server and never reaches the browser.

![The starter dashboard: internet quality across three sites, and per-port traffic compared across
switches](docs/dashboard.png)

<sub>The bundled starter dashboard. Every panel is driven by the variables at the top, so the same
dashboard works on any account without editing. Shown against demo data.</sub>

> [!IMPORTANT]
> **Beta.** This plugin is not yet in the Grafana plugin catalogue, so it is **unsigned** and
> Grafana refuses to load unsigned plugins by default. You must allow it explicitly — see
> [Installation](#installation). Once it is published, installation becomes a one-click job and
> this step disappears.

## What you get

- **Collector → Device → Metric** pickers, each list narrowed by the one above it.
- **Search by anything** — a device by name, IP address or MAC; a metric by name, unit or sensor
  path. Labels stay short; the details sit on a second line that is still searchable.
- **Template variables**, so one dashboard serves every site instead of being cloned per
  customer. Multi-value variables draw one series per value.
- **Correct units and labels** — series carry `collector`, `device`, `variable` and `unit`
  labels, and Domotz units map onto Grafana's, so a panel reads `12.4 MB/s`, not `12400000`.
- **Names that stay current** — display names are resolved at query time, so renaming a device
  in Domotz updates every legend that references it.
- **Alerting** on any history-backed metric.
- A portable [starter dashboard](provisioning/dashboards/starter.json) that works on any account.

## Requirements

- **Grafana 12.3 or later** (verified against 13.1.x). It will not load on older versions.
- A **Domotz Public API key** — see
  [how to create one](https://help.domotz.com/admin-global-features/domotz-api/).

## Installation

Download `domotz-datasource-<version>.zip` from the
[latest release](../../releases/latest).

### Try it in Docker

```bash
unzip domotz-datasource-*.zip -d ./plugins

docker run -d -p 3000:3000 \
  -v "$PWD/plugins/domotz-datasource:/var/lib/grafana/plugins/domotz-datasource" \
  -e GF_PLUGINS_ALLOW_LOADING_UNSIGNED_PLUGINS=domotz-datasource \
  grafana/grafana-enterprise:13.1.1
```

Open <http://localhost:3000> (admin / admin) and add the **Domotz** data source.

### Add it to an existing Grafana

1. Unzip into Grafana's plugin directory — `/var/lib/grafana/plugins` on Linux packages, or
   whatever `paths.plugins` is set to.
2. Allow the unsigned plugin, in `grafana.ini`:

   ```ini
   [plugins]
   allow_loading_unsigned_plugins = domotz-datasource
   ```

   or as an environment variable:

   ```bash
   GF_PLUGINS_ALLOW_LOADING_UNSIGNED_PLUGINS=domotz-datasource
   ```

3. Restart Grafana. Without step 2 the plugin is skipped at startup with
   `plugin ... has no signature` in the log, and never appears in the data source list.

The release archive contains binaries for linux (amd64, arm64, arm), macOS (amd64, arm64) and
Windows (amd64); Grafana picks the right one at runtime.

## Configuration

Add the data source, then set:

| Field | Value |
| --- | --- |
| **API URL** | The Public API endpoint for your cell, e.g. `https://api-eu-west-1-cell-1.domotz.com/public-api/v1/` |
| **API key** | Your Domotz Public API key |

Click **Save & test** — a healthy configuration reports `Connected to Domotz`.

## Building a query

Choose a **scope** — a metric belonging to a device, or one belonging to the collector itself —
then work down: **Collector → Device → Metric**.

Only metrics that record history are listed by default. Turn off **History only** to reach
metrics that just report a current value (a firmware version, a serial number); those are graphed
as a single point at the time of their last reading.

The **↻** button reloads the lists from Domotz. They are cached in the plugin backend for five
minutes, so a collector or device you have just onboarded needs a refresh to appear.

### Template variables

Create a dashboard variable of type **Query**, pick this data source, then choose what to list:

| Query type | Returns | Needs |
| --- | --- | --- |
| Collectors | Every collector on the account | — |
| Devices | Devices of one collector | a collector id, e.g. `$collector` |
| Device metrics | History-capable metrics of one device | a collector and a device id |
| Collector metrics | History-capable metrics of the collector | a collector id |

Chain them — a `Collectors` variable feeding a `Devices` variable — to build one dashboard that
works across every site. A single query is capped at **20 series**; above that it fails with a
message naming the count rather than queueing hundreds of requests against your API key.

> Alert rules are evaluated in the backend, which does not interpolate dashboard variables. This
> is a Grafana platform limitation, not specific to this plugin. Build alert rules on queries
> with concrete selections.

## Starter dashboard

Import [`provisioning/dashboards/starter.json`](provisioning/dashboards/starter.json) via
**Dashboards → New → Import**. It hardcodes nothing — every panel is driven by the template
variables at the top, so it works against any account without editing.

Pick a **Collector**, then select one or more **Collector metrics** and **Device metrics**; each
selection becomes its own panel, and selecting several devices overlays them on one panel. Save a
copy once you have the panels you want.

Metric identifiers are issued per collector, so a dashboard cannot pin a metric by name across
accounts — which is why the starter asks you to choose rather than shipping fixed panels.


## Development

```bash
npm install
npm run build                        # frontend
mage buildAll                        # backend, all platforms
mage build:generateManifestFile      # required for catalogue submission

DOMOTZ_API_KEY=<key> npm run server  # Grafana + the plugin in Docker
```

Note that `mage clean` removes `dist/`, which breaks the dev container's bind mount. After a
clean, use `docker compose down && docker compose up -d` rather than a restart.

### Tests

```bash
npm run test:ci    # frontend unit tests
go test ./pkg/...  # backend unit tests
npm run e2e        # Playwright, needs the dev server running

# against the real Domotz API (skipped without a key)
DOMOTZ_TEST_API_KEY=<key> go test ./pkg/... -run TestLive -p 1 -v
```

Use `-p 1` for the live tests. Each package builds its own client, each bounded at four
concurrent requests, so running packages in parallel puts eight in flight against an API that
allows five — which shows up as an occasional slow failure rather than an obvious 429.

Tests that need a live API key skip cleanly without one, so the suite is green on a fresh
checkout.

## Releases

Tagging `v*` runs [`.github/workflows/release.yml`](.github/workflows/release.yml), which builds
every platform and drafts a GitHub release. Signing is enabled by supplying a
`policy_token` secret once the plugin has a Grafana Cloud organisation to sign under.

## Licence

[Apache 2.0](LICENSE). See [CONTRIBUTORS](CONTRIBUTORS.md) for credit to everyone who built the
version this one replaces.
