package telegram

import (
	"bytes"
	"compress/zlib"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/LeezyWannaFall/Lunara/internal/schedule"
	"github.com/LeezyWannaFall/Lunara/internal/storage"
	xhtml "golang.org/x/net/html"
)

//go:embed widget.html
var scheduleWidgetHTML string

// WidgetAPI is optional so text-only API implementations continue to work.
type WidgetAPI interface {
	SendWidget(context.Context, int64, int64, string, string) error
	AnswerWidgetQuery(context.Context, string, string) error
}

type WidgetQuery struct {
	ID        string `json:"id"`
	From      User   `json:"from"`
	Chat      *Chat  `json:"chat,omitempty"`
	MessageID int64  `json:"message_id,omitempty"`
	Type      struct {
		Type string `json:"type"`
	} `json:"type"`
	Data string `json:"data"`
}

type widgetRow struct {
	Label string `json:"l"`
	Query string `json:"q"`
}
type widgetPage struct {
	TextQuery string      `json:"x,omitempty"`
	Subtitle  string      `json:"u,omitempty"`
	Tab       string      `json:"s,omitempty"`
	Query     string      `json:"q"`
	Title     string      `json:"t"`
	Body      string      `json:"b,omitempty"`
	Rows      []widgetRow `json:"r,omitempty"`
	Previous  string      `json:"p,omitempty"`
	Next      string      `json:"n,omitempty"`
	Earlier   string      `json:"e,omitempty"`
	Later     string      `json:"a,omitempty"`
	Count     string      `json:"c,omitempty"`
	Error     string      `json:"error,omitempty"`
}

func (p widgetPage) encode() (string, error) {
	b, err := json.Marshal(p)
	if err == nil && len(b) > 1024 {
		err = fmt.Errorf("widget data exceeds 1024 bytes")
	}
	return string(b), err
}

func validateWidgetHTML(value string) error {
	if len(value) == 0 || len(value) > 32768 {
		return fmt.Errorf("widget HTML exceeds size limit")
	}
	var b bytes.Buffer
	w, _ := zlib.NewWriterLevel(&b, zlib.BestCompression)
	_, err := io.WriteString(w, value)
	if err != nil {
		return err
	}
	if err = w.Close(); err != nil {
		return err
	}
	if b.Len() > 4096 {
		return fmt.Errorf("compressed widget HTML exceeds 4096 bytes")
	}
	return nil
}

func (c *Client) SendWidget(ctx context.Context, chatID, threadID int64, data, caption string) error {
	if err := validateWidgetHTML(scheduleWidgetHTML); err != nil {
		return err
	}
	if len(data) > 1024 {
		return fmt.Errorf("widget data exceeds 1024 bytes")
	}
	request := map[string]any{"chat_id": chatID, "html": scheduleWidgetHTML, "chat_data": data, "caption": caption, "allow_queries": true, "protect_content": true}
	if threadID > 0 {
		request["message_thread_id"] = threadID
	}
	return c.call(ctx, "sendWidget", request, nil)
}
func (c *Client) AnswerWidgetQuery(ctx context.Context, id, data string) error {
	if len(data) > 1024 {
		return fmt.Errorf("widget answer exceeds 1024 bytes")
	}
	return c.call(ctx, "answerWidgetQuery", map[string]string{"widget_query_id": id, "data": data}, nil)
}

func widgetRequest(mode, arg string, page int) string {
	return fmt.Sprintf("w1|%s|%s|%d", mode, arg, page)
}

func (h *Handler) widgetData(ctx context.Context, query string) (string, error) {
	if len(query) > 64 {
		return "", fmt.Errorf("invalid widget query")
	}
	parts := strings.Split(query, "|")
	if len(parts) != 4 || parts[0] != "w1" {
		return "", fmt.Errorf("invalid widget query")
	}
	mode, arg := parts[1], parts[2]
	index, err := strconv.Atoi(parts[3])
	if err != nil || index < 0 || index > 10000 {
		return "", fmt.Errorf("invalid page")
	}
	now := schedule.LocalDate(h.now(), h.calendar.Location)
	if mode == "today" || mode == "tomorrow" || mode == "nextweek" {
		date := now
		if mode == "tomorrow" {
			date = date.AddDate(0, 0, 1)
		}
		if mode == "nextweek" {
			date = schedule.Monday(now, h.calendar.Location).AddDate(0, 0, 7)
			mode = "week"
		} else {
			mode = "day"
		}
		arg = date.Format(time.DateOnly)
	}
	var date time.Time
	if mode == "day" || mode == "week" {
		if arg == "" {
			arg = now.Format(time.DateOnly)
		}
		date, err = time.ParseInLocation(time.DateOnly, arg, h.calendar.Location)
		if err != nil {
			return "", fmt.Errorf("invalid widget date")
		}
		if date.Year() < 1 || date.Year() > 9999 {
			return "", fmt.Errorf("invalid widget date")
		}
		if mode == "week" {
			date = schedule.Monday(date, h.calendar.Location)
			arg = date.Format(time.DateOnly)
		}
	}
	p := widgetPage{Query: widgetRequest(mode, arg, index), Tab: mode}
	if mode == "day" {
		if date.Equal(now) {
			p.Tab = "today"
		} else if date.Equal(now.AddDate(0, 0, 1)) {
			p.Tab = "tomorrow"
		}
	}
	var bodies []string
	switch mode {
	case "week", "day":
		w, e := h.repo.GetWeek(ctx, h.groupID, date)
		if e != nil && !errors.Is(e, storage.ErrNotFound) {
			return "", e
		}
		step := 1
		p.Title = shortDate(date) + " " + date.Format("2006")
		if mode == "week" {
			step = 7
			p.Title = weekRange(date)
		}
		p.Earlier = widgetRequest(mode, date.AddDate(0, 0, -step).Format(time.DateOnly), 0)
		p.Later = widgetRequest(mode, date.AddDate(0, 0, step).Format(time.DateOnly), 0)
		if e == nil && !w.CheckedAt.IsZero() {
			p.Subtitle = "Проверено: " + checked(w.CheckedAt, h.calendar.Location)
		}
		if errors.Is(e, storage.ErrNotFound) {
			bodies = []string{"Данных в кеше пока нет. Попробуйте позже."}
		} else if w.Status == schedule.Unpublished {
			bodies = []string{"Расписание ещё не опубликовано.\nПроверено: " + checked(w.CheckedAt, h.calendar.Location)}
		} else if mode == "week" {
			for i := 0; i < 7; i++ {
				d := date.AddDate(0, 0, i)
				count := 0
				for _, l := range w.Lessons {
					if sameDate(l.Date, d, h.calendar.Location) {
						count++
					}
				}
				p.Rows = append(p.Rows, widgetRow{Label: fmt.Sprintf("%s · %s", d.Format("02.01")+" "+[...]string{"Вс", "Пн", "Вт", "Ср", "Чт", "Пт", "Сб"}[d.Weekday()], widgetLessonCount(count)), Query: widgetRequest("day", d.Format(time.DateOnly), 0)})
			}
			p.Body = weekName(w.WeekType) + " неделя"
			p.Query = widgetRequest(mode, arg, 0)
			return p.encode()
		} else {
			for _, l := range w.Lessons {
				if sameDate(l.Date, date, h.calendar.Location) {
					bodies = append(bodies, widgetPlain(formatLesson(l)))
				}
			}
			if len(bodies) == 0 {
				bodies = []string{"Занятий нет — можно выдохнуть."}
			}
		}
	case "next", "exams", "help":
		if arg != "" {
			return "", fmt.Errorf("invalid widget argument")
		}
		p.Title = map[string]string{"next": "Ближайшая пара", "exams": "Экзамены", "help": "Справка"}[mode]
		messages, e := h.Handle(ctx, "/"+mode, "")
		if e != nil {
			return "", e
		}
		for _, m := range messages {
			bodies = append(bodies, widgetPlain(m))
		}
	case "changes":
		if arg != "" {
			v, e := strconv.ParseInt(arg, 10, 64)
			if e != nil || v <= 0 {
				return "", fmt.Errorf("invalid history cursor")
			}
		}
		p.Title = "Изменения"
		messages, next, e := h.Changes(ctx, arg)
		if e != nil {
			return "", e
		}
		for _, m := range messages {
			bodies = append(bodies, widgetPlain(m))
		}
		if next > 0 {
			p.Later = widgetRequest("changes", strconv.FormatInt(next, 10), 0)
		}
	default:
		return "", fmt.Errorf("unknown widget mode")
	}
	// Bound UTF-8 bytes, not characters: Telegram's state and answers allow 1 KiB.
	// Every fragment remains reachable, including unusually long source fields.
	var pages []string
	for _, body := range bodies {
		if mode == "day" || mode == "next" {
			pages = append(pages, body)
		} else {
			pages = append(pages, widgetChunks(body, 280)...)
		}
	}
	if len(pages) == 0 {
		pages = []string{"Данных пока нет."}
	}
	if index >= len(pages) {
		index = len(pages) - 1
	}
	p.Query = widgetRequest(mode, arg, index)
	p.Body = pages[index]
	p.Count = fmt.Sprintf("%d / %d", index+1, len(pages))
	if index > 0 {
		p.Previous = widgetRequest(mode, arg, index-1)
	}
	if index+1 < len(pages) {
		p.Next = widgetRequest(mode, arg, index+1)
	}
	if mode == "day" || mode == "next" {
		p.TextQuery = widgetRequest("text"+mode, arg, 0)
		if _, err := p.encode(); err != nil {
			body := p.Body
			for limit := 90; limit >= 10; limit -= 10 {
				p.Body = clip(body, limit) + "\n\nПолная карточка не помещается. Нажмите «Текст»."
				if _, err := p.encode(); err == nil {
					break
				}
			}
		}
	}
	return p.encode()
}

// Preserve link destinations in text pages; never inject source HTML into the widget.
func widgetPlain(value string) string {
	tokenizer := xhtml.NewTokenizer(strings.NewReader(value))
	var b strings.Builder
	link := ""
	for {
		switch tokenizer.Next() {
		case xhtml.ErrorToken:
			return strings.TrimSpace(b.String())
		case xhtml.TextToken:
			b.Write(tokenizer.Text())
		case xhtml.StartTagToken:
			t := tokenizer.Token()
			if t.Data == "a" {
				for _, a := range t.Attr {
					if a.Key == "href" {
						link = a.Val
					}
				}
			}
		case xhtml.EndTagToken:
			if tokenizer.Token().Data == "a" && link != "" {
				b.WriteString(" (" + link + ")")
				link = ""
			}
		}
	}
}

// Budget the JSON-escaped representation, including control characters. Also
// cap line breaks because Telegram clips widgets instead of scrolling them.
func widgetChunks(value string, limit int) []string {
	var result []string
	for value != "" {
		end, cost, lines := 0, 0, 0
		for i, r := range value {
			_, size := utf8.DecodeRuneInString(value[i:])
			encoded := utf8.RuneLen(r)
			switch r {
			case '\n', '\r', '\t', '\b', '\f', '"', '\\':
				encoded = 2
			case '<', '>', '&', '\u2028', '\u2029':
				encoded = 6
			default:
				if r < 0x20 {
					encoded = 6
				}
			}
			if cost+encoded > limit {
				break
			}
			cost += encoded
			end = i + size
			if r == '\n' {
				lines++
				if lines == 7 {
					break
				}
			}
		}
		if end < len(value) {
			if cut := strings.LastIndexAny(value[:end], "\n "); cut > end/2 {
				end = cut + 1
			}
		}
		result = append(result, value[:end])
		value = value[end:]
	}
	return result
}

func (b *Bot) handleWidget(ctx context.Context, q *WidgetQuery) error {
	api, ok := b.api.(WidgetAPI)
	if !ok {
		return nil
	}
	// Never read schedule data for group/inline/forward or another user's chat.
	if q.Chat == nil || q.Chat.Type != "private" || q.From.ID != q.Chat.ID || q.From.ID <= 0 || q.MessageID <= 0 || q.Type.Type != "data" {
		return b.answerWidget(ctx, api, q.ID, `{"error":"Откройте виджет в личном чате с ботом."}`)
	}
	parts := strings.Split(q.Data, "|")
	if len(q.Data) <= 64 && len(parts) == 4 && parts[0] == "w1" && parts[3] == "0" && (parts[1] == "textday" || parts[1] == "textnext") {
		command := "/next"
		if parts[1] == "textday" {
			if _, err := time.ParseInLocation(time.DateOnly, parts[2], b.handler.calendar.Location); err != nil {
				return b.answerWidget(ctx, api, q.ID, `{"error":"Некорректная дата."}`)
			}
			command = "/day " + parts[2]
		} else if parts[2] != "" {
			return b.answerWidget(ctx, api, q.ID, `{"error":"Некорректный запрос."}`)
		}
		textCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		messages, err := b.handler.Handle(textCtx, command, b.username)
		if err == nil {
			for _, message := range messages {
				if err = b.api.SendMessage(textCtx, q.Chat.ID, message); err != nil {
					break
				}
			}
		}
		if err != nil {
			return b.answerWidget(ctx, api, q.ID, `{"error":"Не удалось отправить текст. Попробуйте позже."}`)
		}
		return b.answerWidget(ctx, api, q.ID, `{"notice":"Расписание отправлено текстом."}`)
	}
	readCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	data, err := b.handler.widgetData(readCtx, q.Data)
	cancel()
	if err != nil {
		b.logger.Warn("Widget query failed", "error", err)
		data = `{"error":"Не удалось загрузить данные. Попробуйте ещё раз."}`
	}
	return b.answerWidget(ctx, api, q.ID, data)
}
func (b *Bot) answerWidget(ctx context.Context, api WidgetAPI, id, data string) error {
	answerCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	if err := api.AnswerWidgetQuery(answerCtx, id, data); err != nil {
		// Expired queries must not restart polling and replay the same stale update.
		b.logger.Warn("Widget answer failed", "error", err)
	}
	return nil
}

func widgetLessonCount(n int) string {
	if n == 0 {
		return "свободно"
	}
	word := "пар"
	if n%10 == 1 && n%100 != 11 {
		word = "пара"
	} else if n%10 >= 2 && n%10 <= 4 && (n%100 < 12 || n%100 > 14) {
		word = "пары"
	}
	return fmt.Sprintf("%d %s", n, word)
}
