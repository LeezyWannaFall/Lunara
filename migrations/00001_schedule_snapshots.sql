-- +goose Up
CREATE TABLE schedule_snapshots (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    group_id BIGINT NOT NULL CHECK (group_id > 0),
    week_start DATE NOT NULL CHECK (EXTRACT(ISODOW FROM week_start) = 1),
    week_type TEXT NOT NULL CHECK (week_type IN ('upper', 'lower')),
    status TEXT NOT NULL CHECK (status IN ('published', 'unpublished')),
    lessons JSONB NOT NULL CHECK (jsonb_typeof(lessons) = 'array'),
    normalization_version INTEGER NOT NULL CHECK (normalization_version > 0),
    content_hash TEXT NOT NULL CHECK (content_hash ~ '^[0-9a-f]{64}$'),
    checked_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((status = 'published' AND jsonb_array_length(lessons) > 0)
        OR (status = 'unpublished' AND jsonb_array_length(lessons) = 0)),
    UNIQUE (group_id, week_start, normalization_version, content_hash),
    UNIQUE (id, group_id, week_start)
);

CREATE TABLE schedule_heads (
    group_id BIGINT NOT NULL,
    week_start DATE NOT NULL,
    snapshot_id BIGINT NOT NULL,
    PRIMARY KEY (group_id, week_start),
    FOREIGN KEY (snapshot_id, group_id, week_start)
        REFERENCES schedule_snapshots (id, group_id, week_start)
);

-- +goose Down
DROP TABLE schedule_heads;
DROP TABLE schedule_snapshots;
