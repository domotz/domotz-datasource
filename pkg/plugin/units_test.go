package plugin

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The regression that matters: Domotz ships both "B/s" and "b/s" and they mean
// different things. Case folding would mislabel bandwidth by a factor of 8.
func TestGrafanaUnit_ByteAndBitRatesAreDistinct(t *testing.T) {
	bytesPerSec, ok := grafanaUnit("B/s")
	require.True(t, ok)
	require.Equal(t, "Bps", bytesPerSec, "B/s is bytes per second")

	bitsPerSec, ok := grafanaUnit("b/s")
	require.True(t, ok)
	require.Equal(t, "bps", bitsPerSec, "b/s is bits per second")

	require.NotEqual(t, bytesPerSec, bitsPerSec)
}

// Every unit string observed across a live EU account, so the table is checked
// against reality rather than invention.
func TestGrafanaUnit_CoversObservedVocabulary(t *testing.T) {
	observed := map[string]struct {
		want    string
		mapped  bool
		comment string
	}{
		"req/h":  {"", false, "Grafana has no per-hour rate unit"},
		"B":      {"bytes", true, ""},
		"%":      {"percent", true, ""},
		"B/s":    {"Bps", true, ""},
		"b/s":    {"bps", true, ""},
		"ms":     {"ms", true, ""},
		"C":      {"celsius", true, ""},
		"second": {"s", true, ""},
		"day":    {"d", true, ""},
		"GB":     {"gbytes", true, ""},
	}

	for unit, expect := range observed {
		t.Run(unit, func(t *testing.T) {
			got, ok := grafanaUnit(unit)
			require.Equal(t, expect.mapped, ok, expect.comment)
			require.Equal(t, expect.want, got)
		})
	}
}

func TestGrafanaUnit_UnknownUnitsAreNotGuessed(t *testing.T) {
	for _, unit := range []string{"widgets-per-fortnight", "req/h", "  ", ""} {
		got, ok := grafanaUnit(unit)
		require.False(t, ok, "unit %q must not be mapped", unit)
		require.Empty(t, got)
	}
}

func TestGrafanaUnit_TrimsSurroundingSpace(t *testing.T) {
	got, ok := grafanaUnit("  %  ")
	require.True(t, ok)
	require.Equal(t, "percent", got)
}

func TestGrafanaUnit_FallsBackToCaseInsensitiveForUnambiguousNames(t *testing.T) {
	got, ok := grafanaUnit("Celsius")
	require.True(t, ok)
	require.Equal(t, "celsius", got)

	// ...but never for the byte/bit pair.
	upper, _ := grafanaUnit("B")
	lower, _ := grafanaUnit("b")
	require.Equal(t, "bytes", upper)
	require.Empty(t, lower, "a bare lowercase b is ambiguous and must stay unmapped")
}
