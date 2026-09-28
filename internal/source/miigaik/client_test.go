package miigaik

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeezyWannaFall/Lunara/internal/schedule"
)

func testClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	c, err := NewClient(testCalendar(t), server.Client(), server.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	c.minInterval = 0
	c.retryBackoff = 0
	return c
}

func TestFetchWeek(t *testing.T) {
	body := fixture(t, "upper")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Query().Get("groupId") != "1306" || r.URL.Query().Get("dateStart") != "2026-09-28" || r.URL.Query().Get("dateEnd") != "2026-10-04" {
			t.Errorf("bad request: %s %s", r.Method, r.URL)
		}
		if r.Header.Get("User-Agent") == "" || r.Header.Get("HX-Request") != "" {
			t.Error("wrong headers")
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, body)
	}))
	defer server.Close()
	got, err := testClient(t, server).FetchWeek(context.Background(), 1306, testDate(t, "2026-10-04"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Lessons) != 15 || got.CheckedAt.IsZero() || got.Monday.Format(time.DateOnly) != "2026-09-28" {
		t.Fatalf("bad result: %+v", got)
	}
}

func TestHTTPFailuresAndRetries(t *testing.T) {
	good := fixture(t, "upper")
	for _, tc := range []struct {
		name              string
		status            int
		contentType, body string
		attempts          int
		success           bool
	}{
		{"retry 503", 503, "text/html", "busy", 3, true},
		{"retry 429", 429, "text/html", "rate limit", 3, true},
		{"exhausted", 502, "text/html", "busy", 3, false},
		{"not found", 404, "text/html", "missing", 1, false},
		{"redirect", 302, "text/html", "redirect", 1, false},
		{"wrong content type", 200, "application/json", `{}`, 1, false},
		{"broken page", 200, "text/html", "<html><body>error</body></html>", 1, false},
		{"oversized", 200, "text/html", strings.Repeat("x", int(MaxResponseBytes)+1), 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				call := calls.Add(1)
				w.Header().Set("Content-Type", tc.contentType)
				if tc.success && call == 3 {
					w.Header().Set("Content-Type", "text/html")
					io.WriteString(w, good)
					return
				}
				if tc.status == 302 {
					w.Header().Set("Location", "/redirected")
				}
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer server.Close()
			got, err := testClient(t, server).FetchWeek(context.Background(), 1306, testDate(t, "2026-09-28"))
			if (err == nil) != tc.success || int(calls.Load()) != tc.attempts {
				t.Fatalf("calls=%d error=%v", calls.Load(), err)
			}
			if !tc.success && !reflect.DeepEqual(got, schedule.Schedule{}) {
				t.Fatal("partial data on error")
			}
			if !tc.success && tc.status != 200 {
				var he *HTTPError
				if !errors.As(err, &he) || he.StatusCode != tc.status {
					t.Fatalf("missing HTTP error: %v", err)
				}
			}
		})
	}
}

func TestRetryAfterAndCancellation(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(429)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	_, err := testClient(t, server).FetchWeek(ctx, 1306, testDate(t, "2026-09-28"))
	if !errors.Is(err, context.DeadlineExceeded) || calls.Load() != 1 {
		t.Fatalf("calls=%d err=%v", calls.Load(), err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	for _, tc := range []struct {
		value string
		want  time.Duration
	}{{"2", 2 * time.Second}, {"invalid", 0}, {"-1", 0}, {now.Add(3 * time.Second).Format(http.TimeFormat), 3 * time.Second}, {"9223372036854775807", 24 * time.Hour}} {
		if got := parseRetryAfter(tc.value, now); got != tc.want {
			t.Errorf("%s: %v != %v", tc.value, got, tc.want)
		}
	}
}

func TestTimeoutAndRequestCancellation(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); <-r.Context().Done() }))
	defer server.Close()
	c := testClient(t, server)
	c.http.Timeout = 10 * time.Millisecond
	_, err := c.FetchWeek(context.Background(), 1306, testDate(t, "2026-09-28"))
	if err == nil || calls.Load() != 3 {
		t.Fatalf("timeout calls=%d error=%v", calls.Load(), err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = c.FetchWeek(ctx, 1306, testDate(t, "2026-09-28"))
	if !errors.Is(err, context.Canceled) || calls.Load() != 3 {
		t.Fatalf("canceled request sent: %v", err)
	}
}

func TestSerializedRequestsAndInterval(t *testing.T) {
	body := fixture(t, "upper")
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	times := make(chan time.Time, 2)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		times <- time.Now()
		if calls.Add(1) == 1 {
			entered <- struct{}{}
			<-release
		}
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, body)
	}))
	defer server.Close()
	c := testClient(t, server)
	c.minInterval = 40 * time.Millisecond
	done := make(chan error, 1)
	go func() { _, err := c.FetchWeek(context.Background(), 1306, testDate(t, "2026-09-28")); done <- err }()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.FetchWeek(ctx, 1306, testDate(t, "2026-09-28"))
	close(release)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("queue cancellation: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := c.FetchWeek(context.Background(), 1306, testDate(t, "2026-09-28")); err != nil {
		t.Fatal(err)
	}
	first, second := <-times, <-times
	if second.Sub(first) < 30*time.Millisecond {
		t.Fatalf("requests not paced: %v", second.Sub(first))
	}
}

func TestClientValidation(t *testing.T) {
	hc := &http.Client{Timeout: time.Minute}
	c, err := NewClient(testCalendar(t), hc, "")
	if err != nil {
		t.Fatal(err)
	}
	if c.http.Timeout != 15*time.Second || hc.Timeout != time.Minute {
		t.Fatal("caller client modified or timeout unbounded")
	}
	for _, base := range []string{"file:///tmp/test", "https://example.com/?groupId=1", "https://user:password@example.com/"} {
		if _, err := NewClient(testCalendar(t), nil, base); err == nil {
			t.Fatalf("accepted %s", base)
		}
	}
	if _, err := c.FetchWeek(context.Background(), 1306, time.Time{}); err == nil {
		t.Fatal("zero date accepted")
	}
}

func TestRetryAfterSurvivesExhaustedAttempts(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 3 {
			w.Header().Set("Retry-After", "60")
		}
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	c := testClient(t, server)
	if _, err := c.FetchWeek(context.Background(), 1306, testDate(t, "2026-09-28")); err == nil {
		t.Fatal("expected exhausted retries")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := c.FetchWeek(ctx, 1306, testDate(t, "2026-09-28")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cooldown ignored: %v", err)
	}
	if calls.Load() != 3 {
		t.Fatalf("request during cooldown, calls=%d", calls.Load())
	}
}
