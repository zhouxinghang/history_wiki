-- +goose Up
-- Keep every retired identifier one hop from an active entity.  Reads can
-- therefore resolve historical references without rewriting immutable event
-- revisions, while direct SQL writes cannot create cycles or growing chains.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION enforce_canonical_entity_merge()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    target_status canonical_entity_status;
    target_parent UUID;
    has_incoming_alias BOOLEAN;
BEGIN
    IF NEW.status <> 'merged' THEN
        IF NEW.merged_into_id IS NOT NULL THEN
            RAISE EXCEPTION 'non-merged canonical entity cannot have a merge target';
        END IF;
        IF NEW.status <> 'active' THEN
            EXECUTE format(
                'SELECT EXISTS (SELECT 1 FROM %I WHERE merged_into_id = $1)',
                TG_TABLE_NAME
            ) INTO has_incoming_alias USING NEW.id;
            IF has_incoming_alias THEN
                RAISE EXCEPTION 'canonical entity with aliases must remain active';
            END IF;
        END IF;
        RETURN NEW;
    END IF;

    IF NEW.merged_into_id IS NULL OR NEW.merged_into_id = NEW.id THEN
        RAISE EXCEPTION 'merged canonical entity requires another target';
    END IF;

    EXECUTE format(
        'SELECT status, merged_into_id FROM %I WHERE id = $1 FOR KEY SHARE',
        TG_TABLE_NAME
    ) INTO target_status, target_parent USING NEW.merged_into_id;
    IF target_status IS NULL OR target_status <> 'active' OR target_parent IS NOT NULL THEN
        RAISE EXCEPTION 'canonical entity merge target must be active and final';
    END IF;

    EXECUTE format(
        'SELECT EXISTS (SELECT 1 FROM %I WHERE merged_into_id = $1 AND id <> $2)',
        TG_TABLE_NAME
    ) INTO has_incoming_alias USING NEW.id, NEW.id;
    IF has_incoming_alias THEN
        RAISE EXCEPTION 'canonical entity aliases must be compressed before merge';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER regions_valid_merge
BEFORE INSERT OR UPDATE OF status, merged_into_id ON regions
FOR EACH ROW EXECUTE FUNCTION enforce_canonical_entity_merge();
CREATE TRIGGER places_valid_merge
BEFORE INSERT OR UPDATE OF status, merged_into_id ON places
FOR EACH ROW EXECUTE FUNCTION enforce_canonical_entity_merge();
CREATE TRIGGER historical_periods_valid_merge
BEFORE INSERT OR UPDATE OF status, merged_into_id ON historical_periods
FOR EACH ROW EXECUTE FUNCTION enforce_canonical_entity_merge();
CREATE TRIGGER historical_figures_valid_merge
BEFORE INSERT OR UPDATE OF status, merged_into_id ON historical_figures
FOR EACH ROW EXECUTE FUNCTION enforce_canonical_entity_merge();
CREATE TRIGGER topic_tags_valid_merge
BEFORE INSERT OR UPDATE OF status, merged_into_id ON topic_tags
FOR EACH ROW EXECUTE FUNCTION enforce_canonical_entity_merge();

-- +goose Down
DROP TRIGGER topic_tags_valid_merge ON topic_tags;
DROP TRIGGER historical_figures_valid_merge ON historical_figures;
DROP TRIGGER historical_periods_valid_merge ON historical_periods;
DROP TRIGGER places_valid_merge ON places;
DROP TRIGGER regions_valid_merge ON regions;
DROP FUNCTION enforce_canonical_entity_merge();
