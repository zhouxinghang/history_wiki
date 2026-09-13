package catalog

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxRegionNameLength          = 120
	MaxDisambiguationLabelLength = 120
	MaxRegionSearchLength        = 100
	MaxIdempotencyKeyLength      = 200
)

type EntityStatus string

const (
	StatusActive   EntityStatus = "active"
	StatusInactive EntityStatus = "inactive"
	StatusMerged   EntityStatus = "merged"
)

var (
	ErrInvalidRegion         = errors.New("invalid region")
	ErrRegionNotFound        = errors.New("region not found")
	ErrVersionConflict       = errors.New("region version conflict")
	ErrRegionNotWritable     = errors.New("region is not writable")
	ErrIdempotencyConflict   = errors.New("idempotency key was reused with another request")
	ErrIdempotencyKeyMissing = errors.New("idempotency key is required")
)

type Region struct {
	ID                  string
	Name                string
	DisambiguationLabel *string
	Status              EntityStatus
	MergedIntoID        *string
	LockVersion         int64
	CreatedBy           string
	UpdatedBy           string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type RegionListFilter struct {
	Query  string
	Status EntityStatus
	Limit  int
}

type RegionUpdate struct {
	Name                string
	DisambiguationLabel *string
	Status              EntityStatus
	ExpectedVersion     int64
	UpdatedBy           string
	UpdatedAt           time.Time
}

func NormalizeRegionFields(name string, disambiguationLabel *string) (string, *string, error) {
	normalizedName := strings.TrimSpace(name)
	if normalizedName == "" || utf8.RuneCountInString(normalizedName) > MaxRegionNameLength {
		return "", nil, ErrInvalidRegion
	}
	if disambiguationLabel == nil {
		return normalizedName, nil, nil
	}
	normalizedLabel := strings.TrimSpace(*disambiguationLabel)
	if normalizedLabel == "" {
		return normalizedName, nil, nil
	}
	if utf8.RuneCountInString(normalizedLabel) > MaxDisambiguationLabelLength {
		return "", nil, ErrInvalidRegion
	}
	return normalizedName, &normalizedLabel, nil
}

func (status EntityStatus) ValidFilter() bool {
	return status == "" || status == StatusActive || status == StatusInactive || status == StatusMerged
}

func (status EntityStatus) Writable() bool {
	return status == StatusActive || status == StatusInactive
}

func NormalizeIdempotencyKey(value string) (string, error) {
	key := strings.TrimSpace(value)
	if key == "" {
		return "", ErrIdempotencyKeyMissing
	}
	if len(key) > MaxIdempotencyKeyLength {
		return "", ErrInvalidRegion
	}
	return key, nil
}
