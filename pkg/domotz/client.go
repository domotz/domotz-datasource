package domotz

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// defaultPageSize is the page size used for paginated list endpoints.
	defaultPageSize = 500

	// maxPages bounds pagination so an endpoint that silently ignores
	// page_number cannot spin the plugin forever against a quota-limited API.
	maxPages = 200

	// metadataTTL is how long collector/device/variable metadata is reused.
	metadataTTL = 5 * time.Minute

	// maxConcurrentRequests bounds how many requests this client has in flight
	// at once. GET /meta/usage reports concurrent_allowed: 5, so the bound sits
	// one below it.
	//
	// It lives on the client rather than per QueryData call because the limit is
	// per API key: two dashboards refreshing at the same time share one budget,
	// and a bound scoped to a single request cannot see the other one. Guarding
	// do() rather than the callers means metadata lookups, history fetches and a
	// multi-series fan-out all draw on the same allowance.
	maxConcurrentRequests = 4
)

// APIError is a non-2xx response from the Domotz API. Status is preserved so
// callers can map it onto a Grafana error status rather than flattening
// everything to 400 (which is what the previous implementation did, turning
// upstream 401s and 429s into indistinguishable "bad request" panels).
type APIError struct {
	StatusCode int
	Body       string
	URL        string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("domotz API %s returned %d: %s", e.URL, e.StatusCode, e.Body)
}

// Client talks to one Domotz Public API cell.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
	cache   *ttlCache
	sem     chan struct{}
}

// NewClient builds a client for baseURL (for example
// https://api-eu-west-1-cell-1.domotz.com/public-api/v1/).
//
// httpClient should be the one provided by the Grafana plugin SDK so that
// proxy, TLS and timeout settings configured on the data source are honoured
// and outgoing requests are traced.
func NewClient(baseURL, apiKey string, httpClient *http.Client) *Client {
	return &Client{
		baseURL: strings.TrimSuffix(baseURL, "/") + "/",
		apiKey:  apiKey,
		http:    httpClient,
		cache:   newTTLCache(metadataTTL),
		sem:     make(chan struct{}, maxConcurrentRequests),
	}
}

// InvalidateCache drops cached metadata.
func (c *Client) InvalidateCache() { c.cache.invalidate() }

func (c *Client) url(path string, query url.Values) string {
	full := c.baseURL + strings.TrimPrefix(path, "/")
	if len(query) > 0 {
		full += "?" + query.Encode()
	}
	return full
}

// do issues a request and returns the body, honouring ctx cancellation so an
// abandoned dashboard refresh stops consuming API quota.
func (c *Client) do(ctx context.Context, method, rawURL string) ([]byte, http.Header, error) {
	// Wait for a slot in the per-key concurrency budget. Cancellation is
	// honoured here too: a dashboard the user has navigated away from should
	// stop queueing rather than eventually issue requests nobody reads.
	select {
	case c.sem <- struct{}{}:
		defer func() { <-c.sem }()
	case <-ctx.Done():
		return nil, nil, fmt.Errorf("%s %s: %w", method, rawURL, ctx.Err())
	}

	req, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("building %s %s: %w", method, rawURL, err)
	}
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("%s %s: %w", method, rawURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("reading response from %s: %w", rawURL, err)
	}

	// HEAD returns 204 with the count in a header; GET returns 200.
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return nil, nil, &APIError{StatusCode: resp.StatusCode, Body: string(body), URL: rawURL}
	}
	return body, resp.Header, nil
}

// getList fetches an endpoint that returns its whole collection in one
// response.
//
// Not every list endpoint is paginated: GET /agent/{id}/device ignores
// page_size and page_number outright (and answers HEAD with 405), so asking it
// for pages just returns the same devices repeatedly.
func getList[T any](ctx context.Context, c *Client, path string, query url.Values) ([]T, error) {
	body, _, err := c.do(ctx, http.MethodGet, c.url(path, query))
	if err != nil {
		return nil, err
	}

	items := make([]T, 0)
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, fmt.Errorf("decoding %s: %w", path, err)
	}
	if items == nil {
		// A literal `null` body decodes into a nil slice. Callers serialise
		// these straight to the query editor, where JSON null crashes the
		// pickers, so normalise to an empty list.
		items = make([]T, 0)
	}
	return items, nil
}

// getPaged walks every page of a paginated list endpoint.
//
// Pagination is driven by reading until a short page rather than by a HEAD
// count. HEAD is not supported across the board, and skipping it saves a
// request per lookup on an API with a daily quota.
func getPaged[T any](ctx context.Context, c *Client, path string, query url.Values) ([]T, error) {
	// Non-nil from the start: an account whose first page is empty would
	// otherwise yield a nil slice, which encodes as JSON null and crashes the
	// query editor's `list.map(...)`.
	all := make([]T, 0)

	for page := 0; page < maxPages; page++ {
		pageQuery := url.Values{}
		for k, v := range query {
			pageQuery[k] = v
		}
		pageQuery.Set("page_size", strconv.Itoa(defaultPageSize))
		pageQuery.Set("page_number", strconv.Itoa(page))

		body, _, err := c.do(ctx, http.MethodGet, c.url(path, pageQuery))
		if err != nil {
			return nil, err
		}

		var chunk []T
		if err := json.Unmarshal(body, &chunk); err != nil {
			return nil, fmt.Errorf("decoding %s page %d: %w", path, page, err)
		}
		all = append(all, chunk...)

		// A short page is the last page.
		if len(chunk) < defaultPageSize {
			return all, nil
		}
	}
	return all, nil
}

// cached memoises fn under key for metadataTTL, running at most one load per
// key at a time across concurrent callers.
func cached[T any](c *Client, key string, fn func() (T, error)) (T, error) {
	var zero T

	value, err := c.cache.load(key, func() (any, error) { return fn() })
	if err != nil {
		return zero, err
	}

	typed, ok := value.(T)
	if !ok {
		// Only reachable if two accessors shared a key with different types,
		// which is a programming error rather than an API condition.
		return zero, fmt.Errorf("cached value for %q is %T, not %T", key, value, zero)
	}
	return typed, nil
}

// TestConnection verifies the API key against the /user endpoint.
func (c *Client) TestConnection(ctx context.Context) error {
	_, _, err := c.do(ctx, http.MethodGet, c.url("user", nil))
	return err
}

// Usage returns the API key's daily quota consumption.
func (c *Client) Usage(ctx context.Context) (Usage, error) {
	var usage Usage
	body, _, err := c.do(ctx, http.MethodGet, c.url("meta/usage", nil))
	if err != nil {
		return usage, err
	}
	if err := json.Unmarshal(body, &usage); err != nil {
		return usage, fmt.Errorf("decoding usage: %w", err)
	}
	return usage, nil
}

// Agents returns the collectors reachable with this API key.
func (c *Client) Agents(ctx context.Context) ([]Agent, error) {
	return cached(c, "agents", func() ([]Agent, error) {
		return getPaged[Agent](ctx, c, "agent", nil)
	})
}

// Devices returns the devices of a collector.
//
// This endpoint is unpaginated by contract: device.py is the one list
// descriptor that does not use PaginationHelper.
func (c *Client) Devices(ctx context.Context, agentID int64) ([]Device, error) {
	key := fmt.Sprintf("devices:%d", agentID)
	return cached(c, key, func() ([]Device, error) {
		return getList[Device](ctx, c, fmt.Sprintf("agent/%d/device", agentID), nil)
	})
}

// AgentVariables returns a collector's own variables.
//
// historyOnly asks the API to return only variables that expose history. That
// filter is applied upstream rather than client-side, so pages that would be
// discarded are never fetched.
func (c *Client) AgentVariables(ctx context.Context, agentID int64, historyOnly bool) ([]Variable, error) {
	key := fmt.Sprintf("agentvars:%d:%t", agentID, historyOnly)
	return cached(c, key, func() ([]Variable, error) {
		return getPaged[Variable](ctx, c, fmt.Sprintf("agent/%d/variable", agentID), historyFilter(historyOnly))
	})
}

// AgentDeviceVariables returns every device variable of a collector in one
// paged sweep. Each entry carries its DeviceID.
//
// This is the endpoint that makes multi-series queries affordable: one call
// covers all devices, instead of one call per device.
func (c *Client) AgentDeviceVariables(ctx context.Context, agentID int64, historyOnly bool) ([]Variable, error) {
	key := fmt.Sprintf("agentdevicevars:%d:%t", agentID, historyOnly)
	return cached(c, key, func() ([]Variable, error) {
		return getPaged[Variable](ctx, c, fmt.Sprintf("agent/%d/device/variable", agentID), historyFilter(historyOnly))
	})
}

// DeviceVariables returns the variables of a single device.
func (c *Client) DeviceVariables(ctx context.Context, agentID, deviceID int64, historyOnly bool) ([]Variable, error) {
	key := fmt.Sprintf("devicevars:%d:%d:%t", agentID, deviceID, historyOnly)
	return cached(c, key, func() ([]Variable, error) {
		path := fmt.Sprintf("agent/%d/device/%d/variable", agentID, deviceID)
		return getPaged[Variable](ctx, c, path, historyFilter(historyOnly))
	})
}

func historyFilter(historyOnly bool) url.Values {
	if !historyOnly {
		return nil
	}
	return url.Values{"has_history": []string{"true"}}
}

// VariableHistory returns samples for a variable between from and to. A zero
// deviceID selects a collector-level variable.
//
// History is deliberately not cached: it is the time-varying part of a query
// and Grafana already caches at the panel level.
func (c *Client) VariableHistory(ctx context.Context, agentID, deviceID, variableID int64, from, to time.Time) ([]HistorySample, error) {
	var path string
	if deviceID > 0 {
		path = fmt.Sprintf("agent/%d/device/%d/variable/%d/history", agentID, deviceID, variableID)
	} else {
		path = fmt.Sprintf("agent/%d/variable/%d/history", agentID, variableID)
	}

	query := url.Values{
		"from": []string{from.UTC().Format(apiTimeFormat)},
		"to":   []string{to.UTC().Format(apiTimeFormat)},
	}

	body, _, err := c.do(ctx, http.MethodGet, c.url(path, query))
	if err != nil {
		return nil, err
	}

	var samples []HistorySample
	if err := json.Unmarshal(body, &samples); err != nil {
		return nil, fmt.Errorf("decoding history for variable %d: %w", variableID, err)
	}
	return samples, nil
}

// apiTimeFormat is the naive-UTC layout the history endpoint expects for its
// from/to bounds.
const apiTimeFormat = "2006-01-02T15:04:05"

// UnmarshalJSON accepts both RFC3339 timestamps and the naive form the API
// uses in some responses, and decodes a sample on a best-effort basis.
//
// Individual samples are tolerated rather than fatal: returning an error here
// aborts json.Unmarshal for the whole history array, so one malformed point
// used to discard every valid sample in the window and render the panel as an
// error. A sample whose timestamp cannot be parsed keeps a zero Timestamp,
// which normalise drops - the guard it already had for exactly this case, and
// which the old decoder made unreachable.
func (h *HistorySample) UnmarshalJSON(data []byte) error {
	var raw struct {
		Timestamp json.RawMessage `json:"timestamp"`
		Value     json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	h.Value = decodeSampleValue(raw.Value)
	if ts, ok := decodeSampleTime(raw.Timestamp); ok {
		h.Timestamp = ts
	}
	return nil
}

// decodeSampleTime parses a sample's timestamp, reporting whether it was usable.
func decodeSampleTime(raw json.RawMessage) (time.Time, bool) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return time.Time{}, false
	}
	ts, err := ParseAPITime(s)
	if err != nil {
		return time.Time{}, false
	}
	return ts, true
}

// decodeSampleValue renders a sample's value as a string.
//
// The API types every value as a string regardless of the underlying metric,
// but a numeric or boolean reading occasionally arrives unquoted, which is not
// worth failing a series over: the raw token is already the representation
// BuildFrame would parse.
func decodeSampleValue(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return ""
	}

	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return trimmed
}

// ParseAPITime parses the timestamp formats the Domotz API emits, treating a
// missing zone as UTC.
func ParseAPITime(value string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, apiTimeFormat} {
		if ts, err := time.Parse(layout, value); err == nil {
			return ts.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognised timestamp %q", value)
}
