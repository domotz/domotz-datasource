package plugin

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/domotz/domotz-datasource/pkg/domotz"
	"github.com/grafana/grafana-plugin-sdk-go/data"
)

// Scope selects whether a query targets a collector's own variables or the
// variables of one of its devices.
type Scope string

const (
	ScopeCollector Scope = "collector"
	ScopeDevice    Scope = "device"
)

// QueryModel is the persisted shape of a panel query.
//
// It holds identifiers only. Display metadata (labels, units, device names,
// current values) is resolved from the API at query time, never written into
// the dashboard JSON: baking it in is what made series names go stale after a
// rename and made no-history variables replay a value frozen at the moment the
// panel was authored.
type QueryModel struct {
	Scope      Scope `json:"scope"`
	AgentID    ID    `json:"agentId"`
	DeviceID   ID    `json:"deviceId"`
	VariableID ID    `json:"variableId"`
}

// maxSeriesPerQuery caps how many series one query may expand into.
//
// Each series costs one history request, and the API key allows 5 concurrent
// requests (GET /meta/usage reports concurrent_allowed). The cap stops a stray
// "Include All" on a large dimension from turning one panel into hundreds of
// queued requests, and a panel carrying twenty overlaid series is already past
// the point of being readable.
const maxSeriesPerQuery = 20

// ID is a single entity identifier that tolerates both JSON numbers and numeric
// strings.
//
// The query editor stores identifiers as strings so a field can hold a
// dashboard template variable ("$collector") until applyTemplateVariables
// interpolates it. Interpolation yields a string, so by the time a query
// reaches the backend an id may arrive either way.
type ID int64

func (id *ID) UnmarshalJSON(data []byte) error {
	var ids IDList
	if err := ids.UnmarshalJSON(data); err != nil {
		return err
	}
	switch len(ids) {
	case 0:
		*id = 0
	case 1:
		*id = ID(ids[0])
	default:
		return fmt.Errorf("expected a single id, got %d", len(ids))
	}
	return nil
}

func (id ID) MarshalJSON() ([]byte, error) {
	return json.Marshal(int64(id))
}

func (id ID) Int64() int64 { return int64(id) }

// IDList is one or more entity identifiers, as a field of a saved query arrives
// once its template variable has been interpolated.
type IDList []int64

func (l *IDList) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	switch trimmed {
	case "", "null", `""`:
		*l = nil
		return nil
	}

	// An array, which is how a value taken straight from scopedVars can arrive.
	if trimmed[0] == '[' {
		var items []json.RawMessage
		if err := json.Unmarshal(data, &items); err != nil {
			return err
		}
		ids := make(IDList, 0, len(items))
		for _, item := range items {
			var one IDList
			if err := one.UnmarshalJSON(item); err != nil {
				return err
			}
			ids = append(ids, one...)
		}
		*l = ids
		return nil
	}

	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		ids, err := parseIDString(s)
		if err != nil {
			return err
		}
		*l = ids
		return nil
	}

	var n int64
	if err := json.Unmarshal(data, &n); err != nil {
		return err
	}
	*l = IDList{n}
	return nil
}

// parseIDString decodes the forms an interpolated template variable takes.
//
// Grafana renders a multi-value variable as a glob ("{10,20}") by default and as
// a plain comma-separated list under the csv format. "$__all" leaks through when
// a variable has "Include All" enabled but no custom all-value and its options
// have not loaded yet, and an uninterpolated "$collector" arrives verbatim -
// each gets its own message, because the fix differs.
func parseIDString(s string) (IDList, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if s == "$__all" {
		return nil, fmt.Errorf(`%q is the "Include All" placeholder rather than a value; `+
			`give the variable a custom All value, or select specific values`, s)
	}

	body := s
	if strings.HasPrefix(body, "{") && strings.HasSuffix(body, "}") {
		body = body[1 : len(body)-1]
	}

	fields := strings.Split(body, ",")
	ids := make(IDList, 0, len(fields))
	for _, field := range fields {
		field = strings.Trim(strings.TrimSpace(field), `"`)
		if field == "" {
			continue
		}
		n, err := strconv.ParseInt(field, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("expected a numeric id, got %q "+
				"(an uninterpolated dashboard variable will look like this)", s)
		}
		ids = append(ids, n)
	}
	return ids, nil
}

// orZero makes an unset field contribute exactly one (zero) value to an
// expansion, so a collector-scoped query with no device still yields one model
// and Validate can report a genuinely missing id.
func (l IDList) orZero() []int64 {
	if len(l) == 0 {
		return []int64{0}
	}
	return l
}

// queryModelRaw is the wire form of a query, before multi-value variables are
// expanded into individual series.
type queryModelRaw struct {
	Scope      Scope  `json:"scope"`
	AgentID    IDList `json:"agentId"`
	DeviceID   IDList `json:"deviceId"`
	VariableID IDList `json:"variableId"`
}

// ExpandQueryModel decodes a panel query into one QueryModel per series.
//
// A single-value query yields exactly one model. A query whose ids came from
// multi-value variables yields their combination, which is what lets one panel
// compare a metric across several devices - a shape panel Repeat cannot express,
// since Repeat produces separate panels rather than overlaid series.
//
// Combinations are emitted in field order (collector, then device, then metric)
// so the series order a panel shows is stable across refreshes.
func ExpandQueryModel(raw json.RawMessage) ([]QueryModel, error) {
	var rm queryModelRaw
	if err := json.Unmarshal(raw, &rm); err != nil {
		return nil, fmt.Errorf("malformed query: %w", err)
	}

	agents, devices, variables := rm.AgentID.orZero(), rm.DeviceID.orZero(), rm.VariableID.orZero()

	total := len(agents) * len(devices) * len(variables)
	if total > maxSeriesPerQuery {
		return nil, fmt.Errorf("this query expands to %d series, above the limit of %d; "+
			"narrow the multi-value variables it uses, or split it across panels",
			total, maxSeriesPerQuery)
	}

	models := make([]QueryModel, 0, total)
	for _, agent := range agents {
		for _, device := range devices {
			for _, variable := range variables {
				qm := QueryModel{
					Scope:      rm.Scope,
					AgentID:    ID(agent),
					DeviceID:   ID(device),
					VariableID: ID(variable),
				}
				if err := qm.Validate(); err != nil {
					return nil, err
				}
				models = append(models, qm)
			}
		}
	}
	return models, nil
}

// ParseQueryModel decodes and validates a query that resolves to a single
// series.
func ParseQueryModel(raw json.RawMessage) (QueryModel, error) {
	models, err := ExpandQueryModel(raw)
	if err != nil {
		return QueryModel{}, err
	}
	if len(models) != 1 {
		return QueryModel{}, fmt.Errorf("expected a single series, got %d", len(models))
	}
	return models[0], nil
}

// Validate reports whether the query is complete enough to execute.
//
// An incomplete query is not an error condition to surface in the panel: a
// half-filled editor is the normal state while the user is still choosing.
// The frontend suppresses those via filterQuery, so anything reaching here and
// failing validation is a genuine problem worth reporting.
func (q QueryModel) Validate() error {
	switch q.Scope {
	case ScopeCollector, ScopeDevice:
	case "":
		return fmt.Errorf("scope is required")
	default:
		return fmt.Errorf("unknown scope %q", q.Scope)
	}
	if q.AgentID <= 0 {
		return fmt.Errorf("a collector must be selected")
	}
	if q.Scope == ScopeDevice && q.DeviceID <= 0 {
		return fmt.Errorf("a device must be selected")
	}
	if q.VariableID <= 0 {
		return fmt.Errorf("a variable must be selected")
	}
	return nil
}

// IsComplete reports whether every field needed to run the query is present.
func (q QueryModel) IsComplete() bool { return q.Validate() == nil }

// effectiveDeviceID is 0 for collector-scoped queries, which selects the
// collector-level history endpoint.
func (q QueryModel) effectiveDeviceID() int64 {
	if q.Scope == ScopeCollector {
		return 0
	}
	return q.DeviceID.Int64()
}

// SeriesContext carries the metadata needed to name and annotate a frame. It
// is resolved fresh on every query rather than read from the saved model.
type SeriesContext struct {
	CollectorName string
	DeviceName    string
	Variable      domotz.Variable
}

// BuildFrame turns a variable's history into a Grafana data frame.
//
// Field typing is decided for the series as a whole: if every sample parses as
// a float the value field is numeric, otherwise every sample is kept as a
// string. Choosing per-sample - as the previous implementation did - produced
// a value column shorter than the time column as soon as one sample in an
// otherwise numeric series failed to parse, and Grafana rejects frames whose
// fields differ in length.
func BuildFrame(refID string, samples []domotz.HistorySample, sc SeriesContext, timeRange TimeRange) *data.Frame {
	points := normalise(samples)

	// A variable without history has no samples to plot. Its current value is
	// still meaningful, so emit it as a single point when the reading falls
	// inside the panel's window. The value is the one just fetched from the
	// API, so it tracks reality instead of the authoring-time snapshot.
	if len(points) == 0 && !sc.Variable.HasHistory {
		if p, ok := currentValuePoint(sc.Variable, timeRange); ok {
			points = append(points, p)
		}
	}

	numeric := allNumeric(points)

	timestamps := make([]time.Time, len(points))
	for i, p := range points {
		timestamps[i] = p.at
	}

	timeField := data.NewField("time", nil, timestamps)
	valueField := buildValueField(points, numeric, sc)

	frame := data.NewFrame("", timeField, valueField)
	frame.RefID = refID
	frame.Meta = &data.FrameMeta{
		Type:        data.FrameTypeTimeSeriesMulti,
		TypeVersion: data.FrameTypeVersion{0, 1},
	}
	return frame
}

// buildValueField constructs the value column with its labels and display
// configuration. Labels are what let a panel override target a single device
// via ${__field.labels.device} - impossible when the identity of a series is
// concatenated into one long field name.
func buildValueField(points []point, numeric bool, sc SeriesContext) *data.Field {
	labels := data.Labels{
		"collector": sc.CollectorName,
		"variable":  sc.Variable.DisplayLabel(),
	}
	if sc.DeviceName != "" {
		labels["device"] = sc.DeviceName
	}
	if sc.Variable.Unit != "" {
		labels["unit"] = sc.Variable.Unit
	}

	name := sc.Variable.DisplayLabel()

	var field *data.Field
	if numeric {
		values := make([]float64, len(points))
		for i, p := range points {
			values[i] = p.number
		}
		field = data.NewField(name, labels, values)
	} else {
		values := make([]string, len(points))
		for i, p := range points {
			values[i] = p.text
		}
		field = data.NewField(name, labels, values)
	}

	config := &data.FieldConfig{DisplayNameFromDS: displayName(sc)}
	if unit, ok := grafanaUnit(sc.Variable.Unit); ok {
		config.Unit = unit
	}
	field.Config = config

	return field
}

// displayName is the human-readable series name shown in legends.
func displayName(sc SeriesContext) string {
	name := sc.CollectorName
	if sc.DeviceName != "" {
		name += " - " + sc.DeviceName
	}
	return name + ": " + sc.Variable.DisplayLabel()
}

// point is one sample with both representations retained, so the series-wide
// type decision can be made after every sample has been seen.
type point struct {
	at     time.Time
	text   string
	number float64
	isNum  bool
}

// normalise drops samples the API could not have meant (zero timestamps),
// sorts by time and removes duplicate timestamps, which Grafana's time series
// panels require to be strictly increasing.
func normalise(samples []domotz.HistorySample) []point {
	points := make([]point, 0, len(samples))
	for _, s := range samples {
		if s.Timestamp.IsZero() {
			continue
		}
		p := point{at: s.Timestamp.UTC(), text: s.Value}
		if n, err := strconv.ParseFloat(s.Value, 64); err == nil {
			p.number, p.isNum = n, true
		}
		points = append(points, p)
	}

	sort.SliceStable(points, func(i, j int) bool { return points[i].at.Before(points[j].at) })

	deduped := points[:0]
	var last time.Time
	for _, p := range points {
		if !last.IsZero() && p.at.Equal(last) {
			// Keep the newest reading for a repeated timestamp.
			deduped[len(deduped)-1] = p
			continue
		}
		deduped = append(deduped, p)
		last = p.at
	}
	return deduped
}

// allNumeric reports whether every sample parsed as a float. An empty series
// is treated as numeric so that "no data" panels keep a numeric axis.
func allNumeric(points []point) bool {
	for _, p := range points {
		if !p.isNum {
			return false
		}
	}
	return true
}

// currentValuePoint turns a variable's live reading into a single sample when
// its update time falls inside the queried window.
func currentValuePoint(v domotz.Variable, tr TimeRange) (point, bool) {
	if v.ValueUpdateTime == nil || v.ValueUpdateTime.IsZero() || v.Value == "" {
		return point{}, false
	}
	at := v.ValueUpdateTime.UTC()
	if at.Before(tr.From) || at.After(tr.To) {
		return point{}, false
	}

	p := point{at: at, text: v.Value}
	if n, err := strconv.ParseFloat(v.Value, 64); err == nil {
		p.number, p.isNum = n, true
	}
	return p, true
}

// TimeRange is the panel's queried window.
type TimeRange struct {
	From time.Time
	To   time.Time
}
