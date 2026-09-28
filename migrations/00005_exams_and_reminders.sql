-- +goose Up
CREATE TABLE exam_schedules (
    group_id BIGINT PRIMARY KEY,
    status TEXT NOT NULL CHECK (status IN ('published','unpublished')),
    exams JSONB NOT NULL,
    normalization_version INTEGER NOT NULL,
    content_hash TEXT NOT NULL,
    checked_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE reminder_deliveries (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    chat_id BIGINT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('tomorrow','next_week')),
    period_date DATE NOT NULL,
    text TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','sending','sent','failed')),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    lease_until TIMESTAMPTZ,
    last_error TEXT NOT NULL DEFAULT '',
    telegram_message_id BIGINT,
    sent_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (chat_id, kind, period_date)
);
CREATE INDEX reminder_deliveries_ready_idx ON reminder_deliveries(next_attempt_at,id) WHERE status IN ('pending','sending');

-- +goose Down
DROP TABLE reminder_deliveries;
DROP TABLE exam_schedules;
