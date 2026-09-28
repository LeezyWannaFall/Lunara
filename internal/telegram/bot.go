package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

type OffsetStore interface {
	TelegramOffset(context.Context) (int64, error)
	SaveTelegramOffset(context.Context, int64) error
}

type Bot struct {
	api      API
	handler  *Handler
	state    OffsetStore
	chatID   int64
	username string
	logger   *slog.Logger
}

func NewBot(ctx context.Context, api API, handler *Handler, state OffsetStore, chatID int64, logger *slog.Logger) (*Bot, error) {
	if api == nil || handler == nil || state == nil {
		return nil, fmt.Errorf("Telegram API, handler and state store are required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	me, err := api.GetMe(ctx)
	if err != nil {
		return nil, err
	}
	if me.Username == "" {
		return nil, fmt.Errorf("Telegram bot username is empty")
	}
	return &Bot{api: api, handler: handler, state: state, chatID: chatID, username: me.Username, logger: logger}, nil
}

func (b *Bot) Run(ctx context.Context) error {
	offset, err := b.state.TelegramOffset(ctx)
	if err != nil {
		return err
	}
	for {
		updates, err := b.api.GetUpdates(ctx, offset)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			b.logger.Warn("Telegram polling failed", "error", err)
			select {
			case <-time.After(time.Second):
				continue
			case <-ctx.Done():
				return nil
			}
		}
		for _, update := range updates {
			if update.ID < offset {
				continue
			}
			if err := b.handleUpdate(ctx, update); err != nil {
				return err
			}
			offset = update.ID + 1
			if err := b.state.SaveTelegramOffset(ctx, offset); err != nil {
				return err
			}
		}
	}
}

func (b *Bot) handleUpdate(ctx context.Context, update Update) error {
	if update.Message == nil || update.Message.Text == "" {
		return nil
	}
	chat := update.Message.Chat
	if chat.Type != "private" && (b.chatID == 0 || chat.ID != b.chatID) {
		return nil
	}
	messages, err := b.handler.Handle(ctx, update.Message.Text, b.username)
	if err != nil {
		b.logger.Error("Telegram command failed", "error", err, "chat_id", chat.ID)
		return b.api.SendMessage(ctx, chat.ID, "Не удалось прочитать расписание. Попробуйте ещё раз позже.")
	}
	for _, message := range messages {
		if err := b.api.SendMessage(ctx, chat.ID, message); err != nil {
			return err
		}
	}
	return nil
}
