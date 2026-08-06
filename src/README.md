# Domotz data source for Grafana

Query and visualise [Domotz](https://www.domotz.com) network monitoring data in Grafana:
collector and device metrics, with full history.

## Requirements

- Grafana 12.3 or later
- A Domotz Public API key ([how to create one](https://help.domotz.com/admin-global-features/domotz-api/))

## Configuration

1. Add the data source, then set:
   - **API URL** — the Public API endpoint for your cell, e.g.
     `https://api-eu-west-1-cell-1.domotz.com/public-api/v1/`
   - **API key** — stored encrypted on the Grafana server and never exposed to the browser.
2. Click **Save & test**. A successful check reports `Connected to Domotz`.

## Building a query

Each query selects one metric:

- **Scope** — a metric belonging to a device, or one belonging to the collector itself.
- **Collector** → **Device** → **Metric**, each list filtered by the level above it.

Each entry shows a short name on the first line and its details on the second — a device's IP
address and MAC, a metric's current reading and sensor path. **Both lines are searchable**, so
you can find a device by name, IP or MAC, and a metric by name, unit or path.

Where two metrics on the same device resolve to the same name, the end of the sensor path is
added to tell them apart.

Series carry `collector`, `device`, `variable` and `unit` labels, so panel overrides can target
them with `${__field.labels.device}`.

**History only** limits the metric list to metrics that record history, which is what you
normally want. Turn it off to reach metrics that only report a current value (a firmware version,
a serial number, a sensor with no series) — those are graphed as a single point, at the time of
their last reading.

Display names and units are resolved from the API each time a query runs, not stored in the
dashboard. Renaming a device updates every legend that references it.

The **↻** button next to the Collector field reloads the lists from Domotz. They are cached in
the plugin backend for five minutes, so a collector or device you have just onboarded needs a
refresh to appear.

## Template variables

Create a dashboard variable of type **Query**, pick this data source, then choose what to list:

| Query type | Returns | Needs |
| --- | --- | --- |
| Collectors | Every collector on the account | — |
| Devices | Devices of one collector | a collector id, e.g. `$collector` |
| Device metrics | History-capable metrics of one device | a collector and a device id |
| Collector metrics | History-capable metrics of the collector | a collector id |

Chain them to build one dashboard that works across every site: a `Collectors` variable feeding
a `Devices` variable, with repeated panels.

Each variable's value is the entity id and its label is the display name, so `$collector`
interpolates correctly into a query while still reading well in the picker.

### Multi-value variables

A multi-value variable draws **one series per value on the same panel**, which is the way to
compare a metric across several devices side by side. Panel **Repeat** is the better fit for the
site dimension, where you want one panel per collector rather than fifty overlaid series.

A single query is capped at **20 series**. Above that it fails with a message naming the count,
rather than queueing hundreds of requests against the API key's concurrency budget.

**Include All** works only with a custom *All value* set on the variable. Left empty, Grafana
passes its internal `$__all` placeholder through and the query reports that instead of guessing.

### Alerting

Alert rules are evaluated in the backend, which does not interpolate dashboard variables — this
is a Grafana platform limitation, not specific to this data source. Build alert rules on queries
with concrete collector, device and metric selections; a rule built on `$collector` fails every
evaluation with `expected a numeric id`.


## Development

```bash
npm install
npm run build                        # frontend
GOOS=linux GOARCH=arm64 go build -o dist/gpx_domotz_datasource_linux_arm64 ./pkg
DOMOTZ_API_KEY=<key> npm run server  # Grafana + the plugin in Docker

npm run test:ci                      # frontend unit tests
go test ./pkg/...                    # backend unit tests
npm run e2e                          # Playwright, needs the server running

# against the real Domotz API (skipped without a key)
DOMOTZ_TEST_API_KEY=<key> go test ./pkg/... -run TestLive -v
```
