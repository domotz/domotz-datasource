package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"

	"github.com/domotz/domotz-datasource/pkg/domotz"
	"github.com/gorilla/mux"
	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/backend/log"
	"github.com/grafana/grafana-plugin-sdk-go/backend/resource/httpadapter"
)

// newResourceHandler wires the editor-facing lookup routes.
//
// These run in the backend rather than the browser so the API key never leaves
// the server.
func newResourceHandler(ds *DomotzDatasource) backend.CallResourceHandler {
	router := mux.NewRouter()

	router.HandleFunc("/collectors", ds.handleCollectors).Methods(http.MethodGet)
	router.HandleFunc("/collectors/{collectorId}/devices", ds.handleDevices).Methods(http.MethodGet)
	router.HandleFunc("/collectors/{collectorId}/variables", ds.handleCollectorVariables).Methods(http.MethodGet)
	router.HandleFunc("/collectors/{collectorId}/devices/{deviceId}/variables", ds.handleDeviceVariables).Methods(http.MethodGet)
	router.HandleFunc("/collectors/{collectorId}/shared-variables", ds.handleSharedDeviceVariables).Methods(http.MethodGet)
	router.HandleFunc("/shared-collector-variables", ds.handleSharedCollectorVariables).Methods(http.MethodGet)
	router.HandleFunc("/usage", ds.handleUsage).Methods(http.MethodGet)
	router.HandleFunc("/cache/invalidate", ds.handleInvalidateCache).Methods(http.MethodPost)

	router.NotFoundHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "unknown resource "+r.URL.Path)
	})

	return httpadapter.New(router)
}

func (d *DomotzDatasource) handleCollectors(w http.ResponseWriter, r *http.Request) {
	agents, err := d.client.Agents(r.Context())
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, agents)
}

func (d *DomotzDatasource) handleDevices(w http.ResponseWriter, r *http.Request) {
	collectorID, ok := pathID(w, r, "collectorId")
	if !ok {
		return
	}

	devices, err := d.client.Devices(r.Context(), collectorID)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, devices)
}

func (d *DomotzDatasource) handleCollectorVariables(w http.ResponseWriter, r *http.Request) {
	collectorID, ok := pathID(w, r, "collectorId")
	if !ok {
		return
	}

	variables, err := d.client.AgentVariables(r.Context(), collectorID, historyOnly(r))
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, decorate(variables))
}

func (d *DomotzDatasource) handleDeviceVariables(w http.ResponseWriter, r *http.Request) {
	collectorID, ok := pathID(w, r, "collectorId")
	if !ok {
		return
	}
	deviceID, ok := pathID(w, r, "deviceId")
	if !ok {
		return
	}

	variables, err := d.client.DeviceVariables(r.Context(), collectorID, deviceID, historyOnly(r))
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, decorate(variables))
}

// handleSharedDeviceVariables lists the metrics exposed by a set of devices,
// once each, keyed by sensor path.
//
// This is what a "compare these devices" dashboard needs. Asking per device and
// merging in the browser would offer ifNumber once per device - three entries
// that look identical and behave differently, since each carries a different
// id. Collapsing on path gives one entry that means "this metric, on whichever
// of the selected devices has it".
//
// ?devices= is a comma-separated list; omitting it covers every device on the
// collector. One collector-wide sweep serves any number of devices, and it is
// the same cached call the query path already makes.
func (d *DomotzDatasource) handleSharedDeviceVariables(w http.ResponseWriter, r *http.Request) {
	collectorID, ok := pathID(w, r, "collectorId")
	if !ok {
		return
	}

	wanted := map[int64]bool{}
	if raw := strings.TrimSpace(r.URL.Query().Get("devices")); raw != "" {
		for _, field := range strings.Split(raw, ",") {
			id, err := strconv.ParseInt(strings.TrimSpace(field), 10, 64)
			if err != nil {
				http.Error(w, fmt.Sprintf("devices must be numeric ids, got %q", field), http.StatusBadRequest)
				return
			}
			wanted[id] = true
		}
	}

	variables, err := d.client.AgentDeviceVariables(r.Context(), collectorID, historyOnly(r))
	if err != nil {
		writeClientError(w, err)
		return
	}

	// Keep the first occurrence of each path. Labels for one path can differ
	// between devices, so there is no "correct" one to prefer - first wins, and
	// the order the API returns is stable.
	seen := map[string]bool{}
	shared := make([]domotz.Variable, 0, len(variables))
	for _, v := range variables {
		if len(wanted) > 0 && !wanted[v.DeviceID] {
			continue
		}
		if v.Path == "" || seen[v.Path] {
			continue
		}
		seen[v.Path] = true
		shared = append(shared, v)
	}

	writeJSON(w, decorate(shared))
}

// handleSharedCollectorVariables is handleSharedDeviceVariables one level up:
// the metrics exposed by a set of collectors, listed once each by sensor path.
//
// Collector metrics have the same shape of problem as device ones. Every
// collector measures Download, Upload and Latency, and each copy carries its
// own variable id, so an id-valued selection can only ever describe one site.
// Selecting the path compares the same measurement across all of them.
//
// Unlike the device case there is no collector-wide sweep to lean on -
// AgentVariables is per collector - so this fans out and merges. The calls are
// cached and de-duplicated, and the fan-out is bounded by how many collectors
// the user actually selected.
func (d *DomotzDatasource) handleSharedCollectorVariables(w http.ResponseWriter, r *http.Request) {
	ids, ok := queryIDs(w, r, "collectors")
	if !ok {
		return
	}
	if len(ids) == 0 {
		writeJSON(w, decorate(nil))
		return
	}

	// One request per collector, in parallel. Sequentially this is the slowest
	// path in the plugin for exactly the account it exists to serve: an MSP
	// selecting fifty sites would wait out fifty round trips before the metric
	// dropdown filled in.
	lists := make([][]domotz.Variable, len(ids))
	errs := make([]error, len(ids))

	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func(i int, id int64) {
			defer wg.Done()
			defer func() {
				if rec := recover(); rec != nil {
					log.DefaultLogger.Error("recovered from panic listing collector variables",
						"collectorId", id, "panic", fmt.Sprint(rec), "stack", string(debug.Stack()))
					errs[i] = fmt.Errorf("internal error listing variables for collector %d", id)
				}
			}()
			lists[i], errs[i] = d.client.AgentVariables(r.Context(), id, historyOnly(r))
		}(i, id)
	}
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			writeClientError(w, err)
			return
		}
	}

	// Merged in the order the caller asked for, so the dropdown does not
	// reshuffle between refreshes.
	seen := map[string]bool{}
	shared := make([]domotz.Variable, 0)
	for _, variables := range lists {
		for _, v := range variables {
			if v.Path == "" || seen[v.Path] {
				continue
			}
			seen[v.Path] = true
			shared = append(shared, v)
		}
	}

	writeJSON(w, decorate(shared))
}

// queryIDs reads a comma-separated list of numeric ids from a query parameter.
func queryIDs(w http.ResponseWriter, r *http.Request, param string) ([]int64, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get(param))
	if raw == "" {
		return nil, true
	}

	ids := make([]int64, 0)
	for _, field := range strings.Split(raw, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		id, err := strconv.ParseInt(field, 10, 64)
		if err != nil {
			http.Error(w, fmt.Sprintf("%s must be numeric ids, got %q", param, field), http.StatusBadRequest)
			return nil, false
		}
		ids = append(ids, id)
	}
	return ids, true
}

// handleInvalidateCache backs the refresh control in the query editor, so an
// operator who has just onboarded a collector does not have to wait out the
// metadata TTL to see it.
func (d *DomotzDatasource) handleInvalidateCache(w http.ResponseWriter, _ *http.Request) {
	d.InvalidateCache()
	w.WriteHeader(http.StatusNoContent)
}

func (d *DomotzDatasource) handleUsage(w http.ResponseWriter, r *http.Request) {
	usage, err := d.client.Usage(r.Context())
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, usage)
}

// variableOption is a variable as the query editor consumes it: the resolved
// display label is computed once, in the backend, so the picker and the panel
// legend cannot disagree.
type variableOption struct {
	domotz.Variable
	DisplayLabel string `json:"displayLabel"`
}

func decorate(variables []domotz.Variable) []variableOption {
	out := make([]variableOption, len(variables))
	for i, v := range variables {
		out[i] = variableOption{Variable: v, DisplayLabel: v.DisplayLabel()}
	}
	return out
}

// historyOnly reports whether the caller asked to see only history-capable
// variables. The editor sets this when picking a series to graph.
func historyOnly(r *http.Request) bool {
	return r.URL.Query().Get("hasHistory") == "true"
}

func pathID(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	raw := mux.Vars(r)[name]
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid "+name+": "+raw)
		return 0, false
	}
	return id, true
}

func writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		log.DefaultLogger.Error("encoding resource response", "error", err)
	}
}

// writeClientError forwards the upstream status so the editor can tell an
// expired key apart from a throttled one.
func writeClientError(w http.ResponseWriter, err error) {
	var apiErr *domotz.APIError
	if errors.As(err, &apiErr) {
		writeError(w, apiErr.StatusCode, apiErr.Body)
		return
	}
	writeError(w, http.StatusInternalServerError, err.Error())
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
