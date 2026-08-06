package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/domotz/domotz-datasource/pkg/domotz"
	"github.com/domotz/domotz-datasource/pkg/models"
	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/stretchr/testify/require"
)

// Live end-to-end exercise of QueryData against the real Domotz API:
//
//	DOMOTZ_TEST_API_KEY=... go test ./pkg/plugin/ -run TestLiveQuery -v
func liveDatasource(t *testing.T) *DomotzDatasource {
	t.Helper()

	key := os.Getenv("DOMOTZ_TEST_API_KEY")
	if key == "" {
		t.Skip("set DOMOTZ_TEST_API_KEY to run live query tests")
	}
	baseURL := os.Getenv("DOMOTZ_TEST_API_URL")
	if baseURL == "" {
		baseURL = "https://api-eu-west-1-cell-1.domotz.com/public-api/v1/"
	}

	ds := &DomotzDatasource{
		settings: &models.PluginSettings{
			Path:    baseURL,
			Secrets: &models.SecretPluginSettings{ApiKey: key},
		},
		client: domotz.NewClient(baseURL, key, &http.Client{Timeout: 30 * time.Second}),
	}
	ds.CallResourceHandler = newResourceHandler(ds)
	return ds
}

// pickVariables finds one numeric and one non-numeric history variable on a
// real collector, so both frame typings are exercised against live data.
func pickVariables(t *testing.T, ds *DomotzDatasource) (agentID int64, numeric, textual domotz.Variable) {
	t.Helper()
	ctx := context.Background()

	agents, err := ds.client.Agents(ctx)
	require.NoError(t, err)

	for _, a := range agents {
		vars, err := ds.client.AgentDeviceVariables(ctx, a.ID, true)
		require.NoError(t, err)

		for _, v := range vars {
			if v.Value == "" {
				continue
			}
			_, parseErr := strconv.ParseFloat(v.Value, 64)
			if parseErr == nil && numeric.ID == 0 {
				numeric = v
			}
			if parseErr != nil && textual.ID == 0 {
				textual = v
			}
			if numeric.ID != 0 && textual.ID != 0 {
				return a.ID, numeric, textual
			}
		}
		if numeric.ID != 0 || textual.ID != 0 {
			return a.ID, numeric, textual
		}
	}
	t.Skip("no suitable live variables found")
	return 0, numeric, textual
}

func liveQuery(t *testing.T, ds *DomotzDatasource, qm QueryModel) backend.DataResponse {
	t.Helper()

	raw, err := json.Marshal(qm)
	require.NoError(t, err)

	to := time.Now().UTC()
	resp, err := ds.QueryData(context.Background(), &backend.QueryDataRequest{
		Queries: []backend.DataQuery{{
			RefID:     "A",
			JSON:      raw,
			TimeRange: backend.TimeRange{From: to.Add(-7 * 24 * time.Hour), To: to},
		}},
	})
	require.NoError(t, err)
	return resp.Responses["A"]
}

// A multi-value $device against real data: one panel, one series per device.
func TestLiveQuery_MultiValueDeviceFansOutToOneFramePerSeries(t *testing.T) {
	ds := liveDatasource(t)
	ctx := context.Background()

	agents, err := ds.client.Agents(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, agents)

	// Find one collector with the same metric on two different devices, which is
	// exactly the comparison panel multi-value exists for.
	var (
		agentID          int64
		devices          []int64
		variables        []int64
		byLabel          = map[string][]domotz.Variable{}
		chosenLabelFound bool
	)
	for _, a := range agents {
		vars, varsErr := ds.client.AgentDeviceVariables(ctx, a.ID, true)
		require.NoError(t, varsErr)

		byLabel = map[string][]domotz.Variable{}
		for _, v := range vars {
			if v.DeviceID == 0 {
				continue
			}
			byLabel[v.DisplayLabel()] = append(byLabel[v.DisplayLabel()], v)
		}
		for _, group := range byLabel {
			if len(group) < 2 || group[0].DeviceID == group[1].DeviceID {
				continue
			}
			agentID = a.ID
			devices = []int64{group[0].DeviceID, group[1].DeviceID}
			variables = []int64{group[0].ID, group[1].ID}
			chosenLabelFound = true
			break
		}
		if chosenLabelFound {
			break
		}
	}
	if !chosenLabelFound {
		t.Skip("no live collector exposes one metric on two devices")
	}

	raw := []byte(`{"scope":"device","agentId":"` + strconv.FormatInt(agentID, 10) +
		`","deviceId":"{` + strconv.FormatInt(devices[0], 10) + `,` + strconv.FormatInt(devices[1], 10) +
		`}","variableId":"{` + strconv.FormatInt(variables[0], 10) + `,` + strconv.FormatInt(variables[1], 10) + `}"}`)

	to := time.Now().UTC()
	resp, err := ds.QueryData(ctx, &backend.QueryDataRequest{
		Queries: []backend.DataQuery{{
			RefID:     "A",
			JSON:      raw,
			TimeRange: backend.TimeRange{From: to.Add(-24 * time.Hour), To: to},
		}},
	})
	require.NoError(t, err)

	res := resp.Responses["A"]
	require.NoError(t, res.Error)

	// The product is four pairs; only the two real device/variable pairings exist.
	require.Len(t, res.Frames, 2, "impossible device/metric pairs must be skipped, not errored")

	seen := map[string]bool{}
	for _, frame := range res.Frames {
		require.Equal(t, "A", frame.RefID)
		value := frame.Fields[1]

		rows, rowErr := frame.RowLen()
		require.NoError(t, rowErr)
		require.Equal(t, rows, value.Len(), "fields must be equal length or Grafana rejects the frame")

		device := value.Labels["device"]
		require.NotEmpty(t, device)
		require.False(t, seen[device], "each device must appear once")
		seen[device] = true

		t.Logf("series %q -> %d rows", value.Config.DisplayNameFromDS, rows)
	}
}

func TestLiveQuery_NumericVariableRendersAlignedFrame(t *testing.T) {
	ds := liveDatasource(t)
	agentID, numeric, _ := pickVariables(t, ds)
	if numeric.ID == 0 {
		t.Skip("no numeric variable available")
	}

	res := liveQuery(t, ds, QueryModel{
		Scope: ScopeDevice, AgentID: ID(agentID), DeviceID: ID(numeric.DeviceID), VariableID: ID(numeric.ID),
	})

	require.NoError(t, res.Error)
	require.Len(t, res.Frames, 1)

	frame := res.Frames[0]
	rows, err := frame.RowLen()
	require.NoError(t, err, "fields must be equal length")

	value := frame.Fields[1]
	require.NotEmpty(t, value.Labels["collector"])
	require.NotEmpty(t, value.Labels["device"])
	require.NotEmpty(t, value.Config.DisplayNameFromDS)

	t.Logf("variable %d (%s) -> %d rows, unit=%q, series=%q",
		numeric.ID, numeric.DisplayLabel(), rows, value.Config.Unit, value.Config.DisplayNameFromDS)
}

// The case that used to produce a frame Grafana rejects outright.
func TestLiveQuery_StringVariableRendersAlignedFrame(t *testing.T) {
	ds := liveDatasource(t)
	agentID, _, textual := pickVariables(t, ds)
	if textual.ID == 0 {
		t.Skip("no string-valued variable available")
	}

	res := liveQuery(t, ds, QueryModel{
		Scope: ScopeDevice, AgentID: ID(agentID), DeviceID: ID(textual.DeviceID), VariableID: ID(textual.ID),
	})

	require.NoError(t, res.Error)
	frame := res.Frames[0]

	rows, err := frame.RowLen()
	require.NoError(t, err, "a string-valued series must still produce equal-length fields")

	t.Logf("variable %d (%s, value=%q) -> %d rows",
		textual.ID, textual.DisplayLabel(), textual.Value, rows)
}

// Sweep a broad slice of real variables and assert every resulting frame is
// well formed. This is the check that would have caught the original bug.
func TestLiveQuery_EveryFrameIsWellFormed(t *testing.T) {
	ds := liveDatasource(t)
	ctx := context.Background()

	agents, err := ds.client.Agents(ctx)
	require.NoError(t, err)

	const sampleSize = 40
	checked := 0

	for _, a := range agents {
		vars, err := ds.client.AgentDeviceVariables(ctx, a.ID, true)
		require.NoError(t, err)

		for _, v := range vars {
			if checked >= sampleSize {
				t.Logf("checked %d live variables, all frames well formed", checked)
				return
			}

			res := liveQuery(t, ds, QueryModel{
				Scope: ScopeDevice, AgentID: ID(a.ID), DeviceID: ID(v.DeviceID), VariableID: ID(v.ID),
			})
			require.NoError(t, res.Error, "variable %d errored", v.ID)
			require.Len(t, res.Frames, 1)

			_, err := res.Frames[0].RowLen()
			require.NoError(t, err, "variable %d (%s) produced a misaligned frame",
				v.ID, v.DisplayLabel())
			checked++
		}
	}
	require.Positive(t, checked)
	t.Logf("checked %d live variables, all frames well formed", checked)
}

func TestLiveQuery_CheckHealthSucceeds(t *testing.T) {
	ds := liveDatasource(t)

	res, err := ds.CheckHealth(context.Background(), &backend.CheckHealthRequest{})

	require.NoError(t, err)
	require.Equal(t, backend.HealthStatusOk, res.Status, res.Message)
}

func TestLiveQuery_ResourceRoutesServeEditor(t *testing.T) {
	ds := liveDatasource(t)

	status, body := callResource(t, ds, "collectors")
	require.Equal(t, http.StatusOK, status)

	var agents []domotz.Agent
	require.NoError(t, json.Unmarshal(body, &agents))
	require.NotEmpty(t, agents)

	status, body = callResource(t, ds, "collectors/"+strconv.FormatInt(agents[0].ID, 10)+"/devices")
	require.Equal(t, http.StatusOK, status)

	var devices []domotz.Device
	require.NoError(t, json.Unmarshal(body, &devices))
	t.Logf("editor sees %d collectors, %d devices on the first", len(agents), len(devices))
}
