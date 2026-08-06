package plugin

import (
	"strconv"
	"testing"
	"time"

	"github.com/domotz/domotz-datasource/pkg/domotz"
	"github.com/grafana/grafana-plugin-sdk-go/data"
	"github.com/stretchr/testify/require"
)

// TestLegacyFrameBuilderProducedMisalignedFields reproduces the frame-building
// logic of the pre-rewrite datasource.go to confirm the defect the new
// BuildFrame fixes is real, and that Grafana would in fact have rejected the
// result. Without this, TestBuildFrame_MixedNumericAndStringSamplesStayAligned
// only asserts that correct code is correct.
//
// Scope: this needs a series that mixes numeric and non-numeric samples. A
// sweep of 199 live variables on a real EU account found none - variables are
// consistently typed in practice - so this is a latent robustness defect, not
// one that fires on today's data. A uniformly-string series took the old
// len(values)==0 branch and came out correctly aligned.
func TestLegacyFrameBuilderProducedMisalignedFields(t *testing.T) {
	history := []struct{ Timestamp, Value string }{
		{"2026-08-04T10:00:00Z", "1"},
		{"2026-08-04T10:01:00Z", "UP"}, // not parseable as a float
		{"2026-08-04T10:02:00Z", "0"},
	}

	// --- verbatim shape of the old loop -----------------------------------
	var timestamps []time.Time
	var values []float64
	var stringValues []string

	for _, dp := range history {
		parsedTime, err := time.Parse(time.RFC3339, dp.Timestamp)
		if err != nil {
			continue
		}
		timestamps = append(timestamps, parsedTime)

		num, parseErr := strconv.ParseFloat(dp.Value, 64)
		if parseErr != nil {
			stringValues = append(stringValues, dp.Value)
			continue
		}
		values = append(values, num)
	}

	frame := data.NewFrame("response")
	if len(values) == 0 {
		frame.Fields = append(frame.Fields,
			data.NewField("time", nil, timestamps),
			data.NewField("series", nil, stringValues),
		)
	} else {
		frame.Fields = append(frame.Fields,
			data.NewField("time", nil, timestamps),
			data.NewField("series", nil, values),
		)
	}
	// ----------------------------------------------------------------------

	require.Equal(t, 3, frame.Fields[0].Len(), "three timestamps were collected")
	require.Equal(t, 2, frame.Fields[1].Len(), "but only two values survived")

	// This is what makes it a user-visible failure and not just untidiness.
	_, err := frame.RowLen()
	require.Error(t, err, "Grafana rejects a frame whose fields differ in length")

	// The rewritten builder handles the same input without losing a row.
	fixed := BuildFrame("A", []domotz.HistorySample{
		sample("2026-08-04T10:00:00Z", "1"),
		sample("2026-08-04T10:01:00Z", "UP"),
		sample("2026-08-04T10:02:00Z", "0"),
	}, numericContext(), testRange())

	rows, err := fixed.RowLen()
	require.NoError(t, err)
	require.Equal(t, 3, rows)
}
