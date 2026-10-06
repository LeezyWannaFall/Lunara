-- +goose Up
ALTER TABLE change_sets DROP CONSTRAINT change_sets_kind_check;
ALTER TABLE change_sets ADD CONSTRAINT change_sets_kind_check
    CHECK (kind IN ('regular', 'major_change', 'first_publication', 'parser_rebaseline'));

ALTER TABLE schedule_candidates DROP COLUMN quarantined;
ALTER TABLE schedule_candidates DROP COLUMN quarantine_reason;

-- +goose Down
ALTER TABLE schedule_candidates ADD COLUMN quarantined BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE schedule_candidates ADD COLUMN quarantine_reason TEXT NOT NULL DEFAULT '';

UPDATE change_sets SET kind = 'regular' WHERE kind = 'major_change';
ALTER TABLE change_sets DROP CONSTRAINT change_sets_kind_check;
ALTER TABLE change_sets ADD CONSTRAINT change_sets_kind_check
    CHECK (kind IN ('regular', 'first_publication', 'parser_rebaseline'));
