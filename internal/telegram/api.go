// Package telegram implements the small Bot API surface used by Lunara.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
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
	ID      int64    `json:"update_id"`
	Message *Message `json:"message,omitempty"`
}

type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

type API interface {
	GetMe(context.Context) (User, error)
	GetUpdates(context.Context, int64) ([]Update, error)
	SendMessage(context.Context, int64, string) error
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
		return fmt.Errorf("Telegram %s request failed", method)
	}
	defer res.Body.Close()
	limited, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("Telegram %s response: %w", method, err)
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("Telegram %s HTTP status %d", method, res.StatusCode)
	}
	var envelope response[json.RawMessage]
	if err := json.Unmarshal(limited, &envelope); err != nil {
		return fmt.Errorf("Telegram %s invalid response", method)
	}
	if !envelope.OK {
		return fmt.Errorf("Telegram %s rejected: %s", method, envelope.Description)
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
		"allowed_updates": []string{"message"},
	}, &updates)
	return updates, err
}

func (c *Client) SendMessage(ctx context.Context, chatID int64, text string) error {
	if text == "" || len([]rune(text)) > 4096 {
		return fmt.Errorf("Telegram message length is outside 1..4096")
	}
	return c.call(ctx, "sendMessage", map[string]any{
		"chat_id": chatID, "text": text, "parse_mode": "HTML",
	}, nil)
}
