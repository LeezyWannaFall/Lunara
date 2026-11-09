package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/LeezyWannaFall/Lunara/internal/schedule"
)

func TestWidgetLimitsAndNavigation(t *testing.T) {
	if err := validateWidgetHTML(scheduleWidgetHTML); err != nil {
		t.Fatal(err)
	}
	w := published(t)
	h := testHandler(t, map[string]schedule.Schedule{"2026-09-28": w})
	for _, command := range []string{"start", "widget", "today", "tomorrow", "week", "nextweek", "day", "next", "exams", "changes", "help"} {
		arg := ""
		if command == "day" {
			arg = "28.09.2026"
		}
		q, ok := h.widgetCommand(command, arg)
		if !ok {
			t.Fatal(command)
		}
		data, err := h.widgetData(context.Background(), q)
		if err != nil {
			t.Fatalf("%s: %v", command, err)
		}
		var p widgetPage
		if err = json.Unmarshal([]byte(data), &p); err != nil {
			t.Fatal(err)
		}
		if p.Title == "" || len(data) > 1024 {
			t.Fatalf("invalid %s: %s", command, data)
		}
		if command == "week" {
			if len(p.Rows) != 7 {
				t.Fatal(p)
			}
			for _, r := range p.Rows {
				if _, err := h.widgetData(context.Background(), r.Query); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	for _, q := range []string{"w1|today||0", "w1|tomorrow||0", "w1|nextweek||0", "w1|week||0"} {
		if _, err := h.widgetData(context.Background(), q); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{"garbage", "w2|day||0", "w1|day|oops|0", "w1|day|2026-09-28|-1", "w1|changes|-4|0", "w1|next|unexpected|0", "w1|unknown||0", strings.Repeat("a", 65)} {
		if _, err := h.widgetData(context.Background(), q); err == nil {
			t.Fatalf("accepted %q", q)
		}
	}
}

func TestWidgetPaginationPreservesLongSourceText(t *testing.T) {
	w := published(t)
	w.Lessons = w.Lessons[:1]
	w.Lessons[0].Subject = strings.Repeat("Длинное название <script> & ", 100)
	h := testHandler(t, map[string]schedule.Schedule{"2026-09-28": w})
	q, _ := h.widgetCommand("today", "")
	var got strings.Builder
	for pages := 0; q != ""; pages++ {
		if pages > 100 {
			t.Fatal("pagination loop")
		}
		data, err := h.widgetData(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
		var p widgetPage
		json.Unmarshal([]byte(data), &p)
		got.WriteString(p.Body)
		if len(data) > 1024 || !utf8.ValidString(data) {
			t.Fatal("invalid data")
		}
		q = p.Next
	}
	if got.String() != widgetPlain(formatLesson(w.Lessons[0])) {
		t.Fatal("lost text during pagination")
	}
	for _, value := range []string{strings.Repeat("\n", 1000), strings.Repeat("😀", 1000), strings.Repeat("a", 1000), strings.Repeat("<>&\x01\\\"", 500)} {
		chunks := widgetChunks(value, 280)
		if strings.Join(chunks, "") != value {
			t.Fatal("lost text")
		}
		for _, c := range chunks {
			encoded, _ := json.Marshal(c)
			if len(encoded) > 282 || len(c) > 280 || !utf8.ValidString(c) || strings.Count(c, "\n") > 7 {
				t.Fatal("unbounded chunk")
			}
		}
	}
}

type widgetFakeAPI struct {
	fakeAPI
	widgets, answers   int
	sendErr, answerErr error
	data, caption      string
	thread             int64
}

func (a *widgetFakeAPI) SendWidget(_ context.Context, _ int64, thread int64, data, caption string) error {
	a.widgets++
	a.data = data
	a.caption = caption
	a.thread = thread
	return a.sendErr
}
func (a *widgetFakeAPI) AnswerWidgetQuery(_ context.Context, _ string, data string) error {
	a.answers++
	a.data = data
	return a.answerErr
}
func widgetBot(t *testing.T, a API) *Bot {
	t.Helper()
	b, err := NewBot(context.Background(), a, testHandler(t, nil), fakeState{}, -100, slog.New(slog.NewTextHandler(io.Discard, nil)), 77)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestWidgetRoutingAndFallback(t *testing.T) {
	for _, tc := range []struct {
		name, text, chatType string
		id, thread           int64
		err                  error
		widgets, texts       int
	}{
		{"private", "/week@LunaraBot", "private", 10, 0, nil, 1, 0},
		{"private topic", "/today", "private", 10, 23, nil, 1, 0},
		{"group", "/week", "supergroup", -100, 77, nil, 0, 1},
		{"wrong topic", "/week", "supergroup", -100, 78, nil, 0, 0},
		{"other bot", "/week@OtherBot", "private", 10, 0, nil, 0, 0},
		{"invalid date", "/day oops", "private", 10, 0, nil, 0, 1},
		{"explicit text", "/text week", "private", 10, 0, nil, 0, 1},
		{"unsupported", "/week", "private", 10, 0, &APIError{Code: 404}, 1, 1},
		{"bad request", "/week", "private", 10, 0, &APIError{Code: 400}, 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &widgetFakeAPI{sendErr: tc.err}
			b := widgetBot(t, a)
			err := b.handleUpdate(context.Background(), Update{Message: &Message{Chat: Chat{ID: tc.id, Type: tc.chatType}, MessageThreadID: tc.thread, Text: tc.text}})
			if err != nil || a.widgets != tc.widgets || len(a.sent) != tc.texts {
				t.Fatalf("%v %+v", err, a)
			}
			if tc.name == "private topic" && a.thread != 23 {
				t.Fatal("lost topic")
			}
		})
	}
	for _, err := range []error{&APIError{Code: 429}, &RequestError{Method: "sendWidget"}} {
		a := &widgetFakeAPI{sendErr: err}
		b := widgetBot(t, a)
		if b.handleUpdate(context.Background(), Update{Message: &Message{Chat: Chat{ID: 10, Type: "private"}, Text: "/week"}}) == nil || len(a.sent) != 0 {
			t.Fatal("ambiguous failure must not send duplicate text")
		}
	}
}

func TestWidgetQueryAccessAndExpiredAnswers(t *testing.T) {
	for _, tc := range []struct {
		chat          *Chat
		user, message int64
		kind          string
		allowed       bool
	}{
		{&Chat{ID: 10, Type: "private"}, 10, 1, "data", true},
		{&Chat{ID: 10, Type: "private"}, 11, 1, "data", false},
		{&Chat{ID: -100, Type: "supergroup"}, 10, 1, "data", false},
		{nil, 10, 1, "data", false},
		{&Chat{ID: 10, Type: "private"}, 10, 0, "data", false},
		{&Chat{ID: 10, Type: "private"}, 10, 1, "sign_data", false},
	} {
		a := &widgetFakeAPI{answerErr: errors.New("query expired")}
		b := widgetBot(t, a)
		q := &WidgetQuery{ID: "query", Chat: tc.chat, From: User{ID: tc.user}, MessageID: tc.message, Data: "w1|today||0"}
		q.Type.Type = tc.kind
		if err := b.handleUpdate(context.Background(), Update{WidgetQuery: q}); err != nil {
			t.Fatal(err)
		}
		var p widgetPage
		json.Unmarshal([]byte(a.data), &p)
		if a.answers != 1 || (p.Error == "") != tc.allowed {
			t.Fatalf("bad access: %+v", p)
		}
	}
}

func TestWidgetAPIContract(t *testing.T) {
	var methods []string
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		method := strings.TrimPrefix(r.URL.Path, "/bottest/")
		methods = append(methods, method)
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		switch method {
		case "sendWidget":
			if req["html"] != scheduleWidgetHTML || req["chat_data"] != "{}" || req["allow_queries"] != true || req["is_shared"] != nil || req["message_thread_id"] != float64(23) {
				t.Errorf("wrong widget request")
			}
		case "getUpdates":
			allowed, _ := req["allowed_updates"].([]any)
			found := false
			for _, v := range allowed {
				if v == "widget_query" {
					found = true
				}
			}
			if !found {
				t.Error("missing widget updates")
			}
			return jsonResponse(`{"ok":true,"result":[{"update_id":1,"widget_query":{"id":"q","from":{"id":10},"chat":{"id":10,"type":"private"},"message_id":2,"type":{"type":"data"},"data":"w1|today||0"}}]}`), nil
		case "answerWidgetQuery":
			if req["widget_query_id"] != "q" || req["data"] != "{}" {
				t.Error("wrong answer")
			}
		default:
			t.Errorf("unexpected method %s", method)
		}
		return jsonResponse(`{"ok":true,"result":true}`), nil
	})
	client, _ := NewClient("test", &http.Client{Transport: transport}, "https://telegram.test")
	if err := client.SendWidget(context.Background(), 10, 23, "{}", "caption"); err != nil {
		t.Fatal(err)
	}
	updates, err := client.GetUpdates(context.Background(), 0)
	if err != nil || len(updates) != 1 || updates[0].WidgetQuery.Type.Type != "data" {
		t.Fatalf("%+v %v", updates, err)
	}
	if err := client.AnswerWidgetQuery(context.Background(), "q", "{}"); err != nil {
		t.Fatal(err)
	}
	if err := client.AnswerWidgetQuery(context.Background(), "q", strings.Repeat("a", 1025)); err == nil {
		t.Fatal("oversized answer accepted")
	}
	if len(methods) != 3 {
		t.Fatal(methods)
	}
}

func TestWidgetUnpublishedAndEmpty(t *testing.T) {
	for _, status := range []schedule.Status{schedule.Unpublished, schedule.Published} {
		w := published(t)
		w.Status = status
		w.Lessons = nil
		w.CheckedAt = time.Now()
		h := testHandler(t, map[string]schedule.Schedule{"2026-09-28": w})
		data, err := h.widgetData(context.Background(), "w1|day|2026-09-28|0")
		if err != nil {
			t.Fatal(err)
		}
		wanted := "Занятий нет"
		if status == schedule.Unpublished {
			wanted = "не опубликовано"
		}
		if !strings.Contains(data, wanted) {
			t.Fatal(data)
		}
	}
}

// Export actual backend responses for the optional browser smoke test.
func TestWidgetUIFixture(t *testing.T) {
	path := os.Getenv("LUNARA_WIDGET_FIXTURE")
	if path == "" {
		t.Skip("set LUNARA_WIDGET_FIXTURE to export browser fixture")
	}
	w := published(t)
	w.CheckedAt = w.Monday.Add(9*time.Hour + 42*time.Minute)
	w.Lessons[0].Subject = "Инженерное обустройство территорий"
	w.Lessons[0].Teachers = []string{"Снежинская Елена Юрьевна"}
	w.Lessons[0].Rooms = []schedule.Room{{Name: "53", Building: "Старый корпус"}}
	w.Lessons[1].Subject = "Геодезия"
	h := testHandler(t, map[string]schedule.Schedule{"2026-09-28": w})
	results := map[string]json.RawMessage{}
	queue := []string{"w1|week|2026-09-28|0", "w1|today||0", "w1|tomorrow||0", "w1|week||0", "w1|next||0", "w1|exams||0", "w1|changes||0", "w1|help||0"}
	for len(queue) > 0 {
		q := queue[0]
		queue = queue[1:]
		if _, ok := results[q]; ok {
			continue
		}
		data, err := h.widgetData(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
		results[q] = json.RawMessage(data)
		var p widgetPage
		if err := json.Unmarshal([]byte(data), &p); err != nil {
			t.Fatal(err)
		}
		results[p.Query] = json.RawMessage(data)
		for _, r := range p.Rows {
			queue = append(queue, r.Query)
		}
		if p.Next != "" {
			queue = append(queue, p.Next)
		}
	}
	data, err := json.Marshal(results)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}
