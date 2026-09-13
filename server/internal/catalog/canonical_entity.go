package catalog

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxCanonicalEntityNameLength   = 120
	MaxCanonicalEntitySearchLength = 100
)

type CanonicalEntityKind string

const (
	KindRegion           CanonicalEntityKind = "region"
	KindPlace            CanonicalEntityKind = "place"
	KindHistoricalPeriod CanonicalEntityKind = "historical_period"
	KindHistoricalFigure CanonicalEntityKind = "historical_figure"
	KindTopicTag         CanonicalEntityKind = "topic_tag"
)

var (
	ErrInvalidCanonicalEntity     = errors.New("invalid canonical entity")
	ErrCanonicalEntityNotFound    = errors.New("canonical entity not found")
	ErrCanonicalEntityNotWritable = errors.New("canonical entity is not writable")
	ErrInvalidMergeTarget         = errors.New("invalid canonical entity merge target")
	ErrMergeImpactChanged         = errors.New("canonical entity merge impact changed")
)

type CanonicalEntity struct {
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

type CanonicalEntityListFilter struct {
	Query  string
	Status EntityStatus
	Limit  int
}

type CanonicalEntityUpdate struct {
	Name                string
	DisambiguationLabel *string
	Status              EntityStatus
	ExpectedVersion     int64
	UpdatedBy           string
	UpdatedAt           time.Time
}

type EntityMergeImpact struct {
	DraftCount          int `json:"draftCount"`
	RevisionCount       int `json:"revisionCount"`
	PublishedEventCount int `json:"publishedEventCount"`
}

func (impact EntityMergeImpact) Equal(other EntityMergeImpact) bool {
	return impact == other
}

type CanonicalEntityMerge struct {
	TargetID        string
	ExpectedVersion int64
	ConfirmedImpact EntityMergeImpact
	UpdatedBy       string
	UpdatedAt       time.Time
}

func (kind CanonicalEntityKind) Valid() bool {
	switch kind {
	case KindRegion, KindPlace, KindHistoricalPeriod, KindHistoricalFigure, KindTopicTag:
		return true
	default:
		return false
	}
}

func (kind CanonicalEntityKind) String() string {
	return string(kind)
}

func NormalizeCanonicalEntityFields(name string, disambiguationLabel *string) (string, *string, error) {
	normalizedName := strings.TrimSpace(name)
	if normalizedName == "" || utf8.RuneCountInString(normalizedName) > MaxCanonicalEntityNameLength {
		return "", nil, ErrInvalidCanonicalEntity
	}
	if disambiguationLabel == nil {
		return normalizedName, nil, nil
	}
	normalizedLabel := strings.TrimSpace(*disambiguationLabel)
	if normalizedLabel == "" {
		return normalizedName, nil, nil
	}
	if utf8.RuneCountInString(normalizedLabel) > MaxDisambiguationLabelLength {
		return "", nil, ErrInvalidCanonicalEntity
	}
	return normalizedName, &normalizedLabel, nil
}

func CanonicalEntityTable(kind CanonicalEntityKind) (string, error) {
	switch kind {
	case KindRegion:
		return "regions", nil
	case KindPlace:
		return "places", nil
	case KindHistoricalPeriod:
		return "historical_periods", nil
	case KindHistoricalFigure:
		return "historical_figures", nil
	case KindTopicTag:
		return "topic_tags", nil
	default:
		return "", fmt.Errorf("%w: unsupported kind %q", ErrInvalidCanonicalEntity, kind)
	}
}
