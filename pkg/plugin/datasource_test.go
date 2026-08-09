package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/domotz/domotz-datasource/pkg/domotz"
	"github.com/domotz/domotz-datasource/pkg/models"
	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/stretchr/testify/require"
)

// stubAPI answers the handful of Domotz endpoints the data source calls,
// letting QueryData be exercised end to end without network access or an API
// key.
type stubAPI struct {
	*httptest.Server
	historyRequests atomic.Int64
	agentRequests   atomic.Int64
	// bulkVariableRequests counts the collector-wide device-variable sweep;
	// perDeviceVariableRequests counts the per-device listing it replaced.
	bulkVariableRequests      atomic.Int64
	perDeviceVariableRequests atomic.Int64

	historyBody string
	// emptyAccount answers /agent with [], as a key bound to no collectors does.
	emptyAccount bool
	failWith     int
	// withForbiddenCollector adds a second collector to the listing that every
	// subsequent call rejects with 403, as a shared collector the key was never
	// granted does.
	withForbiddenCollector bool
	// staleDeviceList omits device 11 from the device listing while the
	// variable listing still reports variables belonging to it, which is what
	// two independently-expiring caches look like just after a device is added.
	staleDeviceList bool
	// collector 8's own variables, served when withSecondCollector is set.
	withSecondCollector bool
}

func newStubAPI(t *testing.T) *stubAPI {
	t.Helper()
	s := &stubAPI{historyBody: `[]`}

	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.failWith != 0 {
			http.Error(w, `{"error":"nope"}`, s.failWith)
			return
		}

		path := r.URL.Path

		// Collector 8 is visible in the listing but unreadable, which is how
		// the API presents a shared collector this key was not granted.
		if s.withForbiddenCollector && strings.HasPrefix(path, "/agent/8/") {
			http.Error(w, `{"error":"Operation not authorized"}`, http.StatusForbidden)
			return
		}

		switch {
		case path == "/user":
			_, _ = w.Write([]byte(`{"id":1}`))

		case path == "/meta/usage":
			_, _ = w.Write([]byte(`{"daily_usage":10,"daily_limit":100}`))

		case path == "/agent":
			s.agentRequests.Add(1)
			if s.emptyAccount {
				respondList(w, r, 0, `[]`)
				return
			}
			if s.withSecondCollector {
				respondList(w, r, 2, `[
					{"id":7,"display_name":"Acme HQ"},
					{"id":8,"display_name":"Second Site"}
				]`)
				return
			}
			if s.withForbiddenCollector {
				respondList(w, r, 2, `[
					{"id":7,"display_name":"Acme HQ"},
					{"id":8,"display_name":"Shared Site"}
				]`)
				return
			}
			respondList(w, r, 1, `[{"id":7,"display_name":"Acme HQ","licence":{"expiration_time":null}}]`)

		case path == "/agent/7/device":
			if s.staleDeviceList {
				respondList(w, r, 1, `[{"id":12,"display_name":"Edge AP"}]`)
				return
			}
			respondList(w, r, 2, `[{"id":11,"display_name":"Core Switch"},{"id":12,"display_name":"Edge AP"}]`)

		case path == "/agent/7/device/11/variable":
			s.perDeviceVariableRequests.Add(1)
			respondList(w, r, 1, `[{"id":99,"label":"Bandwidth","unit":"%","has_history":true}]`)

		case path == "/agent/7/device/12/variable":
			s.perDeviceVariableRequests.Add(1)
			respondList(w, r, 1, `[{"id":100,"label":"Bandwidth","unit":"%","has_history":true}]`)

		// The collector-wide sweep the query path uses: one request covers
		// every device, and each entry carries the device it belongs to.
		case path == "/agent/7/device/variable":
			s.bulkVariableRequests.Add(1)
			// Both devices expose the same metric under the same sensor path
			// but different ids - the shape that makes an id-valued query
			// unable to describe more than one device.
			respondList(w, r, 2, `[
				{"id":99,"device_id":11,"label":"Bandwidth","unit":"%","path":"snmp/if/bandwidth","has_history":true},
				{"id":100,"device_id":12,"label":"Bandwidth","unit":"%","path":"snmp/if/bandwidth","has_history":true}
			]`)

		case path == "/agent/7/variable":
			respondList(w, r, 2, `[
				{"id":55,"label":"Collector Uptime","unit":"s","path":"perf/uptime","has_history":true},
				{"id":56,"label":"Requests","unit":"req/h","path":"perf/requests","has_history":true}
			]`)

		case path == "/agent/8/variable":
			s.agentRequests.Add(1)
			respondList(w, r, 2, `[
				{"id":58,"label":"Collector Uptime","unit":"s","path":"perf/uptime","has_history":true},
				{"id":59,"label":"Dock Humidity","unit":"%","path":"perf/humidity","has_history":true}
			]`)

		case strings.HasSuffix(path, "/history"):
			s.historyRequests.Add(1)
			_, _ = w.Write([]byte(s.historyBody))

		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func respondList(w http.ResponseWriter, r *http.Request, count int, body string) {
	if r.Method == http.MethodHead {
		w.Header().Set("X-Entities-Count", fmt.Sprint(count))
		w.WriteHeader(http.StatusNoContent)
		return
	}
	_, _ = w.Write([]byte(body))
}

func newTestDatasource(t *testing.T, api *stubAPI) *DomotzDatasource {
	t.Helper()
	settings := &models.PluginSettings{
		Path:    api.URL,
		Secrets: &models.SecretPluginSettings{ApiKey: "test-key"},
	}
	ds := &DomotzDatasource{
		settings: settings,
		client:   domotz.NewClient(api.URL, "test-key", api.Server.Client()),
	}
	ds.CallResourceHandler = newResourceHandler(ds)
	return ds
}

func queryRequest(t *testing.T, refID string, model QueryModel) *backend.QueryDataRequest {
	t.Helper()
	raw, err := json.Marshal(model)
	require.NoError(t, err)

	return &backend.QueryDataRequest{
		Queries: []backend.DataQuery{{
			RefID: refID,
			JSON:  raw,
			TimeRange: backend.TimeRange{
				From: time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC),
				To:   time.Date(2026, 8, 4, 23, 59, 0, 0, time.UTC),
			},
		}},
	}
}

func TestQueryData_DeviceVariableProducesNamedSeries(t *testing.T) {
	api := newStubAPI(t)
	api.historyBody = `[
		{"timestamp":"2026-08-04T10:00:00Z","value":"40.5"},
		{"timestamp":"2026-08-04T10:05:00Z","value":"55"}
	]`
	ds := newTestDatasource(t, api)

	resp, err := ds.QueryData(context.Background(),
		queryRequest(t, "A", QueryModel{Scope: ScopeDevice, AgentID: 7, DeviceID: 11, VariableID: 99}))
	require.NoError(t, err)

	res := resp.Responses["A"]
	require.NoError(t, res.Error)
	require.Len(t, res.Frames, 1)

	frame := res.Frames[0]
	rows, err := frame.RowLen()
	require.NoError(t, err)
	require.Equal(t, 2, rows)

	value := frame.Fields[1]
	require.Equal(t, 40.5, value.At(0))
	require.Equal(t, float64(55), value.At(1))

	// Metadata comes from the API at query time, not from the saved query.
	require.Equal(t, "Acme HQ", value.Labels["collector"])
	require.Equal(t, "Core Switch", value.Labels["device"])
	require.Equal(t, "Bandwidth", value.Labels["variable"])
	require.Equal(t, "percent", value.Config.Unit)
	require.Equal(t, "Acme HQ - Core Switch: Bandwidth", value.Config.DisplayNameFromDS)
}

func TestQueryData_CollectorScopeSkipsDeviceLookup(t *testing.T) {
	api := newStubAPI(t)
	api.historyBody = `[{"timestamp":"2026-08-04T10:00:00Z","value":"3600"}]`
	ds := newTestDatasource(t, api)

	resp, err := ds.QueryData(context.Background(),
		queryRequest(t, "A", QueryModel{Scope: ScopeCollector, AgentID: 7, VariableID: 55}))
	require.NoError(t, err)

	value := resp.Responses["A"].Frames[0].Fields[1]
	require.Equal(t, "Collector Uptime", value.Labels["variable"])
	_, hasDevice := value.Labels["device"]
	require.False(t, hasDevice)
	require.Equal(t, "Acme HQ: Collector Uptime", value.Config.DisplayNameFromDS)
}

func TestQueryData_IncompleteQueryIsRejectedWithBadRequest(t *testing.T) {
	ds := newTestDatasource(t, newStubAPI(t))

	resp, err := ds.QueryData(context.Background(),
		queryRequest(t, "A", QueryModel{Scope: ScopeDevice, AgentID: 7}))
	require.NoError(t, err)

	res := resp.Responses["A"]
	require.Error(t, res.Error)
	require.Equal(t, backend.StatusBadRequest, res.Status)
}

func TestQueryData_UpstreamAuthFailureKeepsItsStatus(t *testing.T) {
	api := newStubAPI(t)
	api.failWith = http.StatusUnauthorized
	ds := newTestDatasource(t, api)

	resp, err := ds.QueryData(context.Background(),
		queryRequest(t, "A", QueryModel{Scope: ScopeDevice, AgentID: 7, DeviceID: 11, VariableID: 99}))
	require.NoError(t, err)

	res := resp.Responses["A"]
	require.Error(t, res.Error)
	require.Equal(t, backend.StatusUnauthorized, res.Status,
		"a rejected key must not be reported as a malformed query")
}

func TestQueryData_RateLimitKeepsItsStatus(t *testing.T) {
	api := newStubAPI(t)
	api.failWith = http.StatusTooManyRequests
	ds := newTestDatasource(t, api)

	resp, err := ds.QueryData(context.Background(),
		queryRequest(t, "A", QueryModel{Scope: ScopeDevice, AgentID: 7, DeviceID: 11, VariableID: 99}))
	require.NoError(t, err)

	require.Equal(t, backend.StatusTooManyRequests, resp.Responses["A"].Status)
}

// A variable retired upstream must degrade to an empty series, not break the
// whole dashboard.
func TestQueryData_UnknownVariableRendersEmptySeries(t *testing.T) {
	ds := newTestDatasource(t, newStubAPI(t))

	resp, err := ds.QueryData(context.Background(),
		queryRequest(t, "A", QueryModel{Scope: ScopeDevice, AgentID: 7, DeviceID: 11, VariableID: 12345}))
	require.NoError(t, err)

	res := resp.Responses["A"]
	require.NoError(t, res.Error)
	rows, err := res.Frames[0].RowLen()
	require.NoError(t, err)
	require.Equal(t, 0, rows)
}

func TestQueryData_RunsMultipleQueriesAndKeepsRefIDs(t *testing.T) {
	api := newStubAPI(t)
	api.historyBody = `[{"timestamp":"2026-08-04T10:00:00Z","value":"1"}]`
	ds := newTestDatasource(t, api)

	req := queryRequest(t, "A", QueryModel{Scope: ScopeDevice, AgentID: 7, DeviceID: 11, VariableID: 99})
	second := req.Queries[0]
	second.RefID = "B"
	req.Queries = append(req.Queries, second)

	resp, err := ds.QueryData(context.Background(), req)
	require.NoError(t, err)

	require.Len(t, resp.Responses, 2)
	require.Equal(t, "A", resp.Responses["A"].Frames[0].RefID)
	require.Equal(t, "B", resp.Responses["B"].Frames[0].RefID)
	require.Equal(t, int64(2), api.historyRequests.Load(), "history is per-query and uncached")
}

// rawQueryRequest carries query JSON the typed QueryModel cannot express, which
// is how a multi-value variable arrives once interpolated.
func rawQueryRequest(t *testing.T, refID, body string) *backend.QueryDataRequest {
	t.Helper()

	return &backend.QueryDataRequest{
		Queries: []backend.DataQuery{{
			RefID: refID,
			JSON:  []byte(body),
			TimeRange: backend.TimeRange{
				From: time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC),
				To:   time.Date(2026, 8, 4, 23, 59, 0, 0, time.UTC),
			},
		}},
	}
}

// A multi-value $device is the shape panel Repeat cannot express: several
// devices compared as overlaid series on one panel.
func TestQueryData_MultiValueDeviceYieldsOneFramePerValue(t *testing.T) {
	api := newStubAPI(t)
	api.historyBody = `[{"timestamp":"2026-08-04T10:00:00Z","value":"1"}]`
	ds := newTestDatasource(t, api)

	resp, err := ds.QueryData(context.Background(), rawQueryRequest(t, "A",
		`{"scope":"device","agentId":"7","deviceId":"{11,12}","variableId":"{99,100}"}`))
	require.NoError(t, err)

	res := resp.Responses["A"]
	require.NoError(t, res.Error)

	// The product is four combinations, but variable 99 belongs to device 11 and
	// 100 to device 12, so only the two real pairs produce series.
	require.Len(t, res.Frames, 2)
	for _, frame := range res.Frames {
		require.Equal(t, "A", frame.RefID, "every frame shares the query's RefID")
	}

	devices := []string{
		res.Frames[0].Fields[1].Labels["device"],
		res.Frames[1].Fields[1].Labels["device"],
	}
	require.ElementsMatch(t, []string{"Core Switch", "Edge AP"}, devices)
}

func TestQueryData_MultiValueUpstreamFailureStillFailsTheQuery(t *testing.T) {
	api := newStubAPI(t)
	api.failWith = http.StatusTooManyRequests
	ds := newTestDatasource(t, api)

	resp, err := ds.QueryData(context.Background(), rawQueryRequest(t, "A",
		`{"scope":"device","agentId":"7","deviceId":"{11,12}","variableId":"99"}`))
	require.NoError(t, err)

	require.Equal(t, backend.StatusTooManyRequests, resp.Responses["A"].Status,
		"a throttled fan-out must report the throttling, not quietly return fewer series")
}

// Grafana reads a missing RefID as "no data", so a cancelled refresh has to
// report the cancellation rather than leave panels looking empty and healthy.
func TestQueryData_CancelledContextStillAnswersEveryRefID(t *testing.T) {
	ds := newTestDatasource(t, newStubAPI(t))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	req := queryRequest(t, "A", QueryModel{Scope: ScopeDevice, AgentID: 7, DeviceID: 11, VariableID: 99})
	second := req.Queries[0]
	second.RefID = "B"
	req.Queries = append(req.Queries, second)

	resp, err := ds.QueryData(ctx, req)
	require.NoError(t, err)

	for _, refID := range []string{"A", "B"} {
		res, ok := resp.Responses[refID]
		require.True(t, ok, "refId %s must be present in the response", refID)
		require.Error(t, res.Error)
		require.Equal(t, backend.StatusTimeout, res.Status,
			"a cancelled refresh is a timeout, not an internal fault")
	}
}

// One collector-wide sweep covers every device, so a per-device panel costs one
// metadata lookup instead of one per device.
func TestQueryData_DeviceMetadataUsesOneCollectorWideSweep(t *testing.T) {
	api := newStubAPI(t)
	api.historyBody = `[{"timestamp":"2026-08-04T10:00:00Z","value":"1"}]`
	ds := newTestDatasource(t, api)

	resp, err := ds.QueryData(context.Background(), rawQueryRequest(t, "A",
		`{"scope":"device","agentId":"7","deviceId":"{11,12}","variableId":"{99,100}"}`))
	require.NoError(t, err)
	require.NoError(t, resp.Responses["A"].Error)

	require.Equal(t, int64(1), api.bulkVariableRequests.Load(),
		"the collector-wide listing should be fetched once and shared")
	require.Equal(t, int64(0), api.perDeviceVariableRequests.Load(),
		"the query path should no longer issue a request per device")
}

func TestCheckHealth_ReportsSuccess(t *testing.T) {
	ds := newTestDatasource(t, newStubAPI(t))

	res, err := ds.CheckHealth(context.Background(), &backend.CheckHealthRequest{})

	require.NoError(t, err)
	require.Equal(t, backend.HealthStatusOk, res.Status)
	require.Equal(t, "Connected to Domotz", res.Message)
}

func TestCheckHealth_ExplainsRejectedKey(t *testing.T) {
	api := newStubAPI(t)
	api.failWith = http.StatusUnauthorized
	ds := newTestDatasource(t, api)

	res, err := ds.CheckHealth(context.Background(), &backend.CheckHealthRequest{})

	require.NoError(t, err)
	require.Equal(t, backend.HealthStatusError, res.Status)
	require.Equal(t, "API key was rejected by Domotz", res.Message)
}

func TestCheckHealth_ExplainsQuotaExhaustion(t *testing.T) {
	api := newStubAPI(t)
	api.failWith = http.StatusTooManyRequests
	ds := newTestDatasource(t, api)

	res, err := ds.CheckHealth(context.Background(), &backend.CheckHealthRequest{})

	require.NoError(t, err)
	require.Contains(t, res.Message, "quota exceeded")
}

func TestCheckHealth_RequiresAPIKey(t *testing.T) {
	api := newStubAPI(t)
	ds := newTestDatasource(t, api)
	ds.settings.Secrets.ApiKey = ""

	res, err := ds.CheckHealth(context.Background(), &backend.CheckHealthRequest{})

	require.NoError(t, err)
	require.Equal(t, "API key is missing", res.Message)
}

// --- resource routes ---------------------------------------------------

func callResource(t *testing.T, ds *DomotzDatasource, path string) (int, []byte) {
	t.Helper()
	return callResourceMethod(t, ds, http.MethodGet, path)
}

func callResourceMethod(t *testing.T, ds *DomotzDatasource, method, path string) (int, []byte) {
	t.Helper()

	var (
		status int
		body   []byte
	)
	sender := backend.CallResourceResponseSenderFunc(func(res *backend.CallResourceResponse) error {
		status = res.Status
		body = res.Body
		return nil
	})

	// Grafana sends Path without the query string and URL with it, and
	// httpadapter rebuilds the request as Path + "?" + URL's query. Passing the
	// query in both fields appends it twice, which silently corrupts the first
	// parameter's value rather than failing - so split them the way the real
	// caller does.
	reqPath, query, _ := strings.Cut(path, "?")
	reqURL := reqPath
	if query != "" {
		reqURL += "?" + query
	}

	err := ds.CallResource(context.Background(), &backend.CallResourceRequest{
		Method: method,
		Path:   reqPath,
		URL:    reqURL,
	}, sender)
	require.NoError(t, err)
	return status, body
}

// An account with no collectors must serialise as [], not null: the query
// editor calls list.map on the result and a null crashes the picker.
func TestResource_EmptyAccountReturnsAnEmptyArrayNotNull(t *testing.T) {
	api := newStubAPI(t)
	api.emptyAccount = true
	ds := newTestDatasource(t, api)

	status, body := callResource(t, ds, "collectors")

	require.Equal(t, http.StatusOK, status)
	require.JSONEq(t, `[]`, string(body))
	require.NotEqual(t, "null", strings.TrimSpace(string(body)))
}

// Without a reachable invalidation a collector added in the Domotz portal stays
// invisible for the whole TTL, which reads as the editor being broken.
func TestResource_InvalidateCacheForcesAFreshLookup(t *testing.T) {
	api := newStubAPI(t)
	ds := newTestDatasource(t, api)

	_, _ = callResource(t, ds, "collectors")
	_, _ = callResource(t, ds, "collectors")
	require.Equal(t, int64(1), api.agentRequests.Load(), "the second read should be cached")

	status, _ := callResourceMethod(t, ds, http.MethodPost, "cache/invalidate")
	require.Equal(t, http.StatusNoContent, status)

	_, _ = callResource(t, ds, "collectors")
	require.Equal(t, int64(2), api.agentRequests.Load(),
		"a refresh must reach the API again")
}

func TestResource_ListsCollectors(t *testing.T) {
	ds := newTestDatasource(t, newStubAPI(t))

	status, body := callResource(t, ds, "collectors")

	require.Equal(t, http.StatusOK, status)
	var agents []domotz.Agent
	require.NoError(t, json.Unmarshal(body, &agents))
	require.Len(t, agents, 1)
	require.Equal(t, "Acme HQ", agents[0].DisplayName)
}

func TestResource_ListsDeviceVariablesWithResolvedLabel(t *testing.T) {
	ds := newTestDatasource(t, newStubAPI(t))

	status, body := callResource(t, ds, "collectors/7/devices/11/variables")

	require.Equal(t, http.StatusOK, status)
	var options []variableOption
	require.NoError(t, json.Unmarshal(body, &options))
	require.Len(t, options, 1)
	require.Equal(t, "Bandwidth", options[0].DisplayLabel,
		"the picker label is computed in one place, shared with the panel legend")
}

func TestResource_RejectsNonNumericIDs(t *testing.T) {
	ds := newTestDatasource(t, newStubAPI(t))

	status, body := callResource(t, ds, "collectors/not-a-number/devices")

	require.Equal(t, http.StatusBadRequest, status)
	require.Contains(t, string(body), "invalid collectorId")
}

func TestResource_UnknownRouteIsNotFound(t *testing.T) {
	ds := newTestDatasource(t, newStubAPI(t))

	status, _ := callResource(t, ds, "nonsense")

	require.Equal(t, http.StatusNotFound, status)
}

func TestResource_ForwardsUpstreamStatus(t *testing.T) {
	api := newStubAPI(t)
	api.failWith = http.StatusTooManyRequests
	ds := newTestDatasource(t, api)

	status, _ := callResource(t, ds, "collectors")

	require.Equal(t, http.StatusTooManyRequests, status)
}

func TestResource_ReportsUsage(t *testing.T) {
	ds := newTestDatasource(t, newStubAPI(t))

	status, body := callResource(t, ds, "usage")

	require.Equal(t, http.StatusOK, status)
	var usage domotz.Usage
	require.NoError(t, json.Unmarshal(body, &usage))
	require.Equal(t, int64(100), usage.DailyLimit)
}

// The point of addressing a metric by path: one query, one series per device,
// each resolved to that device's own variable id.
func TestQueryData_SensorPathResolvesPerDevice(t *testing.T) {
	api := newStubAPI(t)
	api.historyBody = `[{"timestamp":"2026-08-04T10:00:00Z","value":"7"}]`
	ds := newTestDatasource(t, api)

	resp, err := ds.QueryData(context.Background(), rawQueryRequest(t, "A",
		`{"scope":"device","agentId":7,"deviceId":"{11,12}","variableId":"snmp/if/bandwidth"}`))
	require.NoError(t, err)

	res := resp.Responses["A"]
	require.NoError(t, res.Error)
	require.Len(t, res.Frames, 2, "one series per selected device")
	require.EqualValues(t, 2, api.historyRequests.Load(),
		"each device's own variable id is fetched, not one id twice")

	// Both series carry the same metric name; the device is what tells them
	// apart, and it has to reach the legend.
	devices := []string{
		res.Frames[0].Fields[1].Labels["device"],
		res.Frames[1].Fields[1].Labels["device"],
	}
	require.ElementsMatch(t, []string{"Core Switch", "Edge AP"}, devices)
	require.ElementsMatch(t,
		[]string{"Acme HQ - Core Switch: Bandwidth", "Acme HQ - Edge AP: Bandwidth"},
		[]string{res.Frames[0].Fields[1].Config.DisplayNameFromDS, res.Frames[1].Fields[1].Config.DisplayNameFromDS})
}

// A shared collector the key cannot read is a fact about this caller, not a
// failure: it must drop out of the fan-out rather than blanking a panel that is
// charting the collectors the key *can* read.
func TestQueryData_ForbiddenCollectorIsSkippedNotFatal(t *testing.T) {
	api := newStubAPI(t)
	api.withForbiddenCollector = true
	api.historyBody = `[{"timestamp":"2026-08-04T10:00:00Z","value":"7"}]`
	ds := newTestDatasource(t, api)

	resp, err := ds.QueryData(context.Background(), rawQueryRequest(t, "A",
		`{"scope":"device","agentId":"{7,8}","deviceId":"{11,12}","variableId":"snmp/if/bandwidth"}`))
	require.NoError(t, err)

	res := resp.Responses["A"]
	require.NoError(t, res.Error, "one unreadable collector must not fail the query")
	require.Len(t, res.Frames, 2, "only the readable collector's devices are drawn")
}

// 401 is the key itself being wrong. Drawing nothing quietly would hide it, so
// unlike 403 it still fails the query.
func TestQueryData_UnauthorizedRemainsFatalInAFanOut(t *testing.T) {
	api := newStubAPI(t)
	api.failWith = http.StatusUnauthorized
	ds := newTestDatasource(t, api)

	resp, err := ds.QueryData(context.Background(), rawQueryRequest(t, "A",
		`{"scope":"device","agentId":7,"deviceId":"{11,12}","variableId":"snmp/if/bandwidth"}`))
	require.NoError(t, err)

	require.Error(t, resp.Responses["A"].Error)
}

// A panel pinned to a sensor that has since been removed must read as an empty
// series, not an error. The API answers history for an unknown variable id with
// 403 on the device route and 503 on the collector one, so the id cannot simply
// be sent and the failure absorbed - the query has to stop before asking.
func TestQueryData_RetiredVariableAddressedByIdRendersEmptyWithoutFetching(t *testing.T) {
	api := newStubAPI(t)
	ds := newTestDatasource(t, api)

	resp, err := ds.QueryData(context.Background(),
		queryRequest(t, "A", QueryModel{Scope: ScopeDevice, AgentID: 7, DeviceID: 11, VariableID: 424242}))
	require.NoError(t, err)

	res := resp.Responses["A"]
	require.NoError(t, res.Error, "a retired sensor must not break the panel")
	require.Len(t, res.Frames, 1, "the series is still named, just empty")
	require.Zero(t, api.historyRequests.Load(), "history must not be requested for an id that resolved to nothing")
}

// The device listing and the device-variable listing are cached under separate
// keys with separate expiry, so just after a device is added one can refresh
// before the other. The variable listing naming the device is itself proof it
// exists; trusting the stale device listing dropped the new device's series
// from a fan-out with no error at all.
func TestQueryData_NewDeviceSurvivesAStaleDeviceListing(t *testing.T) {
	api := newStubAPI(t)
	api.staleDeviceList = true
	api.historyBody = `[{"timestamp":"2026-08-04T10:00:00Z","value":"5"}]`
	ds := newTestDatasource(t, api)

	resp, err := ds.QueryData(context.Background(), rawQueryRequest(t, "A",
		`{"scope":"device","agentId":7,"deviceId":"{11,12}","variableId":"snmp/if/bandwidth"}`))
	require.NoError(t, err)

	res := resp.Responses["A"]
	require.NoError(t, res.Error)
	require.Len(t, res.Frames, 2, "the device missing from the stale listing still has a series")
}

// A unit Grafana maps becomes structured FieldConfig.Unit and formats the axis.
// One it does not map used to vanish entirely, leaving a bare number whose
// meaning depended on which similarly named metric you were reading.
func TestQueryData_UnmappedUnitIsKeptInTheSeriesName(t *testing.T) {
	api := newStubAPI(t)
	api.historyBody = `[{"timestamp":"2026-08-04T10:00:00Z","value":"12"}]`
	ds := newTestDatasource(t, api)

	resp, err := ds.QueryData(context.Background(),
		queryRequest(t, "A", QueryModel{Scope: ScopeCollector, AgentID: 7, VariableID: 56}))
	require.NoError(t, err)

	value := resp.Responses["A"].Frames[0].Fields[1]
	require.Equal(t, "Acme HQ: Requests [req/h]", value.Config.DisplayNameFromDS)
	require.Empty(t, value.Config.Unit, "an unmapped unit must not be guessed into a structured one")
}

// A mapped unit is already shown by the axis, so repeating it in the name would
// read "Bandwidth [%]" beside an axis labelled %.
func TestQueryData_MappedUnitIsNotRepeatedInTheSeriesName(t *testing.T) {
	api := newStubAPI(t)
	api.historyBody = `[{"timestamp":"2026-08-04T10:00:00Z","value":"12"}]`
	ds := newTestDatasource(t, api)

	resp, err := ds.QueryData(context.Background(),
		queryRequest(t, "A", QueryModel{Scope: ScopeDevice, AgentID: 7, DeviceID: 11, VariableID: 99}))
	require.NoError(t, err)

	value := resp.Responses["A"].Frames[0].Fields[1]
	require.Equal(t, "Acme HQ - Core Switch: Bandwidth", value.Config.DisplayNameFromDS)
	require.Equal(t, "percent", value.Config.Unit)
}

// recovered is what keeps a panic on a spawned goroutine from taking the plugin
// process - and every dashboard using this data source - down with it. The SDK's
// own recovery interceptor only guards the goroutine serving the RPC.
func TestRecovered(t *testing.T) {
	res, panicked := recovered(nil, "nothing")
	require.False(t, panicked)
	require.Nil(t, res.Error)

	res, panicked = recovered("boom", "query A")
	require.True(t, panicked)
	require.Error(t, res.Error)
	require.Equal(t, backend.StatusInternal, res.Status)
	require.Contains(t, res.Error.Error(), "query A")
}

// The shared-collector route had no test at all, which is how a sequential loop
// under a docstring promising a fan-out went unnoticed.
func TestResource_SharedCollectorVariablesDeduplicateByPath(t *testing.T) {
	api := newStubAPI(t)
	api.withSecondCollector = true
	ds := newTestDatasource(t, api)

	status, body := callResourceMethod(t, ds, http.MethodGet,
		"/shared-collector-variables?hasHistory=false&collectors=7,8")
	require.Equal(t, http.StatusOK, status)

	var got []struct {
		Path         string `json:"path"`
		DisplayLabel string `json:"displayLabel"`
	}
	require.NoError(t, json.Unmarshal(body, &got))

	paths := make([]string, len(got))
	for i, v := range got {
		paths[i] = v.Path
	}
	// perf/uptime is on both collectors under different ids and must appear
	// once; the order follows the order the caller asked for, so the dropdown
	// does not reshuffle between refreshes.
	require.Equal(t, []string{"perf/uptime", "perf/requests", "perf/humidity"}, paths)
}

func TestResource_SharedCollectorVariablesRejectsNonNumericIds(t *testing.T) {
	ds := newTestDatasource(t, newStubAPI(t))

	status, _ := callResourceMethod(t, ds, http.MethodGet, "/shared-collector-variables?collectors=7,nope")
	require.Equal(t, http.StatusBadRequest, status)
}
