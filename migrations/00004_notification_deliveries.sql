-- +goose Up
CREATE TABLE notification_deliveries (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    chat_id BIGINT NOT NULL,
    change_set_id BIGINT NOT NULL REFERENCES change_sets(id),
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'sending', 'sent', 'failed')),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    lease_until TIMESTAMPTZ,
    last_error TEXT NOT NULL DEFAULT '',
    telegram_message_id BIGINT,
    sent_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (chat_id, change_set_id)
);

CREATE INDEX notification_deliveries_ready_idx
    ON notification_deliveries (next_attempt_at, id)
    WHERE status IN ('pending', 'sending');

-- +goose Down
DROP TABLE notification_deliveries;
