package telegram

import (
	"context"
	"io"
	"log/slog"
	"testing"
)

type fakeAPI struct{ sent []int64 }

func (f *fakeAPI) GetMe(context.Context) (User, error)                 { return User{Username: "LunaraBot"}, nil }
func (f *fakeAPI) GetUpdates(context.Context, int64) ([]Update, error) { return nil, nil }
func (f *fakeAPI) SendMessage(_ context.Context, chatID int64, _ string) error {
	f.sent = append(f.sent, chatID)
	return nil
}

type fakeState struct{}

func (fakeState) TelegramOffset(context.Context) (int64, error)   { return 0, nil }
func (fakeState) SaveTelegramOffset(context.Context, int64) error { return nil }

type recordingState struct{ offsets []int64 }

func (r *recordingState) TelegramOffset(context.Context) (int64, error) { return 5, nil }
func (r *recordingState) SaveTelegramOffset(_ context.Context, offset int64) error {
	r.offsets = append(r.offsets, offset)
	return nil
}

type pollingAPI struct {
	fakeAPI
	calls  int
	cancel context.CancelFunc
}

func (p *pollingAPI) GetUpdates(ctx context.Context, offset int64) ([]Update, error) {
	p.calls++
	if p.calls == 1 {
		return []Update{{ID: 5, Message: &Message{Chat: Chat{ID: 1, Type: "private"}, Text: "/help"}}, {ID: 6}}, nil
	}
	p.cancel()
	return nil, ctx.Err()
}

func TestChatAccess(t *testing.T) {
	h := testHandler(t, nil)
	api := &fakeAPI{}
	bot, err := NewBot(context.Background(), api, h, fakeState{}, -100123, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	for _, update := range []Update{
		{ID: 1, Message: &Message{Chat: Chat{ID: 10, Type: "private"}, Text: "/help"}},
		{ID: 2, Message: &Message{Chat: Chat{ID: -100123, Type: "supergroup"}, Text: "/help"}},
		{ID: 3, Message: &Message{Chat: Chat{ID: -999, Type: "group"}, Text: "/help"}},
	} {
		if err := bot.handleUpdate(context.Background(), update); err != nil {
			t.Fatal(err)
		}
	}
	if len(api.sent) != 2 || api.sent[0] != 10 || api.sent[1] != -100123 {
		t.Fatalf("sent to wrong chats: %v", api.sent)
	}
}

func TestRunPersistsOffsetAfterEachUpdate(t *testing.T) {
	h := testHandler(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	api := &pollingAPI{cancel: cancel}
	state := &recordingState{}
	bot, err := NewBot(ctx, api, h, state, 0, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := bot.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if len(state.offsets) != 2 || state.offsets[0] != 6 || state.offsets[1] != 7 {
		t.Fatalf("offsets=%v", state.offsets)
	}
}
