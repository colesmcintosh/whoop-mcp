package whoop

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

type staticSource struct{ tok *oauth2.Token }

func (s staticSource) Token() (*oauth2.Token, error) { return s.tok, nil }

// newTestClient stands up an httptest server and returns a Client wired
// to it, plus the recorder for inspecting incoming requests.
func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	src := staticSource{tok: &oauth2.Token{AccessToken: "test", Expiry: time.Now().Add(time.Hour)}}
	c := New(context.Background(), src)
	c.baseURL = srv.URL
	return c, srv
}

func TestGetEndpointsHitExpectedPaths(t *testing.T) {
	cases := []struct {
		name string
		path string
		call func(c *Client) error
	}{
		{"profile", "/v2/user/profile/basic", func(c *Client) error {
			_, err := c.GetProfile(context.Background())
			return err
		}},
		{"body", "/v2/user/measurement/body", func(c *Client) error {
			_, err := c.GetBodyMeasurement(context.Background())
			return err
		}},
		{"list cycles", "/v2/cycle", func(c *Client) error {
			_, err := c.ListCycles(context.Background(), ListParams{})
			return err
		}},
		{"get cycle", "/v2/cycle/abc", func(c *Client) error {
			_, err := c.GetCycle(context.Background(), "abc")
			return err
		}},
		{"cycle recovery", "/v2/cycle/abc/recovery", func(c *Client) error {
			_, err := c.GetCycleRecovery(context.Background(), "abc")
			return err
		}},
		{"cycle sleep", "/v2/cycle/abc/sleep", func(c *Client) error {
			_, err := c.GetCycleSleep(context.Background(), "abc")
			return err
		}},
		{"list recovery", "/v2/recovery", func(c *Client) error {
			_, err := c.ListRecovery(context.Background(), ListParams{})
			return err
		}},
		{"list sleep", "/v2/activity/sleep", func(c *Client) error {
			_, err := c.ListSleep(context.Background(), ListParams{})
			return err
		}},
		{"get sleep", "/v2/activity/sleep/sid", func(c *Client) error {
			_, err := c.GetSleep(context.Background(), "sid")
			return err
		}},
		{"list workouts", "/v2/activity/workout", func(c *Client) error {
			_, err := c.ListWorkouts(context.Background(), ListParams{})
			return err
		}},
		{"get workout", "/v2/activity/workout/wid", func(c *Client) error {
			_, err := c.GetWorkout(context.Background(), "wid")
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var seen string
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				seen = r.URL.Path
				if r.Header.Get("Authorization") == "" {
					t.Errorf("missing Authorization header")
				}
				_, _ = w.Write([]byte(`{"ok":true}`))
			})
			if err := tc.call(c); err != nil {
				t.Fatalf("call: %v", err)
			}
			if seen != tc.path {
				t.Fatalf("path = %q, want %q", seen, tc.path)
			}
		})
	}
}

func TestListParamsSerialization(t *testing.T) {
	var query string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		_, _ = w.Write([]byte(`{}`))
	})
	start := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC)
	if _, err := c.ListCycles(context.Background(), ListParams{
		Limit:     10,
		Start:     start,
		End:       end,
		NextToken: "tok",
	}); err != nil {
		t.Fatalf("call: %v", err)
	}
	for _, want := range []string{"limit=10", "start=2026-05-01T00", "end=2026-05-10T00", "nextToken=tok"} {
		if !strings.Contains(query, want) {
			t.Errorf("query %q missing %q", query, want)
		}
	}
}

func TestNon2xxReturnsAPIError(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	})
	_, err := c.GetProfile(context.Background())
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if apiErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d", apiErr.StatusCode)
	}
	if !strings.Contains(apiErr.Error(), "429") {
		t.Fatalf("Error() = %q, want status in message", apiErr.Error())
	}
}

func TestEmptyResponseBodyIsError(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	if _, err := c.GetProfile(context.Background()); err == nil {
		t.Fatal("expected error for empty body")
	}
}

func TestNetworkErrorPropagates(t *testing.T) {
	c, srv := newTestClient(t, func(_ http.ResponseWriter, _ *http.Request) {})
	srv.Close() // close immediately so the request fails to connect
	if _, err := c.GetProfile(context.Background()); err == nil {
		t.Fatal("expected network error after server close")
	}
}

func TestRevokeAccessOK(t *testing.T) {
	var method, path string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})
	if err := c.RevokeAccess(context.Background()); err != nil {
		t.Fatalf("RevokeAccess: %v", err)
	}
	if method != http.MethodDelete || path != "/v2/user/access" {
		t.Fatalf("wrong call: %s %s", method, path)
	}
}

func TestRevokeAccessNon2xx(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	})
	err := c.RevokeAccess(context.Background())
	if _, ok := err.(*APIError); !ok {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
}

func TestRevokeAccessNetworkError(t *testing.T) {
	c, srv := newTestClient(t, func(_ http.ResponseWriter, _ *http.Request) {})
	srv.Close()
	if err := c.RevokeAccess(context.Background()); err == nil {
		t.Fatal("expected network error")
	}
}

func TestGetRequestConstructionError(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	})
	// Inject a control character to make http.NewRequestWithContext fail.
	c.baseURL = "http://\x7f"
	if _, err := c.GetProfile(context.Background()); err == nil {
		t.Fatal("expected request construction error")
	}
}

func TestReadBodyError(t *testing.T) {
	// Hijack the connection and send headers that promise more body
	// than we actually deliver, then close — io.ReadAll surfaces an
	// unexpected EOF.
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Skip("hijacker not supported")
			return
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			t.Fatalf("hijack: %v", err)
		}
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\n")
		_ = buf.Flush()
		_ = conn.Close()
	})
	if _, err := c.GetProfile(context.Background()); err == nil {
		t.Fatal("expected read-body error")
	}
}

func TestRevokeAccessRequestConstructionError(t *testing.T) {
	c, _ := newTestClient(t, func(_ http.ResponseWriter, _ *http.Request) {})
	c.baseURL = "http://\x7f"
	if err := c.RevokeAccess(context.Background()); err == nil {
		t.Fatal("expected request construction error")
	}
}
