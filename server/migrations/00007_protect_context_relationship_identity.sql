-- +goose Up
-- Relationship rows have no mutable attributes. Replacing their composite
-- primary key with UPDATE can bypass the deferred check of the old historical
-- period, so relationship changes must use DELETE plus INSERT in one
-- transaction instead.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION reject_context_relationship_update()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION '% relationship identity is immutable', TG_TABLE_NAME;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER place_regions_immutable_identity
BEFORE UPDATE ON place_regions
FOR EACH ROW EXECUTE FUNCTION reject_context_relationship_update();

CREATE TRIGGER period_regions_immutable_identity
BEFORE UPDATE ON period_regions
FOR EACH ROW EXECUTE FUNCTION reject_context_relationship_update();

-- +goose Down
DROP TRIGGER period_regions_immutable_identity ON period_regions;
DROP TRIGGER place_regions_immutable_identity ON place_regions;
DROP FUNCTION reject_context_relationship_update();
