package domotz

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeAPI is a stand-in for the Domotz Public API. It records how many
// requests it received so cache behaviour can be asserted directly, which
// matters because every avoided request is quota the customer keeps.
type fakeAPI struct {
	*httptest.Server
	requests atomic.Int64
	handler  func(w http.ResponseWriter, r *http.Request)
}

func newFakeAPI(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) *fakeAPI {
	t.Helper()
	api := &fakeAPI{handler: handler}
	api.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.requests.Add(1)
		api.handler(w, r)
	}))
	t.Cleanup(api.Close)
	return api
}

func (f *fakeAPI) client() *Client {
	return NewClient(f.URL, "test-key", f.Server.Client())
}

// listHandler serves body as the single (short) page of a list endpoint.
func listHandler(t *testing.T, body string) func(http.ResponseWriter, *http.Request) {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			// Mirrors GET /agent/{id}/device, which rejects HEAD.
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}
}

func TestClient_SendsAPIKeyHeader(t *testing.T) {
	var gotKey string
	api := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-api-key")
		_, _ = w.Write([]byte(`{}`))
	})

	require.NoError(t, api.client().TestConnection(context.Background()))
	require.Equal(t, "test-key", gotKey, "the key must travel in the header, never the URL")
}

func TestClient_TestConnectionSurfacesUpstreamStatus(t *testing.T) {
	api := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"Invalid API Key"}`, http.StatusUnauthorized)
	})

	err := api.client().TestConnection(context.Background())

	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, http.StatusUnauthorized, apiErr.StatusCode,
		"a 401 must stay a 401 so the config page can say the key is wrong")
	require.Contains(t, apiErr.Body, "Invalid API Key")
}

func TestClient_RespectsContextCancellation(t *testing.T) {
	release := make(chan struct{})
	api := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		<-release
		_, _ = w.Write([]byte(`{}`))
	})
	t.Cleanup(func() { close(release) })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := api.client().TestConnection(ctx)
	require.ErrorIs(t, err, context.Canceled,
		"an abandoned dashboard refresh must stop spending API quota")
}

func TestClient_AgentsReturnsEveryCollector(t *testing.T) {
	// The licence block the API returns carries no expiry field, so there is
	// no notion of an "expired" collector to filter out here.
	body := `[
		{"id":1,"display_name":"Home","status":{"value":"ONLINE"}},
		{"id":2,"display_name":"Branch Office","status":{"value":"OFFLINE"}}
	]`
	api := newFakeAPI(t, listHandler(t, body))

	agents, err := api.client().Agents(context.Background())
	require.NoError(t, err)

	require.Len(t, agents, 2)
	require.Equal(t, "Home", agents[0].DisplayName)
	require.Equal(t, "ONLINE", agents[0].Status.Value)
	require.Equal(t, "OFFLINE", agents[1].Status.Value)
}

func TestClient_AgentsToleratesRealLicencePayload(t *testing.T) {
	// Shape mirrors what the EU cell returns - every field the licence block
	// carries, and no expiration_time anywhere. The values are synthetic: a real
	// licence code and the MAC it is bound to have no business in a public repo.
	body := `[{"id":4242,"display_name":"Home","status":{"value":"ONLINE",
		"last_change":"2026-08-03T19:21:28+00:00"},
		"licence":{"id":191,"code":"0000-0000","bound_mac_address":"00:00:5E:00:53:00",
		"activation_time":"2016-08-17T16:30:35+00:00","type":"MONTHLY"}}]`
	api := newFakeAPI(t, listHandler(t, body))

	agents, err := api.client().Agents(context.Background())

	require.NoError(t, err)
	require.Len(t, agents, 1)
	require.Equal(t, int64(4242), agents[0].ID)
}

func TestClient_CachesMetadataAcrossCalls(t *testing.T) {
	api := newFakeAPI(t, listHandler(t, `[{"id":1,"display_name":"HQ"}]`))
	client := api.client()

	for i := 0; i < 5; i++ {
		_, err := client.Agents(context.Background())
		require.NoError(t, err)
	}

	require.Equal(t, int64(1), api.requests.Load(),
		"a single GET should serve every repeat within the TTL")
}

func TestClient_InvalidateCacheForcesRefetch(t *testing.T) {
	api := newFakeAPI(t, listHandler(t, `[{"id":1,"display_name":"HQ"}]`))
	client := api.client()

	_, err := client.Agents(context.Background())
	require.NoError(t, err)
	client.InvalidateCache()
	_, err = client.Agents(context.Background())
	require.NoError(t, err)

	require.Equal(t, int64(2), api.requests.Load())
}

func TestClient_AsksUpstreamToFilterHistoryCapableVariables(t *testing.T) {
	var gotQuery string
	api := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("has_history")
		_, _ = w.Write([]byte(`[{"id":9,"label":"Uptime","has_history":true}]`))
	})

	_, err := api.client().DeviceVariables(context.Background(), 1, 2, true)
	require.NoError(t, err)
	require.Equal(t, "true", gotQuery,
		"filtering upstream avoids paging through variables that get discarded")
}

func TestClient_EmptyListCostsOneRequest(t *testing.T) {
	api := newFakeAPI(t, listHandler(t, `[]`))

	agents, err := api.client().Agents(context.Background())

	require.NoError(t, err)
	require.Empty(t, agents)
	require.Equal(t, int64(1), api.requests.Load())
}

// The variable endpoints are genuinely paginated, and page_number is honoured.
func TestClient_PaginatesUntilShortPage(t *testing.T) {
	api := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		size := defaultPageSize
		if r.URL.Query().Get("page_number") == "1" {
			size = 195
		}
		vars := make([]Variable, size)
		for i := range vars {
			vars[i] = Variable{ID: int64(i), Label: "v"}
		}
		_ = json.NewEncoder(w).Encode(vars)
	})

	vars, err := api.client().AgentDeviceVariables(context.Background(), 1, true)

	require.NoError(t, err)
	require.Len(t, vars, 695)
	require.Equal(t, int64(2), api.requests.Load(), "two pages, no HEAD probe")
}

// GET /agent/{id}/device answers HEAD with 405 and ignores page_size and
// page_number: every page returns the same devices. Treating it as paginated
// would loop over duplicates, so it must be fetched exactly once.
func TestClient_DeviceListIsFetchedUnpaginated(t *testing.T) {
	var methods []string
	var sawPagingParams bool
	api := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		if r.URL.Query().Has("page_number") || r.URL.Query().Has("page_size") {
			sawPagingParams = true
		}
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		_, _ = w.Write([]byte(`[{"id":1,"display_name":"Core Switch"}]`))
	})

	devices, err := api.client().Devices(context.Background(), 1)

	require.NoError(t, err)
	require.Len(t, devices, 1)
	require.Equal(t, []string{http.MethodGet}, methods, "no HEAD: the endpoint returns 405")
	require.False(t, sawPagingParams, "paging params are ignored by this endpoint")
}

func TestClient_VariableHistoryUsesDeviceEndpointWhenDeviceIsSet(t *testing.T) {
	var gotPath string
	api := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`[]`))
	})

	from := time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)

	_, err := api.client().VariableHistory(context.Background(), 1, 2, 3, from, to)
	require.NoError(t, err)
	require.Equal(t, "/agent/1/device/2/variable/3/history", gotPath)
}

func TestClient_VariableHistoryUsesCollectorEndpointWhenDeviceIsZero(t *testing.T) {
	var gotPath, gotFrom, gotTo string
	api := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotFrom = r.URL.Query().Get("from")
		gotTo = r.URL.Query().Get("to")
		_, _ = w.Write([]byte(`[]`))
	})

	from := time.Date(2026, 8, 4, 9, 30, 0, 0, time.UTC)
	to := time.Date(2026, 8, 4, 10, 30, 0, 0, time.UTC)

	_, err := api.client().VariableHistory(context.Background(), 1, 0, 3, from, to)
	require.NoError(t, err)
	require.Equal(t, "/agent/1/variable/3/history", gotPath)
	require.Equal(t, "2026-08-04T09:30:00", gotFrom)
	require.Equal(t, "2026-08-04T10:30:00", gotTo)
}

func TestClient_VariableHistoryParsesBothTimestampLayouts(t *testing.T) {
	api := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"timestamp":"2026-08-04T10:00:00Z","value":"1"},
			{"timestamp":"2026-08-04T11:00:00","value":"2"}
		]`))
	})

	samples, err := api.client().VariableHistory(context.Background(), 1, 2, 3, time.Now(), time.Now())

	require.NoError(t, err)
	require.Len(t, samples, 2)
	require.Equal(t, 10, samples[0].Timestamp.Hour())
	require.Equal(t, 11, samples[1].Timestamp.Hour())
	require.Equal(t, time.UTC, samples[1].Timestamp.Location())
}

func TestClient_BaseURLWithoutTrailingSlashStillWorks(t *testing.T) {
	var gotPath string
	api := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{}`))
	})

	client := NewClient(api.URL, "k", api.Server.Client()) // no trailing slash
	require.NoError(t, client.TestConnection(context.Background()))
	require.Equal(t, "/user", gotPath)
}

func TestClient_UsageDecodesQuota(t *testing.T) {
	api := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"daily_usage":120,"daily_limit":1000}`))
	})

	usage, err := api.client().Usage(context.Background())

	require.NoError(t, err)
	require.Equal(t, int64(120), usage.DailyUsage)
	require.Equal(t, int64(1000), usage.DailyLimit)
}

func TestTTLCache_ExpiresEntries(t *testing.T) {
	cache := newTTLCache(time.Minute)
	now := time.Now()
	cache.now = func() time.Time { return now }

	loads := 0
	load := func() (any, error) {
		loads++
		return "v", nil
	}

	got, err := cache.load("k", load)
	require.NoError(t, err)
	require.Equal(t, "v", got)

	_, err = cache.load("k", load)
	require.NoError(t, err)
	require.Equal(t, 1, loads, "a live entry should be served from the cache")

	now = now.Add(2 * time.Minute)
	_, err = cache.load("k", load)
	require.NoError(t, err)
	require.Equal(t, 2, loads, "an expired entry should be reloaded")
}

func TestTTLCache_DoesNotCacheFailures(t *testing.T) {
	cache := newTTLCache(time.Minute)

	_, err := cache.load("k", func() (any, error) { return nil, errors.New("429") })
	require.ErrorContains(t, err, "429")

	got, err := cache.load("k", func() (any, error) { return "v", nil })
	require.NoError(t, err)
	require.Equal(t, "v", got, "a failed load must not pin an error for the whole TTL")
}

// A cold dashboard asks every panel for the same lists at once. The API key
// allows five concurrent requests, so those duplicates have to collapse.
func TestTTLCache_LoadsOncePerKeyUnderConcurrency(t *testing.T) {
	cache := newTTLCache(time.Minute)

	var loads int32
	release := make(chan struct{})
	load := func() (any, error) {
		atomic.AddInt32(&loads, 1)
		<-release
		return "v", nil
	}

	var wg sync.WaitGroup
	results := make([]any, 8)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got, err := cache.load("k", load)
			require.NoError(t, err)
			results[i] = got
		}(i)
	}

	// Let the waiters pile up behind the single loader before it returns.
	require.Eventually(t, func() bool { return atomic.LoadInt32(&loads) == 1 }, time.Second, time.Millisecond)
	close(release)
	wg.Wait()

	require.Equal(t, int32(1), atomic.LoadInt32(&loads), "8 concurrent misses should issue 1 upstream call")
	for i, got := range results {
		require.Equal(t, "v", got, "waiter %d", i)
	}
}

func TestTTLCache_InvalidateDiscardsInFlightResult(t *testing.T) {
	cache := newTTLCache(time.Minute)

	started := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_, _ = cache.load("k", func() (any, error) {
			close(started)
			<-release
			return "stale", nil
		})
	}()

	<-started
	cache.invalidate()
	close(release)

	// The in-flight load began before the refresh, so its result must not be
	// installed afterwards: the whole point of the refresh button is that the
	// next read sees current data.
	require.Eventually(t, func() bool {
		_, ok := cache.get("k")
		return !ok
	}, time.Second, time.Millisecond)
}

func TestParseAPITime_RejectsGarbage(t *testing.T) {
	_, err := ParseAPITime("not a time")
	require.ErrorContains(t, err, "unrecognised timestamp")
}

// One malformed point used to abort decoding of the whole array, so a single bad
// timestamp lost every valid sample in the window.
func TestClient_HistoryKeepsValidSamplesAlongsideAMalformedOne(t *testing.T) {
	api := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"timestamp":"2026-08-04T10:00:00Z","value":"1"},
			{"timestamp":null,"value":"2"},
			{"timestamp":"","value":"3"},
			{"timestamp":"04/08/2026 10:10","value":"4"},
			{"timestamp":"2026-08-04T10:15:00Z","value":"5"}
		]`))
	})

	samples, err := api.client().VariableHistory(context.Background(), 7, 11, 99, time.Time{}, time.Time{})

	require.NoError(t, err, "a malformed sample must not fail the whole series")
	require.Len(t, samples, 5)

	var usable []HistorySample
	for _, s := range samples {
		if !s.Timestamp.IsZero() {
			usable = append(usable, s)
		}
	}
	require.Len(t, usable, 2, "the three unusable timestamps stay zero for normalise to drop")
	require.Equal(t, "1", usable[0].Value)
	require.Equal(t, "5", usable[1].Value)
}

// The API types every value as a string, but a numeric metric occasionally
// arrives unquoted. That is not worth failing a series over.
func TestClient_HistoryAcceptsUnquotedValues(t *testing.T) {
	api := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"timestamp":"2026-08-04T10:00:00Z","value":42.5},
			{"timestamp":"2026-08-04T10:05:00Z","value":true},
			{"timestamp":"2026-08-04T10:10:00Z","value":null}
		]`))
	})

	samples, err := api.client().VariableHistory(context.Background(), 7, 11, 99, time.Time{}, time.Time{})

	require.NoError(t, err)
	require.Equal(t, []string{"42.5", "true", ""},
		[]string{samples[0].Value, samples[1].Value, samples[2].Value})
}

// A variable list is served straight to the query editor, where JSON null
// crashes the pickers.
func TestClient_EmptyListsAreEmptySlicesNotNil(t *testing.T) {
	t.Run("paginated", func(t *testing.T) {
		api := newFakeAPI(t, listHandler(t, `[]`))

		agents, err := api.client().Agents(context.Background())

		require.NoError(t, err)
		require.NotNil(t, agents)
		require.Empty(t, agents)
	})

	t.Run("unpaginated body of null", func(t *testing.T) {
		api := newFakeAPI(t, listHandler(t, `null`))

		devices, err := api.client().Devices(context.Background(), 7)

		require.NoError(t, err)
		require.NotNil(t, devices)
		require.Empty(t, devices)
	})
}

// The key allows 5 concurrent requests, and the bound is per key rather than per
// dashboard refresh, so it has to live on the client.
func TestClient_BoundsConcurrentRequests(t *testing.T) {
	var (
		inFlight int32
		peak     int32
	)
	release := make(chan struct{})

	api := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		current := atomic.AddInt32(&inFlight, 1)
		for {
			observed := atomic.LoadInt32(&peak)
			if current <= observed || atomic.CompareAndSwapInt32(&peak, observed, current) {
				break
			}
		}
		<-release
		atomic.AddInt32(&inFlight, -1)
		_, _ = w.Write([]byte(`[]`))
	})
	client := api.client()

	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Distinct keys, so single-flight cannot collapse them and the
			// semaphore is what does the limiting.
			_, _ = client.Devices(context.Background(), int64(i))
		}(i)
	}

	require.Eventually(t, func() bool {
		return atomic.LoadInt32(&inFlight) == int32(maxConcurrentRequests)
	}, time.Second, time.Millisecond, "the client should saturate its budget")

	close(release)
	wg.Wait()

	require.LessOrEqual(t, atomic.LoadInt32(&peak), int32(maxConcurrentRequests),
		"12 concurrent lookups must never exceed the per-key budget")
}

func TestClient_HonoursCancellationWhileQueuedForASlot(t *testing.T) {
	release := make(chan struct{})

	api := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		<-release
		_, _ = w.Write([]byte(`[]`))
	})
	client := api.client()

	// Deferred rather than t.Cleanup: newFakeAPI registers the server's Close as
	// a cleanup, and Close waits for outstanding handlers. Cleanups run last-in
	// first-out, so a cleanup registered here would run after Close and the two
	// would deadlock.
	defer close(release)

	// Saturate the budget so the next call has to queue.
	for i := 0; i < maxConcurrentRequests; i++ {
		go func(i int) { _, _ = client.Devices(context.Background(), int64(i)) }(i)
	}
	require.Eventually(t, func() bool {
		return api.requests.Load() == int64(maxConcurrentRequests)
	}, time.Second, time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.Devices(ctx, 999)
	require.ErrorIs(t, err, context.Canceled)
}

// APITime is embedded in Variable, so a strict decoder would let one unreadable
// timestamp take out a whole collector's metric picker.
func TestVariable_UnreadableUpdateTimeDoesNotFailTheList(t *testing.T) {
	api := newFakeAPI(t, listHandler(t, `[
		{"id":1,"label":"Good","value_update_time":"2026-08-04T10:00:00Z"},
		{"id":2,"label":"Odd","value_update_time":"04/08/2026"},
		{"id":3,"label":"Missing","value_update_time":null}
	]`))

	variables, err := api.client().AgentVariables(context.Background(), 7, false)

	require.NoError(t, err)
	require.Len(t, variables, 3)

	require.NotNil(t, variables[0].ValueUpdateTime)
	require.False(t, variables[0].ValueUpdateTime.IsZero())

	require.NotNil(t, variables[1].ValueUpdateTime)
	require.True(t, variables[1].ValueUpdateTime.IsZero(), "an unreadable time is simply absent")

	// A JSON null leaves the pointer nil, which is why currentValuePoint checks
	// for nil before reading it.
	require.Nil(t, variables[2].ValueUpdateTime)
}

// Address fields drive search in the query editor, so they must survive
// decoding - including the shapes where the API omits them.
func TestClient_DevicesCarryAddresses(t *testing.T) {
	body := `[
		{"id":1,"display_name":"NAS","ip_addresses":["192.168.1.20"],"hw_address":"00:08:9B:CD:12:34"},
		{"id":2,"display_name":"Laptop","ip_addresses":["192.168.1.55"]},
		{"id":3,"display_name":"Hue Bulb"}
	]`
	api := newFakeAPI(t, listHandler(t, body))

	devices, err := api.client().Devices(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, devices, 3)

	require.Equal(t, []string{"192.168.1.20"}, devices[0].IPAddresses)
	require.Equal(t, "00:08:9B:CD:12:34", devices[0].HWAddress)
	require.Equal(t, "192.168.1.20", devices[0].PrimaryIP())

	// hw_address is only defined on LocalIpDevice; a device reached through a
	// third party has no MAC and must still decode cleanly.
	require.Empty(t, devices[1].HWAddress)
	require.Equal(t, "192.168.1.55", devices[1].PrimaryIP())

	require.Empty(t, devices[2].IPAddresses)
	require.Empty(t, devices[2].PrimaryIP(), "a device with no addresses must not panic")
}

func TestClient_DevicesWithMultipleAddressesReportTheFirst(t *testing.T) {
	api := newFakeAPI(t, listHandler(t, `[{"id":1,"ip_addresses":["10.0.0.1","10.0.0.2"]}]`))

	devices, err := api.client().Devices(context.Background(), 1)

	require.NoError(t, err)
	require.Equal(t, "10.0.0.1", devices[0].PrimaryIP())
}
