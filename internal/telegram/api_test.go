package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestClientBotAPIRequests(t *testing.T) {
	var methods []string
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		methods = append(methods, req.URL.Path)
		body, _ := io.ReadAll(req.Body)
		if strings.HasSuffix(req.URL.Path, "/getUpdates") {
			var request map[string]any
			if err := json.Unmarshal(body, &request); err != nil || request["offset"] != float64(42) || request["timeout"] != float64(30) {
				t.Fatalf("bad getUpdates request: %s", body)
			}
			return jsonResponse(`{"ok":true,"result":[{"update_id":42,"message":{"message_id":1,"chat":{"id":7,"type":"private"},"text":"/help"}}]}`), nil
		}
		if strings.HasSuffix(req.URL.Path, "/sendMessage") && (!strings.Contains(string(body), `"parse_mode":"HTML"`) || !strings.Contains(string(body), `"chat_id":7`)) {
			t.Fatalf("bad sendMessage request: %s", body)
		}
		if strings.HasSuffix(req.URL.Path, "/sendMessage") {
			return jsonResponse(`{"ok":true,"result":{"message_id":99,"chat":{"id":7,"type":"private"},"text":"hello"}}`), nil
		}
		return jsonResponse(`{"ok":true,"result":true}`), nil
	})
	client, err := NewClient("secret", &http.Client{Transport: transport}, "https://telegram.test")
	if err != nil {
		t.Fatal(err)
	}
	updates, err := client.GetUpdates(context.Background(), 42)
	if err != nil || len(updates) != 1 || updates[0].ID != 42 {
		t.Fatalf("updates=%v err=%v", updates, err)
	}
	if err := client.SendMessage(context.Background(), 7, "hello"); err != nil {
		t.Fatal(err)
	}
	if len(methods) != 2 || methods[0] != "/botsecret/getUpdates" || methods[1] != "/botsecret/sendMessage" {
		t.Fatalf("methods=%v", methods)
	}
}

func jsonResponse(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func TestClientParsesTelegramRetryAfter(t *testing.T) {
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		response := jsonResponse(`{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":12}}`)
		response.StatusCode = http.StatusTooManyRequests
		return response, nil
	})
	client, _ := NewClient("secret", &http.Client{Transport: transport}, "https://telegram.test")
	_, err := client.SendNotification(context.Background(), 7, "hello")
	if delay, ok := RetryAfter(err); !ok || delay != 12*time.Second || !Temporary(err) {
		t.Fatalf("delay=%v ok=%v err=%v", delay, ok, err)
	}
}
