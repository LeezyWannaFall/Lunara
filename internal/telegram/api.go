// Package telegram implements the small Bot API surface used by Lunara.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type Message struct {
	MessageID int64  `json:"message_id"`
	Chat      Chat   `json:"chat"`
	Text      string `json:"text"`
}

type Update struct {
	ID            int64          `json:"update_id"`
	Message       *Message       `json:"message,omitempty"`
	CallbackQuery *CallbackQuery `json:"callback_query,omitempty"`
}

type CallbackQuery struct {
	ID      string   `json:"id"`
	Message *Message `json:"message,omitempty"`
	Data    string   `json:"data"`
}

type InlineButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

type API interface {
	GetMe(context.Context) (User, error)
	GetUpdates(context.Context, int64) ([]Update, error)
	SendMessage(context.Context, int64, string) error
	SendMessageWithButtons(context.Context, int64, string, [][]InlineButton) error
	AnswerCallback(context.Context, string) error
}

type Client struct {
	baseURL string
	http    *http.Client
}

func NewClient(token string, httpClient *http.Client, baseURL string) (*Client, error) {
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("Telegram bot token is required")
	}
	if baseURL == "" {
		baseURL = "https://api.telegram.org"
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 40 * time.Second}
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/") + "/bot" + token, http: httpClient}, nil
}

type response[T any] struct {
	OK          bool   `json:"ok"`
	Result      T      `json:"result"`
	Description string `json:"description"`
	ErrorCode   int    `json:"error_code"`
	Parameters  struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

type APIError struct {
	Code        int
	Description string
	RetryAfter  time.Duration
}

func (e *APIError) Error() string {
	return fmt.Sprintf("Telegram API error %d: %s", e.Code, e.Description)
}

type RequestError struct{ Method string }

func (e *RequestError) Error() string { return "Telegram " + e.Method + " request failed" }

func RetryAfter(err error) (time.Duration, bool) {
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.RetryAfter > 0 {
		return apiErr.RetryAfter, true
	}
	return 0, false
}

func Temporary(err error) bool {
	var apiErr *APIError
	var requestErr *RequestError
	return errors.As(err, &requestErr) || errors.As(err, &apiErr) && (apiErr.Code == 429 || apiErr.Code >= 500)
}

func (c *Client) call(ctx context.Context, method string, request any, result any) error {
	body, err := json.Marshal(request)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/"+method, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		// net/http errors may contain the request URL, which includes the bot token.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &RequestError{Method: method}
	}
	defer res.Body.Close()
	limited, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("Telegram %s response: %w", method, err)
	}
	var envelope response[json.RawMessage]
	if err := json.Unmarshal(limited, &envelope); err != nil {
		if res.StatusCode != http.StatusOK {
			return &APIError{Code: res.StatusCode, Description: http.StatusText(res.StatusCode)}
		}
		return fmt.Errorf("Telegram %s invalid response", method)
	}
	if res.StatusCode != http.StatusOK || !envelope.OK {
		code := envelope.ErrorCode
		if code == 0 {
			code = res.StatusCode
		}
		return &APIError{Code: code, Description: envelope.Description, RetryAfter: time.Duration(envelope.Parameters.RetryAfter) * time.Second}
	}
	if result != nil && json.Unmarshal(envelope.Result, result) != nil {
		return fmt.Errorf("Telegram %s invalid result", method)
	}
	return nil
}

func (c *Client) GetMe(ctx context.Context) (User, error) {
	var user User
	err := c.call(ctx, "getMe", struct{}{}, &user)
	return user, err
}

func (c *Client) GetUpdates(ctx context.Context, offset int64) ([]Update, error) {
	var updates []Update
	err := c.call(ctx, "getUpdates", map[string]any{
		"offset": offset, "limit": 100, "timeout": 30,
		"allowed_updates": []string{"message", "callback_query"},
	}, &updates)
	return updates, err
}

func (c *Client) SendMessage(ctx context.Context, chatID int64, text string) error {
	return c.SendMessageWithButtons(ctx, chatID, text, nil)
}

func (c *Client) SendMessageWithButtons(ctx context.Context, chatID int64, text string, buttons [][]InlineButton) error {
	_, err := c.sendMessage(ctx, chatID, text, buttons)
	return err
}

func (c *Client) SendNotification(ctx context.Context, chatID int64, text string) (int64, error) {
	message, err := c.sendMessage(ctx, chatID, text, nil)
	return message.MessageID, err
}

func (c *Client) sendMessage(ctx context.Context, chatID int64, text string, buttons [][]InlineButton) (Message, error) {
	if text == "" || len([]rune(text)) > 4096 {
		return Message{}, fmt.Errorf("Telegram message length is outside 1..4096")
	}
	request := map[string]any{
		"chat_id": chatID, "text": text, "parse_mode": "HTML",
	}
	if len(buttons) > 0 {
		request["reply_markup"] = map[string]any{"inline_keyboard": buttons}
	}
	var message Message
	err := c.call(ctx, "sendMessage", request, &message)
	return message, err
}

func (c *Client) AnswerCallback(ctx context.Context, id string) error {
	return c.call(ctx, "answerCallbackQuery", map[string]string{"callback_query_id": id}, nil)
}
