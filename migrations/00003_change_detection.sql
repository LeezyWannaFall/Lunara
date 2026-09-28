-- +goose Up
CREATE TABLE schedule_candidates (
    group_id BIGINT NOT NULL,
    week_start DATE NOT NULL,
    snapshot_id BIGINT NOT NULL,
    first_seen_at TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL,
    observation_count INTEGER NOT NULL CHECK (observation_count > 0),
    quarantined BOOLEAN NOT NULL DEFAULT false,
    quarantine_reason TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (group_id, week_start),
    FOREIGN KEY (snapshot_id, group_id, week_start)
        REFERENCES schedule_snapshots (id, group_id, week_start)
);

CREATE TABLE change_sets (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    group_id BIGINT NOT NULL CHECK (group_id > 0),
    detected_at TIMESTAMPTZ NOT NULL,
    week_starts DATE[] NOT NULL CHECK (cardinality(week_starts) > 0),
    kind TEXT NOT NULL CHECK (kind IN ('regular', 'first_publication', 'parser_rebaseline')),
    changes JSONB NOT NULL CHECK (jsonb_typeof(changes) = 'array'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX change_sets_group_history_idx ON change_sets (group_id, id DESC);

-- +goose Down
DROP TABLE change_sets;
DROP TABLE schedule_candidates;
