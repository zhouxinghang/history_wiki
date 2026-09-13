-- +goose Up
CREATE TABLE places (
    id UUID PRIMARY KEY,
    name TEXT NOT NULL CHECK (btrim(name) <> '' AND char_length(name) <= 120),
    disambiguation_label TEXT CHECK (
        disambiguation_label IS NULL OR
        (btrim(disambiguation_label) <> '' AND char_length(disambiguation_label) <= 120)
    ),
    status canonical_entity_status NOT NULL DEFAULT 'active',
    merged_into_id UUID REFERENCES places (id) ON DELETE RESTRICT,
    lock_version BIGINT NOT NULL DEFAULT 1 CHECK (lock_version >= 1),
    created_by UUID NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    updated_by UUID NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK ((status = 'merged') = (merged_into_id IS NOT NULL)),
    CHECK (merged_into_id IS NULL OR merged_into_id <> id)
);

CREATE INDEX places_name_idx ON places (lower(name), lower(COALESCE(disambiguation_label, '')), id);
CREATE INDEX places_status_idx ON places (status, lower(name), id);

CREATE TRIGGER places_immutable_id
BEFORE UPDATE OF id ON places
FOR EACH ROW EXECUTE FUNCTION reject_id_change();

CREATE TABLE place_regions (
    place_id UUID NOT NULL REFERENCES places (id) ON DELETE CASCADE,
    region_id UUID NOT NULL REFERENCES regions (id) ON DELETE RESTRICT,
    PRIMARY KEY (place_id, region_id)
);

CREATE INDEX place_regions_region_idx ON place_regions (region_id, place_id);

CREATE TABLE historical_periods (
    id UUID PRIMARY KEY,
    name TEXT NOT NULL CHECK (btrim(name) <> '' AND char_length(name) <= 120),
    disambiguation_label TEXT CHECK (
        disambiguation_label IS NULL OR
        (btrim(disambiguation_label) <> '' AND char_length(disambiguation_label) <= 120)
    ),
    status canonical_entity_status NOT NULL DEFAULT 'active',
    merged_into_id UUID REFERENCES historical_periods (id) ON DELETE RESTRICT,
    lock_version BIGINT NOT NULL DEFAULT 1 CHECK (lock_version >= 1),
    created_by UUID NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    updated_by UUID NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK ((status = 'merged') = (merged_into_id IS NOT NULL)),
    CHECK (merged_into_id IS NULL OR merged_into_id <> id)
);

CREATE INDEX historical_periods_name_idx ON historical_periods (lower(name), lower(COALESCE(disambiguation_label, '')), id);
CREATE INDEX historical_periods_status_idx ON historical_periods (status, lower(name), id);

CREATE TRIGGER historical_periods_immutable_id
BEFORE UPDATE OF id ON historical_periods
FOR EACH ROW EXECUTE FUNCTION reject_id_change();

CREATE TABLE period_regions (
    historical_period_id UUID NOT NULL REFERENCES historical_periods (id) ON DELETE CASCADE,
    region_id UUID NOT NULL REFERENCES regions (id) ON DELETE RESTRICT,
    PRIMARY KEY (historical_period_id, region_id)
);

CREATE INDEX period_regions_region_idx
ON period_regions (region_id, historical_period_id);

-- New relationships can only point at active regions. Existing relationships
-- remain readable if an administrator later retires a region.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION require_active_associated_region()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM regions
        WHERE id = NEW.region_id AND status = 'active'
    ) THEN
        RAISE EXCEPTION 'associated region must exist and be active';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER place_regions_require_active_region
BEFORE INSERT OR UPDATE OF region_id ON place_regions
FOR EACH ROW EXECUTE FUNCTION require_active_associated_region();

CREATE TRIGGER period_regions_require_active_region
BEFORE INSERT OR UPDATE OF region_id ON period_regions
FOR EACH ROW EXECUTE FUNCTION require_active_associated_region();

-- The check is deferred so a period and its region rows can be created, or all
-- relationships can be replaced, in one transaction without an invalid
-- intermediate state becoming observable.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION require_region_for_active_historical_period()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.status = 'active' AND NOT EXISTS (
        SELECT 1 FROM period_regions
        WHERE historical_period_id = NEW.id
    ) THEN
        RAISE EXCEPTION 'active historical period must have at least one region';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION preserve_active_historical_period_region()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    period_id UUID;
    period_status canonical_entity_status;
BEGIN
    IF TG_OP = 'DELETE' THEN
        period_id := OLD.historical_period_id;
    ELSE
        period_id := NEW.historical_period_id;
    END IF;

    SELECT status INTO period_status
    FROM historical_periods
    WHERE id = period_id;

    IF FOUND AND period_status = 'active' AND NOT EXISTS (
        SELECT 1 FROM period_regions
        WHERE historical_period_id = period_id
    ) THEN
        RAISE EXCEPTION 'active historical period must have at least one region';
    END IF;
    RETURN COALESCE(NEW, OLD);
END;
$$;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER historical_periods_require_region
AFTER INSERT OR UPDATE ON historical_periods
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION require_region_for_active_historical_period();

CREATE CONSTRAINT TRIGGER period_regions_preserve_region
AFTER INSERT OR UPDATE OR DELETE ON period_regions
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION preserve_active_historical_period_region();

-- +goose Down
DROP TRIGGER period_regions_preserve_region ON period_regions;
DROP TRIGGER historical_periods_require_region ON historical_periods;
DROP FUNCTION preserve_active_historical_period_region();
DROP FUNCTION require_region_for_active_historical_period();
DROP TRIGGER period_regions_require_active_region ON period_regions;
DROP TRIGGER place_regions_require_active_region ON place_regions;
DROP FUNCTION require_active_associated_region();
DROP TABLE period_regions;
DROP TRIGGER historical_periods_immutable_id ON historical_periods;
DROP TABLE historical_periods;
DROP TABLE place_regions;
DROP TRIGGER places_immutable_id ON places;
DROP TABLE places;
