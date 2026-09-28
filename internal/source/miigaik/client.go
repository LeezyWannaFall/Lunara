package miigaik

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/LeezyWannaFall/Lunara/internal/schedule"
)

const DefaultBaseURL = "https://study.miigaik.ru/"

// HTTPError never includes the response body (it may contain arbitrary HTML).
type HTTPError struct{ StatusCode int }

func (e *HTTPError) Error() string { return fmt.Sprintf("schedule HTTP status %d", e.StatusCode) }

// Client serializes requests, including retries, to avoid overloading the source.
// It is safe for concurrent use. Create one client per source.
type Client struct {
	baseURL      *url.URL
	http         *http.Client
	calendar     schedule.Calendar
	gate         chan struct{}
	nextRequest  time.Time
	minInterval  time.Duration
	retryBackoff time.Duration
}

// NewClient copies httpClient and enforces a maximum 15-second per-attempt timeout.
// A nil HTTP client and an empty URL select production defaults. Redirects are
// rejected so an error/login page cannot masquerade as the requested schedule.
func NewClient(calendar schedule.Calendar, httpClient *http.Client, baseURL string) (*Client, error) {
	if err := calendar.Validate(); err != nil {
		return nil, err
	}
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	base, err := url.Parse(baseURL)
	if err != nil || (base.Scheme != "https" && base.Scheme != "http") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, fmt.Errorf("invalid schedule base URL")
	}
	hc := http.Client{}
	if httpClient != nil {
		hc = *httpClient
	}
	if hc.Timeout <= 0 || hc.Timeout > 15*time.Second {
		hc.Timeout = 15 * time.Second
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{baseURL: base, http: &hc, calendar: calendar, gate: make(chan struct{}, 1), minInterval: time.Second, retryBackoff: time.Second}, nil
}

// FetchWeek accepts any date within the desired week. Errors always return a
// zero Schedule; Unpublished is a successful, explicitly empty source response.
func (c *Client) FetchWeek(ctx context.Context, groupID int64, date time.Time) (schedule.Schedule, error) {
	if date.IsZero() {
		return schedule.Schedule{}, fmt.Errorf("date is required")
	}
	monday := schedule.Monday(date, c.calendar.Location)
	if err := validateRequest(groupID, monday, c.calendar); err != nil {
		return schedule.Schedule{}, err
	}
	select {
	case c.gate <- struct{}{}:
		defer func() { <-c.gate }()
	case <-ctx.Done():
		return schedule.Schedule{}, ctx.Err()
	}
	endpoint := *c.baseURL
	query := endpoint.Query()
	query.Set("groupId", strconv.FormatInt(groupID, 10))
	query.Set("dateStart", monday.Format(time.DateOnly))
	query.Set("dateEnd", monday.AddDate(0, 0, 6).Format(time.DateOnly))
	endpoint.RawQuery = query.Encode()
	for attempt := 0; attempt < 3; attempt++ {
		if err := wait(ctx, time.Until(c.nextRequest)); err != nil {
			return schedule.Schedule{}, err
		}
		c.nextRequest = time.Now().Add(c.minInterval)
		result, retry, retryAfter, err := c.fetch(ctx, endpoint.String(), groupID, monday)
		if err == nil {
			result.CheckedAt = time.Now().UTC()
			return result, nil
		}
		if ctx.Err() != nil {
			return schedule.Schedule{}, ctx.Err()
		}
		if !retry {
			return schedule.Schedule{}, err
		}
		delay := c.retryBackoff * time.Duration(1<<attempt)
		if retryAfter > delay {
			delay = retryAfter
		}
		if next := time.Now().Add(delay); next.After(c.nextRequest) {
			c.nextRequest = next
		}
		if attempt == 2 {
			return schedule.Schedule{}, err
		}
	}
	panic("unreachable")
}

func (c *Client) fetch(ctx context.Context, endpoint string, groupID int64, monday time.Time) (schedule.Schedule, bool, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return schedule.Schedule{}, false, 0, err
	}
	req.Header.Set("Accept", "text/html")
	req.Header.Set("User-Agent", "Lunara/0.2 (+https://github.com/LeezyWannaFall/Lunara)")
	response, err := c.http.Do(req)
	if err != nil {
		return schedule.Schedule{}, transient(err), 0, fmt.Errorf("fetch schedule: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		retry := response.StatusCode == 429 || response.StatusCode >= 500 && response.StatusCode <= 599
		return schedule.Schedule{}, retry, parseRetryAfter(response.Header.Get("Retry-After"), time.Now()), &HTTPError{StatusCode: response.StatusCode}
	}
	contentType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || contentType != "text/html" {
		return schedule.Schedule{}, false, 0, fmt.Errorf("%w: expected text/html", ErrUnexpectedPage)
	}
	result, err := ParseWeek(response.Body, groupID, monday, c.calendar)
	return result, transient(err), 0, err
}

func transient(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var networkError net.Error
	return errors.As(err, &networkError) && (networkError.Timeout() || networkError.Temporary()) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF)
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	if seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil {
		// Clamp before multiplying to avoid duration overflow from an untrusted header.
		if seconds > 86400 {
			seconds = 86400
		}
		if seconds > 0 {
			return time.Duration(seconds) * time.Second
		}
	}
	if date, err := http.ParseTime(value); err == nil && date.After(now) {
		return min(date.Sub(now), 24*time.Hour)
	}
	return 0
}

func wait(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
