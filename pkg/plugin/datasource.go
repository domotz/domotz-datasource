package plugin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/domotz/domotz-datasource/pkg/domotz"
	"github.com/domotz/domotz-datasource/pkg/models"
	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/backend/httpclient"
	"github.com/grafana/grafana-plugin-sdk-go/backend/instancemgmt"
	"github.com/grafana/grafana-plugin-sdk-go/backend/log"
	"github.com/grafana/grafana-plugin-sdk-go/data"
)

var (
	_ backend.QueryDataHandler      = (*DomotzDatasource)(nil)
	_ backend.CheckHealthHandler    = (*DomotzDatasource)(nil)
	_ instancemgmt.InstanceDisposer = (*DomotzDatasource)(nil)
)

// DomotzDatasource serves panel queries and editor lookups for one configured
// Domotz API key.
type DomotzDatasource struct {
	backend.CallResourceHandler
	settings *models.PluginSettings
	client   *domotz.Client
}

// NewDatasource creates a data source instance. The SDK recreates the instance
// whenever settings change, so the client and its cache are always tied to the
// credentials they were built with.
func NewDatasource(_ context.Context, dis backend.DataSourceInstanceSettings) (instancemgmt.Instance, error) {
	settings, err := models.LoadPluginSettings(dis)
	if err != nil {
		return nil, err
	}

	// Use the SDK's HTTP client so proxy, TLS and timeout options configured
	// on the data source are honoured and requests are traced. The previous
	// implementation built bare &http.Client{} values with no timeout, so a
	// hung Domotz API pinned plugin goroutines indefinitely.
	opts, err := dis.HTTPClientOptions(context.Background())
	if err != nil {
		return nil, fmt.Errorf("building http client options: %w", err)
	}
	httpClient, err := httpclient.New(opts)
	if err != nil {
		return nil, fmt.Errorf("building http client: %w", err)
	}

	ds := &DomotzDatasource{
		settings: settings,
		client:   domotz.NewClient(settings.Path, settings.Secrets.ApiKey, httpClient),
	}
	ds.CallResourceHandler = newResourceHandler(ds)
	return ds, nil
}

// Dispose drops cached metadata when the instance is replaced.
func (d *DomotzDatasource) Dispose() {
	if d.client != nil {
		d.client.InvalidateCache()
	}
}

// InvalidateCache drops cached metadata on demand, backing the query editor's
// refresh control. Without it a collector or device added in the Domotz portal
// stays invisible in the pickers until the TTL expires, which reads as the
// editor being broken rather than briefly stale.
func (d *DomotzDatasource) InvalidateCache() {
	if d.client != nil {
		d.client.InvalidateCache()
	}
}

// QueryData runs every query in the request.
//
// Every RefID in the request gets an entry in the response, including when the
// context is cancelled: Grafana reads a missing RefID as "no data", so a
// cancelled refresh used to leave panels looking empty and healthy rather than
// reporting the timeout that caused it.
//
// Concurrency is bounded inside the Domotz client rather than here. The limit is
// per API key (GET /meta/usage reports concurrent_allowed: 5), so it has to
// cover metadata lookups, a multi-series fan-out and any other dashboard
// refreshing at the same time - none of which a bound scoped to one request can
// see.
func (d *DomotzDatasource) QueryData(ctx context.Context, req *backend.QueryDataRequest) (*backend.QueryDataResponse, error) {
	response := backend.NewQueryDataResponse()

	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)

	for _, q := range req.Queries {
		wg.Add(1)
		go func(q backend.DataQuery) {
			defer wg.Done()

			res := d.query(ctx, q)

			mu.Lock()
			response.Responses[q.RefID] = res
			mu.Unlock()
		}(q)
	}
	wg.Wait()

	return response, nil
}

// query executes one panel query, which may resolve to several series.
func (d *DomotzDatasource) query(ctx context.Context, q backend.DataQuery) backend.DataResponse {
	models, err := ExpandQueryModel(q.JSON)
	if err != nil {
		return backend.ErrDataResponse(backend.StatusBadRequest, err.Error())
	}

	timeRange := TimeRange{From: q.TimeRange.From.UTC(), To: q.TimeRange.To.UTC()}

	if len(models) == 1 {
		return d.singleSeries(ctx, q.RefID, models[0], timeRange)
	}
	return d.multiSeries(ctx, q.RefID, models, timeRange)
}

// singleSeries runs an ordinary one-series query, where any failure is the
// panel's answer and a retired variable still yields an empty named series.
func (d *DomotzDatasource) singleSeries(ctx context.Context, refID string, qm QueryModel, timeRange TimeRange) backend.DataResponse {
	sc, _, err := d.resolveSeriesContext(ctx, qm)
	if err != nil {
		return errorResponse(err, "resolving query metadata")
	}

	// A path that matched nothing resolves to no id at all. There is no history
	// to ask for, and asking anyway would address variable 0; render the same
	// empty named series a retired sensor gets.
	if sc.Variable.ID <= 0 {
		var response backend.DataResponse
		response.Frames = append(response.Frames, BuildFrame(refID, nil, sc, timeRange))
		return response
	}

	samples, err := d.client.VariableHistory(ctx, qm.AgentID.Int64(), qm.effectiveDeviceID(), sc.Variable.ID, timeRange.From, timeRange.To)
	if err != nil {
		return errorResponse(err, "fetching variable history")
	}

	var response backend.DataResponse
	response.Frames = append(response.Frames, BuildFrame(refID, samples, sc, timeRange))
	return response
}

// multiSeries runs a query that a multi-value variable expanded into several
// series, emitting one frame per series under the same RefID.
//
// Combinations that do not exist upstream are skipped rather than reported. When
// two multi-value variables are combined the product necessarily contains pairs
// that cannot exist - a device belongs to exactly one collector - and failing on
// those would let one impossible pair blank an otherwise good panel. A real
// upstream failure (401, 429, a network error) still fails the query, because
// silently returning fewer series than asked for is how a throttled dashboard
// ends up looking merely quiet.
func (d *DomotzDatasource) multiSeries(ctx context.Context, refID string, models []QueryModel, timeRange TimeRange) backend.DataResponse {
	type result struct {
		frame *data.Frame
		err   error
		stage string
	}

	// Resolve first, fetch second. Resolution is served from cache and costs no
	// upstream calls, so it is what tells us which combinations are real before
	// any history is requested - and the cap has to apply to those, not to the
	// product. Four collectors and three devices describe twelve pairs and draw
	// three series; charging the user for the nine impossible ones would reject
	// ordinary selections.
	type resolved struct {
		qm QueryModel
		sc SeriesContext
	}

	type resolution struct {
		out    *resolved
		err    error
		exists bool
	}

	resolutions := make([]resolution, len(models))

	var resolveWG sync.WaitGroup
	for i, qm := range models {
		resolveWG.Add(1)
		go func(i int, qm QueryModel) {
			defer resolveWG.Done()

			sc, exists, err := d.resolveSeriesContext(ctx, qm)
			if err != nil {
				if isForbidden(err) {
					// This key cannot see this collector. That is the same kind
					// of fact as "this device is not on that collector" - the
					// series does not exist for this caller - and one shared
					// collector the key was never granted must not blank a
					// panel that is charting nine others.
					log.DefaultLogger.Warn("skipping series the API key may not read",
						"agentId", qm.AgentID, "deviceId", qm.DeviceID)
					return
				}
				resolutions[i] = resolution{err: err}
				return
			}
			resolutions[i] = resolution{out: &resolved{qm: qm, sc: sc}, exists: exists}
		}(i, qm)
	}
	resolveWG.Wait()

	real := make([]resolved, 0, len(models))
	for _, r := range resolutions {
		if r.err != nil {
			return errorResponse(r.err, "resolving query metadata")
		}
		if r.exists && r.out.sc.Variable.ID > 0 {
			real = append(real, *r.out)
		}
	}

	if len(real) > maxSeriesPerQuery {
		return backend.ErrDataResponse(backend.StatusBadRequest, fmt.Sprintf(
			"this query draws %d series, above the limit of %d; "+
				"narrow the multi-value variables it uses, or split it across panels",
			len(real), maxSeriesPerQuery))
	}

	results := make([]result, len(real))

	var wg sync.WaitGroup
	for i, r := range real {
		wg.Add(1)
		go func(i int, r resolved) {
			defer wg.Done()

			samples, err := d.client.VariableHistory(ctx, r.qm.AgentID.Int64(), r.qm.effectiveDeviceID(), r.sc.Variable.ID, timeRange.From, timeRange.To)
			if err != nil {
				results[i] = result{err: err, stage: "fetching variable history"}
				return
			}

			results[i] = result{frame: BuildFrame(refID, samples, r.sc, timeRange)}
		}(i, r)
	}
	wg.Wait()

	var response backend.DataResponse
	for _, res := range results {
		if res.err != nil {
			return errorResponse(res.err, res.stage)
		}
		if res.frame != nil {
			response.Frames = append(response.Frames, res.frame)
		}
	}

	if len(response.Frames) == 0 {
		log.DefaultLogger.Warn("multi-value query matched no existing series",
			"refId", refID, "combinations", len(models))
	}
	return response
}

// resolveSeriesContext looks up the display metadata for one series.
//
// Every lookup is cache-backed and de-duplicated, so a dashboard refresh costs a
// couple of upstream calls rather than one per panel.
//
// The second return value reports whether the whole triple actually exists
// upstream. A single-series query ignores it and renders a placeholder, so one
// retired sensor cannot break a dashboard; a fan-out uses it to drop
// combinations that a product of two multi-value variables invented.
func (d *DomotzDatasource) resolveSeriesContext(ctx context.Context, qm QueryModel) (SeriesContext, bool, error) {
	var sc SeriesContext

	agents, err := d.client.Agents(ctx)
	if err != nil {
		return sc, false, err
	}
	for _, a := range agents {
		if a.ID == qm.AgentID.Int64() {
			sc.CollectorName = a.DisplayName
			break
		}
	}
	if sc.CollectorName == "" {
		// The collector may have been unbound or its licence may have lapsed
		// since the panel was saved. Keep rendering with a stable placeholder
		// rather than failing the whole panel.
		sc.CollectorName = fmt.Sprintf("Collector %d", qm.AgentID)
	}

	var variables []domotz.Variable
	deviceExists := true

	if qm.Scope == ScopeCollector {
		variables, err = d.client.AgentVariables(ctx, qm.AgentID.Int64(), false)
		if err != nil {
			return sc, false, err
		}
	} else {
		// One collector-wide sweep covers every device, so a panel with a series
		// per device costs one metadata lookup instead of one per device. This is
		// what AgentDeviceVariables exists for, and the listing carries DeviceID
		// so a variable can be checked against the device it was paired with.
		variables, err = d.client.AgentDeviceVariables(ctx, qm.AgentID.Int64(), false)
		if err != nil {
			return sc, false, err
		}

		devices, devErr := d.client.Devices(ctx, qm.AgentID.Int64())
		if devErr != nil {
			return sc, false, devErr
		}
		deviceExists = false
		for _, dev := range devices {
			if dev.ID == qm.DeviceID.Int64() {
				sc.DeviceName = dev.DisplayName
				deviceExists = true
				break
			}
		}
		if sc.DeviceName == "" {
			sc.DeviceName = fmt.Sprintf("Device %d", qm.DeviceID)
		}
	}

	for _, v := range variables {
		if !variableMatches(v, qm) {
			continue
		}
		if qm.Scope == ScopeDevice && v.DeviceID != 0 && v.DeviceID != qm.DeviceID.Int64() {
			// Right variable, wrong device: this pair does not exist.
			continue
		}
		sc.Variable = v
		return sc, deviceExists, nil
	}

	// The variable is gone upstream, or never belonged to this device. Render an
	// empty series named by whatever the query asked for instead of erroring.
	log.DefaultLogger.Warn("variable not found",
		"agentId", qm.AgentID, "deviceId", qm.DeviceID,
		"variableId", qm.VariableID, "variablePath", qm.VariablePath)
	sc.Variable = domotz.Variable{ID: qm.VariableID.Int64(), Path: qm.VariablePath, HasHistory: true}
	return sc, false, nil
}

// isForbidden reports whether an error is the API refusing access to one
// entity, as opposed to refusing the key outright.
//
// 403 is per collector: shared and collaboration collectors appear in the
// account's own listing but are not readable with every key. 401 stays fatal -
// that is the key itself being wrong, and quietly drawing nothing would hide
// it.
func isForbidden(err error) bool {
	var apiErr *domotz.APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusForbidden
}

// variableMatches reports whether an upstream variable is the one a query asked
// for, by id or by sensor path.
//
// Path matching is what makes a metric addressable across devices: the caller
// pairs it with a device, and the DeviceID check above keeps the match honest.
func variableMatches(v domotz.Variable, qm QueryModel) bool {
	if qm.VariablePath != "" {
		return v.Path == qm.VariablePath
	}
	return v.ID == qm.VariableID.Int64()
}

// errorResponse maps a client error onto the Grafana status that matches the
// upstream one, so the panel says "unauthorized" or "rate limited" rather than
// flattening everything to a bad request.
func errorResponse(err error, stage string) backend.DataResponse {
	var apiErr *domotz.APIError
	if errors.As(err, &apiErr) {
		return backend.ErrDataResponse(statusFromHTTP(apiErr.StatusCode),
			fmt.Sprintf("%s: %s", stage, apiErr.Error()))
	}
	// A cancelled or timed-out refresh is not an internal fault, and saying so
	// points the user at the dashboard's query timeout rather than at the plugin.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return backend.ErrDataResponse(backend.StatusTimeout, fmt.Sprintf("%s: %v", stage, err))
	}
	return backend.ErrDataResponse(backend.StatusInternal, fmt.Sprintf("%s: %v", stage, err))
}

func statusFromHTTP(code int) backend.Status {
	switch code {
	case http.StatusUnauthorized:
		return backend.StatusUnauthorized
	case http.StatusForbidden:
		return backend.StatusForbidden
	case http.StatusNotFound:
		return backend.StatusNotFound
	case http.StatusTooManyRequests:
		return backend.StatusTooManyRequests
	case http.StatusBadRequest:
		return backend.StatusBadRequest
	default:
		return backend.StatusInternal
	}
}

// CheckHealth backs the "Save & test" button on the config page.
func (d *DomotzDatasource) CheckHealth(ctx context.Context, _ *backend.CheckHealthRequest) (*backend.CheckHealthResult, error) {
	if d.settings.Path == "" {
		return unhealthy("API URL is missing"), nil
	}
	if d.settings.Secrets.ApiKey == "" {
		return unhealthy("API key is missing"), nil
	}

	if err := d.client.TestConnection(ctx); err != nil {
		var apiErr *domotz.APIError
		if errors.As(err, &apiErr) {
			switch apiErr.StatusCode {
			case http.StatusUnauthorized, http.StatusForbidden:
				return unhealthy("API key was rejected by Domotz"), nil
			case http.StatusTooManyRequests:
				return unhealthy("Domotz API quota exceeded for this key"), nil
			}
			return unhealthy(fmt.Sprintf("Domotz API returned %d", apiErr.StatusCode)), nil
		}
		return unhealthy(fmt.Sprintf("Could not reach the Domotz API: %v", err)), nil
	}

	return &backend.CheckHealthResult{
		Status:  backend.HealthStatusOk,
		Message: "Connected to Domotz",
	}, nil
}

func unhealthy(message string) *backend.CheckHealthResult {
	return &backend.CheckHealthResult{Status: backend.HealthStatusError, Message: message}
}
