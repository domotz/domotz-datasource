package plugin

import "strings"

// Domotz unit strings mapped onto Grafana unit identifiers, so panels render
// "12.4 MB/s" instead of a bare number with the unit smuggled into the series
// name.
//
// Matching is CASE SENSITIVE first, and deliberately so: Domotz distinguishes
// "B/s" (bytes per second, e.g. interface traffic) from "b/s" (bits per
// second, e.g. link speed), and both occur in comparable numbers on a real
// account. Folding case would silently mislabel one of them by a factor of 8.
//
// The vocabulary below is the full set observed across a live EU account;
// anything outside it is left unmapped rather than guessed at.
var exactUnits = map[string]string{
	"%": "percent",

	"B":  "bytes",  // bytes (IEC)
	"kB": "kbytes", // kibibytes
	"MB": "mbytes",
	"GB": "gbytes",

	"B/s": "Bps", // bytes/sec (SI)
	"b/s": "bps", // bits/sec  (SI)

	"ms":     "ms",
	"s":      "s",
	"second": "s",
	"day":    "d",

	"C": "celsius",
	"F": "fahrenheit",

	"V":  "volt",
	"A":  "amp",
	"W":  "watt",
	"Hz": "hertz",
}

// caseInsensitiveUnits covers spellings where case carries no meaning, applied
// only after an exact match fails. "B"/"b" are excluded on purpose - see above.
var caseInsensitiveUnits = map[string]string{
	"percent":    "percent",
	"celsius":    "celsius",
	"fahrenheit": "fahrenheit",
	"volt":       "volt",
	"watt":       "watt",
	"hertz":      "hertz",
	"seconds":    "s",
	"days":       "d",
	"minute":     "m",
	"minutes":    "m",
	"hour":       "h",
	"hours":      "h",
	"dbm":        "dBm",
}

// grafanaUnit resolves a Domotz unit string to a Grafana unit identifier.
// The second return value reports whether a mapping was found.
//
// Unmapped units - "req/h" is the commonest, and Grafana has no per-hour rate
// unit - are reported as not found so the caller can leave the field config
// alone. Grafana renders an unrecognised identifier literally, which reads as
// a bug; the raw unit is kept as a frame label instead.
func grafanaUnit(domotzUnit string) (string, bool) {
	trimmed := strings.TrimSpace(domotzUnit)
	if trimmed == "" {
		return "", false
	}
	if unit, ok := exactUnits[trimmed]; ok {
		return unit, true
	}
	unit, ok := caseInsensitiveUnits[strings.ToLower(trimmed)]
	return unit, ok
}
