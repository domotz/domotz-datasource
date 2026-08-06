// Package domotz is a typed client for the Domotz Public API v1.
//
// Only the subset of the API the data source needs is modelled here: collectors
// ("agents" in the API), devices, variables and variable history.
package domotz

import (
	"encoding/json"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Agent is a Domotz collector. Customer-facing copy calls these "collectors";
// the API path segment is still /agent.
//
// The licence block the API returns (id, code, bound_mac_address,
// activation_time, type) carries no expiry, so there is nothing to filter
// collectors on: an unlicensed collector simply stops reporting.
type Agent struct {
	ID          int64  `json:"id"`
	DisplayName string `json:"display_name"`
	Status      Status `json:"status"`
}

// Status is a collector's reachability, shown in the collector picker so an
// offline site is obvious before its panels come back empty.
type Status struct {
	Value      string `json:"value"`
	LastChange string `json:"last_change"`
}

// APITime tolerates the several timestamp layouts the Domotz API emits. Using
// a plain time.Time would make the whole enclosing object fail to decode when
// a timestamp arrives without a zone offset.
type APITime struct{ time.Time }

// UnmarshalJSON never fails on the timestamp itself.
//
// APITime is embedded in Variable, so returning an error here fails the whole
// enclosing list: one variable carrying an unparseable value_update_time would
// take out an entire collector's metric picker. An unreadable timestamp is
// simply absent, and callers already treat the zero value as "no reading".
func (t *APITime) UnmarshalJSON(data []byte) error {
	t.Time = time.Time{}

	var raw *string
	if err := json.Unmarshal(data, &raw); err != nil || raw == nil || *raw == "" {
		return nil
	}
	if parsed, err := ParseAPITime(*raw); err == nil {
		t.Time = parsed
	}
	return nil
}

// Device is one device seen by a collector.
//
// IPAddresses and HWAddress let the query editor be searched by address as
// well as by name, which is how people actually look for a device they only
// know from a router table or a label on the box.
//
// Both are optional by contract, not just in practice: ip_addresses is defined
// on IpDevice, while hw_address is only defined on LocalIpDevice - a device
// reached through a third-party controller rather than the local network has
// no MAC to report.
type Device struct {
	ID          int64    `json:"id"`
	DisplayName string   `json:"display_name"`
	IPAddresses []string `json:"ip_addresses"`
	HWAddress   string   `json:"hw_address"`
}

// PrimaryIP returns the first address, or "" when the device reports none.
func (d Device) PrimaryIP() string {
	if len(d.IPAddresses) == 0 {
		return ""
	}
	return d.IPAddresses[0]
}

// Variable is a metric exposed by a collector or one of its devices.
//
// DeviceID is only populated by the collector-wide device variables endpoint
// (GET /agent/{id}/device/variable); it is zero for collector-level variables
// and for the per-device listing, where the device is implied by the URL.
type Variable struct {
	ID              int64    `json:"id"`
	DeviceID        int64    `json:"device_id"`
	Label           string   `json:"label"`
	Metric          string   `json:"metric"`
	Path            string   `json:"path"`
	Unit            string   `json:"unit"`
	HasHistory      bool     `json:"has_history"`
	Value           string   `json:"value"`
	ValueUpdateTime *APITime `json:"value_update_time"`
}

// DisplayLabel resolves the name to show for a variable, falling back through
// the same chain the original query editor used client-side: an explicit label
// wins, then a prettified metric, then the last segment of the path. Keeping
// this in the backend means the series name in a panel matches the name in the
// picker without the frontend having to persist it into the saved query.
func (v Variable) DisplayLabel() string {
	if v.Label != "" {
		return v.Label
	}
	if v.Metric != "" {
		return prettify(strings.Split(v.Metric, "_"))
	}
	if v.Path != "" {
		segments := strings.Split(v.Path, "/")
		last := segments[len(segments)-1]
		if last != "" {
			return prettify(strings.Split(last, "-"))
		}
	}
	return "value"
}

// prettify title-cases each word and joins them with spaces. Words are already
// split by the caller because the separator differs by source field.
//
// Title-casing works on the first rune, not the first byte: slicing w[:1] cuts
// a multi-byte character in half, so a metric or path segment beginning with a
// non-ASCII letter came out as a replacement byte followed by orphaned
// continuation bytes in both the legend and the metric picker.
func prettify(words []string) string {
	out := make([]string, 0, len(words))
	for _, w := range words {
		if w == "" {
			continue
		}
		first, size := utf8.DecodeRuneInString(w)
		if first == utf8.RuneError {
			// Not valid UTF-8 - leave it exactly as the API sent it rather
			// than corrupting it further.
			out = append(out, w)
			continue
		}
		out = append(out, string(unicode.ToUpper(first))+w[size:])
	}
	return strings.Join(out, " ")
}

// HistorySample is one point of a variable's history. The API types every
// value as a string regardless of the underlying metric, so numeric parsing is
// the client's responsibility.
type HistorySample struct {
	Timestamp time.Time `json:"timestamp"`
	Value     string    `json:"value"`
}

// Usage is the API key's daily quota, surfaced in the config page so operators
// can see how much of their allowance the dashboards consume.
type Usage struct {
	DailyUsage int64 `json:"daily_usage"`
	DailyLimit int64 `json:"daily_limit"`
}
