package domotz_test

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/domotz/domotz-datasource/pkg/domotz"
	"github.com/stretchr/testify/require"
)

// These tests run against the real Domotz Public API and are skipped unless a
// key is supplied:
//
//	DOMOTZ_TEST_API_KEY=... go test ./pkg/domotz/ -run TestLive -v
//
// They exist because the unit tests can only prove the client is consistent
// with our *assumptions* about the API. Two of those assumptions turned out to
// be wrong - a licence expiry field that does not exist, and a device endpoint
// that rejects HEAD and ignores pagination - and only real traffic caught them.
func liveClient(t *testing.T) *domotz.Client {
	t.Helper()

	key := os.Getenv("DOMOTZ_TEST_API_KEY")
	if key == "" {
		t.Skip("set DOMOTZ_TEST_API_KEY to run live API tests")
	}

	baseURL := os.Getenv("DOMOTZ_TEST_API_URL")
	if baseURL == "" {
		baseURL = "https://api-eu-west-1-cell-1.domotz.com/public-api/v1/"
	}

	return domotz.NewClient(baseURL, key, &http.Client{Timeout: 30 * time.Second})
}

func TestLive_Connection(t *testing.T) {
	require.NoError(t, liveClient(t).TestConnection(context.Background()))
}

func TestLive_Usage(t *testing.T) {
	usage, err := liveClient(t).Usage(context.Background())

	require.NoError(t, err)
	require.Positive(t, usage.DailyLimit, "the quota endpoint must report a limit")
	t.Logf("daily usage %d / %d", usage.DailyUsage, usage.DailyLimit)
}

func TestLive_AgentsPaginate(t *testing.T) {
	agents, err := liveClient(t).Agents(context.Background())

	require.NoError(t, err)
	require.NotEmpty(t, agents)

	seen := map[int64]bool{}
	for _, a := range agents {
		require.NotZero(t, a.ID)
		require.False(t, seen[a.ID], "pagination must not repeat collector %d", a.ID)
		seen[a.ID] = true
	}
	t.Logf("%d collectors", len(agents))
}

// Regression guard for the endpoint that rejects HEAD and ignores paging.
func TestLive_DevicesFetchWithoutHeadOrPaging(t *testing.T) {
	client := liveClient(t)
	ctx := context.Background()

	agents, err := client.Agents(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, agents)

	devices, err := client.Devices(ctx, agents[0].ID)
	require.NoError(t, err, "GET /agent/{id}/device must not be treated as paginated")

	seen := map[int64]bool{}
	for _, d := range devices {
		require.False(t, seen[d.ID], "device %d returned twice - paging is being misapplied", d.ID)
		seen[d.ID] = true
	}
	t.Logf("collector %d has %d devices", agents[0].ID, len(devices))
}

func TestLive_BulkDeviceVariablesCarryDeviceID(t *testing.T) {
	client := liveClient(t)
	ctx := context.Background()

	agent := busiestAgent(t, client)

	vars, err := client.AgentDeviceVariables(ctx, agent, true)
	require.NoError(t, err)
	require.NotEmpty(t, vars, "expected history-capable device variables")

	for _, v := range vars {
		require.NotZero(t, v.DeviceID, "the bulk endpoint must tag every variable with its device")
		require.True(t, v.HasHistory, "has_history=true must be honoured upstream")
	}
	t.Logf("collector %d exposes %d history-capable device variables", agent, len(vars))
}

// Real collectors expose plenty of history-backed variables whose values are
// words ("DOWN", "Linux ...", "5.8 %", "39 C/102 F").
//
// Note on severity: a sweep of 199 live variables found no series that mixes
// numeric and non-numeric samples - real variables are consistently typed, and
// a uniformly-string series was handled correctly by the old code too. The
// misalignment defect BuildFrame fixes is therefore latent rather than
// actively firing on this account: it needs a variable that changes type
// mid-series (an OID returning "0" then "N/A", say). Worth fixing, but it is
// not the reason existing dashboards misbehave.
func TestLive_HistoryContainsNonNumericValues(t *testing.T) {
	client := liveClient(t)
	ctx := context.Background()

	vars, err := client.AgentDeviceVariables(ctx, busiestAgent(t, client), true)
	require.NoError(t, err)

	var nonNumeric int
	for _, v := range vars {
		if v.Value == "" {
			continue
		}
		if _, err := strconv.ParseFloat(v.Value, 64); err != nil {
			nonNumeric++
		}
	}
	require.Positive(t, nonNumeric,
		"real data must contain string-valued history variables, else the frame typing fix is untested")
	t.Logf("%d of %d history variables hold non-numeric values", nonNumeric, len(vars))
}

func TestLive_VariableHistoryDecodes(t *testing.T) {
	client := liveClient(t)
	ctx := context.Background()

	agent := busiestAgent(t, client)
	vars, err := client.AgentDeviceVariables(ctx, agent, true)
	require.NoError(t, err)
	require.NotEmpty(t, vars)

	to := time.Now().UTC()
	from := to.Add(-24 * time.Hour)

	// Walk until a variable actually returns samples in the window.
	var samples []domotz.HistorySample
	var chosen domotz.Variable
	for _, v := range vars {
		got, err := client.VariableHistory(ctx, agent, v.DeviceID, v.ID, from, to)
		require.NoError(t, err, "history request failed for variable %d", v.ID)
		if len(got) > 0 {
			samples, chosen = got, v
			break
		}
	}
	require.NotEmpty(t, samples, "no variable returned history in the last 24h")

	for _, s := range samples {
		require.False(t, s.Timestamp.IsZero(), "every sample timestamp must parse")
		require.False(t, s.Timestamp.After(to.Add(time.Minute)), "sample outside requested window")
	}
	t.Logf("variable %d (%s) returned %d samples", chosen.ID, chosen.DisplayLabel(), len(samples))
}

func TestLive_CollectorVariablesResolve(t *testing.T) {
	client := liveClient(t)
	ctx := context.Background()

	agents, err := client.Agents(ctx)
	require.NoError(t, err)

	for _, a := range agents {
		vars, err := client.AgentVariables(ctx, a.ID, false)
		require.NoError(t, err)
		if len(vars) == 0 {
			continue
		}
		for _, v := range vars {
			require.NotEmpty(t, v.DisplayLabel(), "every variable must resolve to a label")
		}
		t.Logf("collector %d exposes %d collector-level variables", a.ID, len(vars))
		return
	}
	t.Skip("no collector exposed collector-level variables")
}

// busiestAgent picks a collector that actually has device variables, so the
// tests do not silently pass against an empty account.
func busiestAgent(t *testing.T, client *domotz.Client) int64 {
	t.Helper()
	ctx := context.Background()

	agents, err := client.Agents(ctx)
	require.NoError(t, err)

	for _, a := range agents {
		vars, err := client.AgentDeviceVariables(ctx, a.ID, true)
		require.NoError(t, err)
		if len(vars) > 0 {
			return a.ID
		}
	}
	t.Skip("no collector on this account exposes history-capable device variables")
	return 0
}

// Pagination terminates on a short page, which is correct only while the API
// honours page_size exactly. It does today - verified up to 1000, with 5000
// rejected outright - but a server that silently clamped page_size below our
// page size would truncate every list with no error at all.
//
// Nothing else would notice: the other live tests assert "not empty", never a
// count. This compares the paged result against the upstream X-Entities-Count,
// which is an independent oracle for exactly that failure.
func TestLive_PagedFetchIsNotSilentlyTruncated(t *testing.T) {
	client := liveClient(t)
	ctx := context.Background()

	agent := busiestAgent(t, client)

	vars, err := client.AgentDeviceVariables(ctx, agent, false)
	require.NoError(t, err)
	require.NotEmpty(t, vars)

	want := upstreamCount(t, fmt.Sprintf("agent/%d/device/variable", agent), nil)
	require.Equal(t, want, len(vars),
		"paged fetch returned %d of %d device variables - pagination stopped early",
		len(vars), want)

	// Same check on a filtered listing, where the count differs from the total.
	history, err := client.AgentDeviceVariables(ctx, agent, true)
	require.NoError(t, err)

	wantHistory := upstreamCount(t, fmt.Sprintf("agent/%d/device/variable", agent),
		url.Values{"has_history": []string{"true"}})
	require.Equal(t, wantHistory, len(history))
	require.Less(t, wantHistory, want, "the filter should exclude something, else this proves little")

	t.Logf("paged fetch matched upstream: %d total, %d with history", want, wantHistory)
}

// upstreamCount reads X-Entities-Count via HEAD, independently of the client's
// own pagination logic.
func upstreamCount(t *testing.T, path string, query url.Values) int {
	t.Helper()

	base := os.Getenv("DOMOTZ_TEST_API_URL")
	if base == "" {
		base = "https://api-eu-west-1-cell-1.domotz.com/public-api/v1/"
	}
	target := strings.TrimSuffix(base, "/") + "/" + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	req, err := http.NewRequest(http.MethodHead, target, nil)
	require.NoError(t, err)
	req.Header.Set("x-api-key", os.Getenv("DOMOTZ_TEST_API_KEY"))

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	n, err := strconv.Atoi(resp.Header.Get("X-Entities-Count"))
	require.NoError(t, err)
	return n
}
