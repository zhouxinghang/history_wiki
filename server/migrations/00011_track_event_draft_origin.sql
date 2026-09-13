-- +goose Up
ALTER TABLE event_drafts
    ADD COLUMN based_on_revision_no INTEGER CHECK (based_on_revision_no >= 1),
    ADD CONSTRAINT event_drafts_based_on_revision_fk
        FOREIGN KEY (event_id, based_on_revision_no)
        REFERENCES event_revisions (event_id, revision_no)
        ON DELETE RESTRICT;

-- +goose Down
ALTER TABLE event_drafts
    DROP CONSTRAINT event_drafts_based_on_revision_fk,
    DROP COLUMN based_on_revision_no;
