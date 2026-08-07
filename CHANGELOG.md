# Changelog

## Unreleased

- Metrics can be compared across several devices, and across several collectors. Selecting more than
  one device used to empty the metric list and turn two panels red: a metric carries a different
  identifier on every device that exposes it, so no single identifier could describe them all.
  Metrics are now addressed by sensor path, which is what they share, and the backend resolves it to
  the right identifier per device.
- The collector variable accepts several sites, and collector metrics work the same way.
- Dashboard variable dropdowns show what the query editor shows: a device reads
  `Name · IP · MAC` and a collector `Name · STATUS`, in both places, and both are searchable on
  every part.
- A collector the API key cannot read is skipped instead of blanking the panel. Shared collectors
  appear in the account listing but are not always readable, and one of them used to fail every
  series in the query.
- The series limit counts series that are actually drawn rather than combinations described. Four
  collectors and three devices describe twelve pairs but draw three, and the old count rejected
  ordinary selections.
- A panel whose variable resolves to nothing reads as no data instead of `a variable must be
  selected` in red.

## 1.0.0-beta.3

No functional change. This release exists to prove the automated build: the release workflow was
failing, so beta.1 and beta.2 were packaged by hand.

- The release and CI workflows now take their Go version from `go.mod`. They pinned older versions,
  and `setup-go` runs with `GOTOOLCHAIN=local`, so the build refused to start rather than fetching
  the toolchain the Grafana plugin SDK requires.
- Development dependencies carrying known vulnerabilities are updated. The plugin validator fails a
  release on those, and it is a precondition for submitting to the Grafana catalogue.
- The licence carries a real copyright line instead of the Apache template placeholder.

## 1.0.0-beta.2

- The sample dashboard is replaced by a portable starter that hardcodes nothing: every panel is
  driven by template variables, so it works on any account without editing.
- Variable dropdowns list only metrics that record history, and disambiguate metrics that share a
  label - one account listed `443` five times with no way to tell them apart.
- A panel whose variable resolves to nothing now reads as no data rather than erroring.
- Corrected the Domotz API portal link.

## 1.0.0-beta.1

First public beta. Distributed via GitHub Releases and **unsigned** — Grafana must be told to
allow it (see the README). Not yet in the Grafana plugin catalogue.

Initial release.

Rewritten from the 2025 prototype, which was never published. Notable differences for anyone
looking at that code:

### Query model

- Queries persist identifiers only (`scope`, `agentId`, `deviceId`, `variableId`). Display names,
  units and current values are resolved from the API at query time instead of being written into
  the dashboard JSON, so a renamed device no longer leaves stale legends behind and a metric
  without history no longer replays the value it had when the panel was authored.
- Identifiers are stored as strings so a field can hold a dashboard variable.

### Template variables

- Added `CustomVariableSupport` with four query types: collectors, devices, device metrics and
  collector metrics. Chaining them makes one dashboard work across every site.
- `applyTemplateVariables` and `filterQuery` are implemented, replacing the
  `fromDashboard: "FROM_DASHBOARD"` sentinel that had been threaded through the query model to
  suppress queries from half-filled editors.

### Series output

- Series carry `collector`, `device`, `variable` and `unit` labels, so panel overrides can target
  them individually.
- Domotz units map onto Grafana units. The mapping is case sensitive: `B/s` (bytes per second)
  and `b/s` (bits per second) are distinct and both occur in practice.
- Field type is decided for a series as a whole. Deciding per sample could yield a value column
  shorter than its time column, which Grafana rejects.
- Samples are sorted and deduplicated by timestamp.

### Backend

- Uses the plugin SDK's HTTP client, so data source proxy, TLS and timeout settings apply and
  requests are traced. The previous version used bare `http.Client` values with no timeout.
- Request contexts are propagated, so an abandoned dashboard refresh stops consuming API quota.
- Collector, device and metric lookups are cached for five minutes, and metric lists are filtered
  upstream with `has_history`.
- The device list endpoint is fetched unpaginated: it rejects `HEAD` and ignores `page_size` and
  `page_number`. Paginated endpoints are read until a short page rather than probed with `HEAD`
  first, saving a request per lookup.
- Upstream HTTP status codes are preserved, so a rejected key and an exhausted quota are
  reported distinctly instead of collapsing into one opaque error.

### Frontend

- Query editor rebuilt on `Combobox`; `Select` is deprecated as of Grafana 12.
- Removed `recharts`, which existed to draw a two-number pie chart. The bundle went from 371 KB
  to 16 KB.
- `grafanaDependency` raised to `>=12.3.0`. Verified against Grafana 13.1.1.

### Naming

- Plugin id changed from `domotz-grafanadomotz-datasource` to `domotz-datasource` and the
  display name from "Grafana-Domotz" to "Domotz". Safe to change because nothing was ever
  published; both are fixed permanently once it reaches the catalog.
