package miigaik

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/LeezyWannaFall/Lunara/internal/schedule"
	"github.com/PuerkitoBio/goquery"
)

func (c *Client) FetchExams(ctx context.Context, groupID int64) (schedule.ExamSchedule, error) {
	if groupID <= 0 {
		return schedule.ExamSchedule{}, fmt.Errorf("group ID must be positive")
	}
	select {
	case c.gate <- struct{}{}:
		defer func() { <-c.gate }()
	case <-ctx.Done():
		return schedule.ExamSchedule{}, ctx.Err()
	}
	endpoint, _ := url.Parse(c.baseURL.String())
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/exam/"
	q := endpoint.Query()
	q.Set("searchMode", "group")
	q.Set("groupId", strconv.FormatInt(groupID, 10))
	endpoint.RawQuery = q.Encode()
	for attempt := 0; attempt < 3; attempt++ {
		if err := wait(ctx, time.Until(c.nextRequest)); err != nil {
			return schedule.ExamSchedule{}, err
		}
		c.nextRequest = time.Now().Add(c.minInterval)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
		req.Header.Set("Accept", "text/html")
		req.Header.Set("User-Agent", "Lunara/0.3 (+https://github.com/LeezyWannaFall/Lunara)")
		resp, err := c.http.Do(req)
		if err == nil && resp.StatusCode == http.StatusOK {
			contentType, _, typeErr := mime.ParseMediaType(resp.Header.Get("Content-Type"))
			if typeErr != nil || contentType != "text/html" {
				resp.Body.Close()
				return schedule.ExamSchedule{}, fmt.Errorf("%w: expected text/html", ErrUnexpectedPage)
			}
			result, parseErr := ParseExams(resp.Body, groupID, c.calendar)
			resp.Body.Close()
			if parseErr != nil {
				return schedule.ExamSchedule{}, parseErr
			}
			result.CheckedAt = time.Now().UTC()
			return result, nil
		}
		var fetchErr error
		retry := false
		retryAfter := time.Duration(0)
		if err != nil {
			fetchErr, retry = fmt.Errorf("fetch exams: %w", err), transient(err)
		} else {
			retry = resp.StatusCode == 429 || resp.StatusCode >= 500 && resp.StatusCode <= 599
			retryAfter = parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
			fetchErr = &HTTPError{StatusCode: resp.StatusCode}
			resp.Body.Close()
		}
		if !retry || attempt == 2 {
			return schedule.ExamSchedule{}, fetchErr
		}
		delay := c.retryBackoff * time.Duration(1<<attempt)
		if retryAfter > delay {
			delay = retryAfter
		}
		c.nextRequest = time.Now().Add(delay)
	}
	panic("unreachable")
}

func ParseExams(reader io.Reader, groupID int64, calendar schedule.Calendar) (schedule.ExamSchedule, error) {
	if groupID <= 0 || calendar.Validate() != nil {
		return schedule.ExamSchedule{}, fmt.Errorf("invalid exam request")
	}
	body, err := io.ReadAll(io.LimitReader(reader, MaxResponseBytes+1))
	if err != nil || int64(len(body)) > MaxResponseBytes {
		return schedule.ExamSchedule{}, ErrResponseTooLarge
	}
	text := strings.ToLower(strings.TrimSpace(string(body)))
	if !strings.HasSuffix(text, "</html>") || !strings.Contains(text, "</body>") {
		return schedule.ExamSchedule{}, fmt.Errorf("%w: incomplete HTML document", ErrUnexpectedPage)
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(body)))
	if err != nil {
		return schedule.ExamSchedule{}, err
	}
	config := doc.Find("#exam-group-select-config")
	if config.Length() != 1 {
		return schedule.ExamSchedule{}, fmt.Errorf("%w: missing group selector", ErrUnexpectedPage)
	}
	var selected struct {
		OptionsProps struct {
			InitialOptions []struct {
				Name, Value string
				Selected    bool
			}
		}
	}
	if json.Unmarshal([]byte(config.Text()), &selected) != nil {
		return schedule.ExamSchedule{}, fmt.Errorf("%w: invalid group selector", ErrUnexpectedPage)
	}
	name := ""
	count := 0
	for _, o := range selected.OptionsProps.InitialOptions {
		if o.Selected {
			count++
			if o.Value != strconv.FormatInt(groupID, 10) {
				return schedule.ExamSchedule{}, fmt.Errorf("%w: selected group differs", ErrUnexpectedPage)
			}
			name = clean(o.Name)
		}
	}
	root := doc.Find("#exam-schedule-results")
	if count != 1 || name == "" || root.Length() != 1 || clean(root.ChildrenFiltered("h2").Text()) != name {
		return schedule.ExamSchedule{}, fmt.Errorf("%w: invalid exam heading", ErrUnexpectedPage)
	}
	block := root.ChildrenFiltered(".schedule-block")
	if block.Length() != 1 {
		return schedule.ExamSchedule{}, fmt.Errorf("%w: missing exam block", ErrUnexpectedPage)
	}
	result := schedule.ExamSchedule{GroupID: groupID, Status: schedule.Published, Exams: []schedule.Exam{}, NormalizationVersion: NormalizationVersion}
	if empty := block.ChildrenFiltered(".schedule-empty"); empty.Length() > 0 {
		if empty.Length() != 1 || block.Find(".lesson-block").Length() != 0 {
			return schedule.ExamSchedule{}, fmt.Errorf("%w: ambiguous empty exams", ErrUnexpectedPage)
		}
		result.Status = schedule.Unpublished
		return fingerprintExams(result), nil
	}
	days := block.ChildrenFiltered(".day-block")
	if days.Length() == 0 {
		return schedule.ExamSchedule{}, fmt.Errorf("%w: missing exam days", ErrUnexpectedPage)
	}
	for _, day := range days.EachIter() {
		date, e := time.ParseInLocation("02.01.2006", clean(day.Find(".weekday-block span").First().Text()), calendar.Location)
		if e != nil {
			return schedule.ExamSchedule{}, fmt.Errorf("%w: invalid exam date", ErrUnexpectedPage)
		}
		cards := day.ChildrenFiltered(".lesson-block")
		if cards.Length() == 0 {
			return schedule.ExamSchedule{}, fmt.Errorf("%w: exam day is empty", ErrUnexpectedPage)
		}
		for _, card := range cards.EachIter() {
			lesson, e := parseLesson(card, date)
			if e != nil {
				return schedule.ExamSchedule{}, fmt.Errorf("%w: %v", ErrUnexpectedPage, e)
			}
			result.Exams = append(result.Exams, schedule.Exam{Date: date, StartMinute: lesson.StartMinute, Subject: lesson.Subject, Kind: func() string {
				if lesson.RawType != "" {
					return lesson.RawType
				}
				return string(lesson.Type)
			}(), Teachers: lesson.Teachers, Rooms: lesson.Rooms})
		}
	}
	if len(result.Exams) != doc.Find("#exam-schedule-results .lesson-block").Length() {
		return schedule.ExamSchedule{}, fmt.Errorf("%w: unparsed exams", ErrUnexpectedPage)
	}
	return fingerprintExams(result), nil
}

func fingerprintExams(result schedule.ExamSchedule) schedule.ExamSchedule {
	sort.SliceStable(result.Exams, func(i, j int) bool {
		if !result.Exams[i].Date.Equal(result.Exams[j].Date) {
			return result.Exams[i].Date.Before(result.Exams[j].Date)
		}
		return result.Exams[i].StartMinute < result.Exams[j].StartMinute
	})
	body, _ := json.Marshal(result)
	sum := sha256.Sum256(body)
	result.Hash = hex.EncodeToString(sum[:])
	return result
}
