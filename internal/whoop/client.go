// Package whoop is a minimal HTTP client for Whoop API v2.
//
// The client returns raw JSON bodies rather than typed structs so the
// caller (MCP server) can forward responses to a language model without
// needing to stay in lockstep with Whoop's evolving schema.
package whoop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"golang.org/x/oauth2"
)

// BaseURL is the root of the Whoop developer API.
const BaseURL = "https://api.prod.whoop.com/developer"

// Client wraps an OAuth-authenticated HTTP client for the Whoop API.
type Client struct {
	httpClient *http.Client
	baseURL    string
}

// New returns a Client that uses the given oauth2 TokenSource for
// authentication. The underlying http.Client automatically refreshes the
// access token when it expires.
func New(ctx context.Context, src oauth2.TokenSource) *Client {
	hc := oauth2.NewClient(ctx, src)
	hc.Timeout = 30 * time.Second
	return &Client{httpClient: hc, baseURL: BaseURL}
}

// APIError represents a non-2xx response from the Whoop API.
type APIError struct {
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("whoop api: status %d: %s", e.StatusCode, e.Body)
}

// ListParams are the common pagination filters accepted by Whoop list endpoints.
type ListParams struct {
	Limit     int       // Defaults to 10 server-side. Max 25.
	Start     time.Time // Inclusive lower bound on the record's start time.
	End       time.Time // Exclusive upper bound on the record's start time.
	NextToken string    // Token returned from a previous response for pagination.
}

func (p ListParams) values() url.Values {
	v := url.Values{}
	if p.Limit > 0 {
		v.Set("limit", strconv.Itoa(p.Limit))
	}
	if !p.Start.IsZero() {
		v.Set("start", p.Start.UTC().Format(time.RFC3339))
	}
	if !p.End.IsZero() {
		v.Set("end", p.End.UTC().Format(time.RFC3339))
	}
	if p.NextToken != "" {
		v.Set("nextToken", p.NextToken)
	}
	return v
}

func (c *Client) get(ctx context.Context, path string, query url.Values) (json.RawMessage, error) {
	u := c.baseURL + path
	if len(query) > 0 {
		u = u + "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("whoop request %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &APIError{StatusCode: resp.StatusCode, Body: string(body)}
	}
	if len(body) == 0 {
		return nil, errors.New("empty response body")
	}
	return json.RawMessage(body), nil
}

// GetProfile returns the basic profile of the authenticated user.
func (c *Client) GetProfile(ctx context.Context) (json.RawMessage, error) {
	return c.get(ctx, "/v2/user/profile/basic", nil)
}

// GetBodyMeasurement returns height, weight, and max heart rate for the
// authenticated user.
func (c *Client) GetBodyMeasurement(ctx context.Context) (json.RawMessage, error) {
	return c.get(ctx, "/v2/user/measurement/body", nil)
}

// ListCycles returns a paginated list of physiological cycles.
func (c *Client) ListCycles(ctx context.Context, p ListParams) (json.RawMessage, error) {
	return c.get(ctx, "/v2/cycle", p.values())
}

// GetCycle returns a single cycle by id.
func (c *Client) GetCycle(ctx context.Context, cycleID string) (json.RawMessage, error) {
	return c.get(ctx, "/v2/cycle/"+url.PathEscape(cycleID), nil)
}

// GetCycleRecovery returns the recovery score attached to a cycle.
func (c *Client) GetCycleRecovery(ctx context.Context, cycleID string) (json.RawMessage, error) {
	return c.get(ctx, "/v2/cycle/"+url.PathEscape(cycleID)+"/recovery", nil)
}

// GetCycleSleep returns the sleep record attached to a cycle.
func (c *Client) GetCycleSleep(ctx context.Context, cycleID string) (json.RawMessage, error) {
	return c.get(ctx, "/v2/cycle/"+url.PathEscape(cycleID)+"/sleep", nil)
}

// ListRecovery returns a paginated list of recovery records.
func (c *Client) ListRecovery(ctx context.Context, p ListParams) (json.RawMessage, error) {
	return c.get(ctx, "/v2/recovery", p.values())
}

// ListSleep returns a paginated list of sleep records.
func (c *Client) ListSleep(ctx context.Context, p ListParams) (json.RawMessage, error) {
	return c.get(ctx, "/v2/activity/sleep", p.values())
}

// GetSleep returns a single sleep record by id.
func (c *Client) GetSleep(ctx context.Context, sleepID string) (json.RawMessage, error) {
	return c.get(ctx, "/v2/activity/sleep/"+url.PathEscape(sleepID), nil)
}

// ListWorkouts returns a paginated list of workouts.
func (c *Client) ListWorkouts(ctx context.Context, p ListParams) (json.RawMessage, error) {
	return c.get(ctx, "/v2/activity/workout", p.values())
}

// GetWorkout returns a single workout by id.
func (c *Client) GetWorkout(ctx context.Context, workoutID string) (json.RawMessage, error) {
	return c.get(ctx, "/v2/activity/workout/"+url.PathEscape(workoutID), nil)
}

// RevokeAccess revokes the user's OAuth grant via Whoop. After a
// successful call the access token and any associated refresh tokens
// are invalidated.
func (c *Client) RevokeAccess(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.baseURL+"/v2/user/access", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("whoop revoke: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return &APIError{StatusCode: resp.StatusCode, Body: string(body)}
	}
	return nil
}
