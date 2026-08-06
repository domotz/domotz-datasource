package plugin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

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
