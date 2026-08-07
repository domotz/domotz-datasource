package plugin

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/domotz/domotz-datasource/pkg/domotz"
	"github.com/stretchr/testify/require"
)

func at(s string) time.Time {
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return ts.UTC()
}

func apiTime(s string) *domotz.APITime {
	return &domotz.APITime{Time: at(s)}
}

func sample(ts, value string) domotz.HistorySample {
	return domotz.HistorySample{Timestamp: at(ts), Value: value}
}

func testRange() TimeRange {
	return TimeRange{From: at("2026-08-04T00:00:00Z"), To: at("2026-08-04T23:59:59Z")}
}

func numericContext() SeriesContext {
	return SeriesContext{
		CollectorName: "Acme HQ",
		DeviceName:    "Core Switch",
		Variable: domotz.Variable{
			ID:         42,
			Label:      "Uptime",
			Unit:       "%",
			HasHistory: true,
		},
	}
}

// The regression this whole rewrite hangs on: a series that is mostly numeric
// but contains one unparseable sample used to produce a value field shorter
// than the time field, which Grafana rejects outright.
func TestBuildFrame_MixedNumericAndStringSamplesStayAligned(t *testing.T) {
	samples := []domotz.HistorySample{
		sample("2026-08-04T10:00:00Z", "1"),
		sample("2026-08-04T10:01:00Z", "UP"), // not a float
		sample("2026-08-04T10:02:00Z", "0"),
	}

	frame := BuildFrame("A", samples, numericContext(), testRange())

	require.Len(t, frame.Fields, 2)
	require.Equal(t, 3, frame.Fields[0].Len(), "time field")
	require.Equal(t, 3, frame.Fields[1].Len(), "value field")
	require.Equal(t, frame.Fields[0].Len(), frame.Fields[1].Len(),
		"every field in a frame must have the same length")

	// One unparseable sample demotes the whole series to strings, which keeps
	// the columns aligned instead of silently dropping rows.
	require.Equal(t, "1", frame.Fields[1].At(0))
	require.Equal(t, "UP", frame.Fields[1].At(1))
	require.Equal(t, "0", frame.Fields[1].At(2))
}

func TestBuildFrame_AllNumericProducesFloatField(t *testing.T) {
	samples := []domotz.HistorySample{
		sample("2026-08-04T10:00:00Z", "12.5"),
		sample("2026-08-04T10:01:00Z", "13"),
	}

	frame := BuildFrame("A", samples, numericContext(), testRange())

	require.Equal(t, 2, frame.Fields[1].Len())
	require.Equal(t, 12.5, frame.Fields[1].At(0))
	require.Equal(t, float64(13), frame.Fields[1].At(1))
}

func TestBuildFrame_SetsLabelsForOverrides(t *testing.T) {
	frame := BuildFrame("A", []domotz.HistorySample{sample("2026-08-04T10:00:00Z", "1")},
		numericContext(), testRange())

	labels := frame.Fields[1].Labels
	require.Equal(t, "Acme HQ", labels["collector"])
	require.Equal(t, "Core Switch", labels["device"])
	require.Equal(t, "Uptime", labels["variable"])
	require.Equal(t, "%", labels["unit"])
}

func TestBuildFrame_CollectorScopeOmitsDeviceLabel(t *testing.T) {
	sc := numericContext()
	sc.DeviceName = ""

	frame := BuildFrame("A", []domotz.HistorySample{sample("2026-08-04T10:00:00Z", "1")},
		sc, testRange())

	_, hasDevice := frame.Fields[1].Labels["device"]
	require.False(t, hasDevice)
	require.Equal(t, "Acme HQ: Uptime", frame.Fields[1].Config.DisplayNameFromDS)
}

func TestBuildFrame_MapsKnownUnitsAndSkipsUnknownOnes(t *testing.T) {
	sc := numericContext()
	frame := BuildFrame("A", []domotz.HistorySample{sample("2026-08-04T10:00:00Z", "1")}, sc, testRange())
	require.Equal(t, "percent", frame.Fields[1].Config.Unit)

	sc.Variable.Unit = "widgets-per-fortnight"
	frame = BuildFrame("A", []domotz.HistorySample{sample("2026-08-04T10:00:00Z", "1")}, sc, testRange())
	require.Empty(t, frame.Fields[1].Config.Unit, "unmapped units must not leak into the panel")
	require.Equal(t, "widgets-per-fortnight", frame.Fields[1].Labels["unit"], "but must stay reachable")
}

func TestBuildFrame_SortsOutOfOrderSamples(t *testing.T) {
	samples := []domotz.HistorySample{
		sample("2026-08-04T10:02:00Z", "3"),
		sample("2026-08-04T10:00:00Z", "1"),
		sample("2026-08-04T10:01:00Z", "2"),
	}

	frame := BuildFrame("A", samples, numericContext(), testRange())

	require.Equal(t, float64(1), frame.Fields[1].At(0))
	require.Equal(t, float64(2), frame.Fields[1].At(1))
	require.Equal(t, float64(3), frame.Fields[1].At(2))
}

func TestBuildFrame_DedupesRepeatedTimestamps(t *testing.T) {
	samples := []domotz.HistorySample{
		sample("2026-08-04T10:00:00Z", "1"),
		sample("2026-08-04T10:00:00Z", "2"),
	}

	frame := BuildFrame("A", samples, numericContext(), testRange())

	require.Equal(t, 1, frame.Fields[0].Len())
	require.Equal(t, float64(2), frame.Fields[1].At(0), "last reading wins")
}

func TestBuildFrame_EmptyHistoryProducesEmptyAlignedFrame(t *testing.T) {
	frame := BuildFrame("A", nil, numericContext(), testRange())

	require.Len(t, frame.Fields, 2)
	require.Equal(t, 0, frame.Fields[0].Len())
	require.Equal(t, 0, frame.Fields[1].Len())
}

// A variable with no history still has a live reading worth plotting, but only
// when it falls inside the queried window.
func TestBuildFrame_NoHistoryVariableUsesLiveValueInsideRange(t *testing.T) {
	sc := numericContext()
	sc.Variable.HasHistory = false
	sc.Variable.Value = "7"
	sc.Variable.ValueUpdateTime = apiTime("2026-08-04T12:00:00Z")

	frame := BuildFrame("A", nil, sc, testRange())

	require.Equal(t, 1, frame.Fields[0].Len())
	require.Equal(t, float64(7), frame.Fields[1].At(0))
	require.Equal(t, at("2026-08-04T12:00:00Z"), frame.Fields[0].At(0))
}

func TestBuildFrame_NoHistoryVariableOutsideRangeIsOmitted(t *testing.T) {
	sc := numericContext()
	sc.Variable.HasHistory = false
	sc.Variable.Value = "7"
	sc.Variable.ValueUpdateTime = apiTime("2026-07-01T12:00:00Z") // before the window

	frame := BuildFrame("A", nil, sc, testRange())

	require.Equal(t, 0, frame.Fields[0].Len())
}

func TestBuildFrame_HistoryVariableDoesNotFallBackToLiveValue(t *testing.T) {
	sc := numericContext()
	sc.Variable.HasHistory = true
	sc.Variable.Value = "7"
	sc.Variable.ValueUpdateTime = apiTime("2026-08-04T12:00:00Z")

	frame := BuildFrame("A", nil, sc, testRange())

	require.Equal(t, 0, frame.Fields[0].Len(),
		"a history-backed variable with no samples genuinely has no data")
}

func TestBuildFrame_PreservesRefID(t *testing.T) {
	frame := BuildFrame("B", nil, numericContext(), testRange())
	require.Equal(t, "B", frame.RefID)
}

func TestQueryModel_Validate(t *testing.T) {
	tests := []struct {
		name    string
		model   QueryModel
		wantErr string
	}{
		{
			name:  "complete device query",
			model: QueryModel{Scope: ScopeDevice, AgentID: 1, DeviceID: 2, VariableID: 3},
		},
		{
			name:  "complete collector query needs no device",
			model: QueryModel{Scope: ScopeCollector, AgentID: 1, VariableID: 3},
		},
		{
			name:    "missing scope",
			model:   QueryModel{AgentID: 1, VariableID: 3},
			wantErr: "scope is required",
		},
		{
			name:    "unknown scope",
			model:   QueryModel{Scope: "galaxy", AgentID: 1, VariableID: 3},
			wantErr: `unknown scope "galaxy"`,
		},
		{
			name:    "missing collector",
			model:   QueryModel{Scope: ScopeDevice, DeviceID: 2, VariableID: 3},
			wantErr: "collector must be selected",
		},
		{
			name:    "device scope without device",
			model:   QueryModel{Scope: ScopeDevice, AgentID: 1, VariableID: 3},
			wantErr: "device must be selected",
		},
		{
			name:    "missing variable",
			model:   QueryModel{Scope: ScopeDevice, AgentID: 1, DeviceID: 2},
			wantErr: "variable must be selected",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.model.Validate()
			if tc.wantErr == "" {
				require.NoError(t, err)
				require.True(t, tc.model.IsComplete())
				return
			}
			require.ErrorContains(t, err, tc.wantErr)
			require.False(t, tc.model.IsComplete())
		})
	}
}

func TestParseQueryModel_AcceptsNumericIDs(t *testing.T) {
	qm, err := ParseQueryModel([]byte(`{"scope":"device","agentId":10,"deviceId":20,"variableId":30}`))

	require.NoError(t, err)
	require.Equal(t, ScopeDevice, qm.Scope)
	require.Equal(t, ID(10), qm.AgentID)
	require.Equal(t, ID(20), qm.DeviceID)
	require.Equal(t, ID(30), qm.VariableID)
	require.Equal(t, int64(20), qm.effectiveDeviceID())
}

func TestQueryModel_CollectorScopeUsesCollectorHistoryEndpoint(t *testing.T) {
	qm := QueryModel{Scope: ScopeCollector, AgentID: 10, DeviceID: 20, VariableID: 30}
	require.Equal(t, int64(0), qm.effectiveDeviceID(),
		"a collector-scoped query must ignore any stale device id")
}

func TestVariableDisplayLabel_FallsBackThroughMetricAndPath(t *testing.T) {
	require.Equal(t, "Uptime", domotz.Variable{Label: "Uptime", Metric: "up_time"}.DisplayLabel())
	require.Equal(t, "Up Time", domotz.Variable{Metric: "up_time"}.DisplayLabel())
	require.Equal(t, "Rx Bytes", domotz.Variable{Path: "iface/eth0/rx-bytes"}.DisplayLabel())
	require.Equal(t, "value", domotz.Variable{}.DisplayLabel())
}

// applyTemplateVariables interpolates a dashboard variable into a string, so
// ids reach the backend quoted. Both encodings must decode identically.
func TestParseQueryModel_AcceptsStringIDsFromTemplateInterpolation(t *testing.T) {
	qm, err := ParseQueryModel([]byte(`{"scope":"device","agentId":"10","deviceId":"20","variableId":"30"}`))

	require.NoError(t, err)
	require.Equal(t, ID(10), qm.AgentID)
	require.Equal(t, ID(20), qm.DeviceID)
	require.Equal(t, ID(30), qm.VariableID)
}

func TestParseQueryModel_TreatsEmptyAndNullIDsAsUnset(t *testing.T) {
	qm, err := ParseQueryModel([]byte(`{"scope":"collector","agentId":7,"deviceId":"","variableId":9}`))
	require.NoError(t, err)
	require.Equal(t, ID(0), qm.DeviceID)

	_, err = ParseQueryModel([]byte(`{"scope":"device","agentId":null,"deviceId":2,"variableId":3}`))
	require.ErrorContains(t, err, "collector must be selected")
}

// An uninterpolated variable is a configuration mistake worth naming, not a
// silent zero that renders as "no data".
func TestParseQueryModel_RejectsUninterpolatedVariable(t *testing.T) {
	_, err := ParseQueryModel([]byte(`{"scope":"device","agentId":"$collector","deviceId":2,"variableId":3}`))

	require.ErrorContains(t, err, "$collector")
	require.ErrorContains(t, err, "numeric id")
}

func TestVariableDisplayLabel_TitleCasesTheFirstRuneNotTheFirstByte(t *testing.T) {
	// Slicing one byte off a multi-byte character used to emit a replacement
	// byte followed by orphaned continuation bytes.
	require.Equal(t, "Élan Vital", domotz.Variable{Metric: "élan_vital"}.DisplayLabel())
	require.Equal(t, "État", domotz.Variable{Path: "sensors/état"}.DisplayLabel())
	require.Equal(t, "Ωmega", domotz.Variable{Metric: "ωmega"}.DisplayLabel())
}

// Grafana renders a multi-value variable as a glob by default, and as a plain
// comma-separated list under the csv format.
func TestExpandQueryModel_ExpandsMultiValueVariables(t *testing.T) {
	for _, encoding := range []string{`"{11,12,13}"`, `"11,12,13"`, `[11,12,13]`} {
		t.Run(encoding, func(t *testing.T) {
			raw := []byte(`{"scope":"device","agentId":7,"deviceId":` + encoding + `,"variableId":99}`)

			models, err := ExpandQueryModel(raw)
			require.NoError(t, err)
			require.Len(t, models, 3)

			require.Equal(t, []ID{11, 12, 13},
				[]ID{models[0].DeviceID, models[1].DeviceID, models[2].DeviceID},
				"series order must be stable across refreshes")
			for _, qm := range models {
				require.Equal(t, ID(7), qm.AgentID)
				require.Equal(t, ID(99), qm.VariableID)
			}
		})
	}
}

func TestExpandQueryModel_CombinesSeveralMultiValueFields(t *testing.T) {
	models, err := ExpandQueryModel(
		[]byte(`{"scope":"device","agentId":"{7,8}","deviceId":"{11,12}","variableId":99}`))

	require.NoError(t, err)
	require.Len(t, models, 4, "the expansion is the product of the multi-value fields")
}

func TestExpandQueryModel_RejectsExpansionsAboveTheCombinationCeiling(t *testing.T) {
	ids := make([]string, maxCombinations+1)
	for i := range ids {
		ids[i] = strconv.Itoa(i + 1)
	}
	raw := []byte(`{"scope":"device","agentId":7,"deviceId":"{` + strings.Join(ids, ",") + `}","variableId":99}`)

	_, err := ExpandQueryModel(raw)

	require.ErrorContains(t, err, "describes 501 combinations")
	require.ErrorContains(t, err, "limit of 500")
}

// The series cap is deliberately NOT applied here. A multi-collector selection
// describes far more pairs than can exist - a device belongs to exactly one
// collector - so counting the product against it would reject a selection that
// draws three series. Only combinations that resolve to something real are
// charged against maxSeriesPerQuery, which multiSeries enforces once metadata
// is in hand.
func TestExpandQueryModel_DoesNotChargeImpossiblePairsAgainstTheSeriesCap(t *testing.T) {
	collectors := make([]string, 5)
	devices := make([]string, 5)
	for i := range collectors {
		collectors[i] = strconv.Itoa(i + 1)
		devices[i] = strconv.Itoa(100 + i)
	}
	raw := []byte(`{"scope":"device","agentId":"{` + strings.Join(collectors, ",") + `}",` +
		`"deviceId":"{` + strings.Join(devices, ",") + `}","variableId":99}`)

	models, err := ExpandQueryModel(raw)

	require.NoError(t, err, "25 pairs describe at most 5 real series")
	require.Len(t, models, 25)
}

// "Include All" with no custom all-value leaks the placeholder itself. The two
// mistakes need different fixes, so they get different messages.
func TestExpandQueryModel_RejectsIncludeAllPlaceholder(t *testing.T) {
	_, err := ExpandQueryModel([]byte(`{"scope":"device","agentId":"$__all","deviceId":11,"variableId":99}`))

	require.ErrorContains(t, err, "Include All")
	require.NotContains(t, err.Error(), "numeric id")
}

func TestExpandQueryModel_SingleValueYieldsExactlyOneSeries(t *testing.T) {
	models, err := ExpandQueryModel([]byte(`{"scope":"collector","agentId":"7","variableId":"55"}`))

	require.NoError(t, err)
	require.Len(t, models, 1)
	require.Equal(t, ID(0), models[0].DeviceID)
}

// The path form is what makes "this metric, on these devices" expressible: a
// metric has a different variable id on every device that exposes it, so an
// id-valued query can only ever describe one of them.
func TestExpandQueryModel_AcceptsSensorPathsAsVariableReferences(t *testing.T) {
	const path = "device_oid_sensor/oid/1.3.6.1.2.1.2.1.0/data"

	models, err := ExpandQueryModel(
		[]byte(`{"scope":"device","agentId":7,"deviceId":"{11,12}","variableId":"` + path + `"}`))

	require.NoError(t, err)
	require.Len(t, models, 2, "one series per selected device")
	for i, qm := range models {
		require.Equal(t, path, qm.VariablePath, "series %d", i)
		require.Zero(t, qm.VariableID, "a path carries no id until it is resolved per device")
		require.NoError(t, qm.Validate(), "a path alone is a complete query")
	}
	require.Equal(t, []ID{11, 12}, []ID{models[0].DeviceID, models[1].DeviceID})
}

func TestExpandQueryModel_StillTreatsNumericReferencesAsIds(t *testing.T) {
	models, err := ExpandQueryModel(
		[]byte(`{"scope":"device","agentId":7,"deviceId":11,"variableId":"99"}`))

	require.NoError(t, err)
	require.Len(t, models, 1)
	require.Equal(t, ID(99), models[0].VariableID)
	require.Empty(t, models[0].VariablePath, "a numeric reference must not be mistaken for a path")
}

// An uninterpolated variable is the one non-numeric string that is definitely
// not a sensor path. Accepting it would turn a dashboard wiring mistake into an
// empty panel with nothing to explain it.
func TestExpandQueryModel_RejectsUninterpolatedVariableAsPath(t *testing.T) {
	_, err := ExpandQueryModel(
		[]byte(`{"scope":"device","agentId":7,"deviceId":11,"variableId":"$deviceMetric"}`))

	require.Error(t, err)
	require.Contains(t, err.Error(), "uninterpolated")
}
