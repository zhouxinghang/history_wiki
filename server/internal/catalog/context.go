package catalog

import (
	"errors"
	"sort"
	"strings"
	"time"
)

var (
	ErrInvalidPlace                    = errors.New("invalid place")
	ErrPlaceNotFound                   = errors.New("place not found")
	ErrPlaceNotWritable                = errors.New("place is not writable")
	ErrHistoricalPeriodNotFound        = errors.New("historical period not found")
	ErrHistoricalPeriodNotWritable     = errors.New("historical period is not writable")
	ErrInvalidHistoricalPeriod         = errors.New("invalid historical period")
	ErrInvalidRegionAssociation        = errors.New("invalid region association")
	ErrPlaceVersionConflict            = errors.New("place version conflict")
	ErrHistoricalPeriodVersionConflict = errors.New("historical period version conflict")
)

type RegionReference struct {
	ID                  string
	Name                string
	DisambiguationLabel *string
}

type Place struct {
	ID                  string
	Name                string
	DisambiguationLabel *string
	Status              EntityStatus
	MergedIntoID        *string
	RegionIDs           []string
	Regions             []RegionReference
	LockVersion         int64
	CreatedBy           string
	UpdatedBy           string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type HistoricalPeriod struct {
	ID                  string
	Name                string
	DisambiguationLabel *string
	Status              EntityStatus
	MergedIntoID        *string
	RegionIDs           []string
	Regions             []RegionReference
	LockVersion         int64
	CreatedBy           string
	UpdatedBy           string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type ContextEntityListFilter struct {
	Query  string
	Status EntityStatus
	Limit  int
}

type PlaceUpdate struct {
	Name                string
	DisambiguationLabel *string
	Status              EntityStatus
	RegionIDs           []string
	ExpectedVersion     int64
	UpdatedBy           string
	UpdatedAt           time.Time
}

type HistoricalPeriodUpdate struct {
	Name                string
	DisambiguationLabel *string
	Status              EntityStatus
	RegionIDs           []string
	ExpectedVersion     int64
	UpdatedBy           string
	UpdatedAt           time.Time
}

func NormalizePlaceFields(name string, disambiguationLabel *string) (string, *string, error) {
	name, disambiguationLabel, err := NormalizeRegionFields(name, disambiguationLabel)
	if err != nil {
		return "", nil, ErrInvalidPlace
	}
	return name, disambiguationLabel, nil
}

func NormalizeHistoricalPeriodFields(name string, disambiguationLabel *string) (string, *string, error) {
	name, disambiguationLabel, err := NormalizeRegionFields(name, disambiguationLabel)
	if err != nil {
		return "", nil, ErrInvalidHistoricalPeriod
	}
	return name, disambiguationLabel, nil
}

// NormalizeRegionIDs removes duplicates and returns a stable ordering so that
// request hashes and audit details do not depend on form selection order.
func NormalizeRegionIDs(regionIDs []string) []string {
	unique := make(map[string]struct{}, len(regionIDs))
	for _, regionID := range regionIDs {
		regionID = strings.TrimSpace(regionID)
		if regionID != "" {
			unique[regionID] = struct{}{}
		}
	}
	normalized := make([]string, 0, len(unique))
	for regionID := range unique {
		normalized = append(normalized, regionID)
	}
	sort.Strings(normalized)
	return normalized
}
