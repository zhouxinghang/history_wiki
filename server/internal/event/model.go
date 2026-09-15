package event

import (
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/zhouxinghang/history_wiki/server/internal/identifier"
)

var (
	ErrInvalidEvent         = errors.New("invalid event")
	ErrEventNotFound        = errors.New("event not found")
	ErrSlugConflict         = errors.New("event slug conflict")
	ErrEventVersionConflict = errors.New("event version conflict")
	ErrDraftVersionConflict = errors.New("draft version conflict")
	ErrActiveDraftExists    = errors.New("active draft already exists")
	ErrRevisionNotFound     = errors.New("event revision not found")
	ErrNoPublishedRevision  = errors.New("event has no published revision")
	ErrInvalidAssociation   = errors.New("invalid event association")
	ErrIdempotencyConflict  = errors.New("event idempotency conflict")
	ErrIncompleteDraft      = errors.New("event draft is incomplete for publication")
	ErrResultSetTooLarge    = errors.New("published event result set is too large")
	ErrEventNotPublished    = errors.New("event is not published")
)

type PublicationStatus string

const (
	StatusUnpublished PublicationStatus = "unpublished"
	StatusPublished   PublicationStatus = "published"
	StatusArchived    PublicationStatus = "archived"
)

const (
	MaxEventRegions   = 20
	MaxEventPlaces    = 50
	MaxEventPeriods   = 20
	MaxEventFigures   = 100
	MaxEventTopicTags = 50
)

type PrimaryCategory string

const (
	CategoryPolitics PrimaryCategory = "政治"
	CategoryMilitary PrimaryCategory = "军事"
	CategoryCulture  PrimaryCategory = "文化"
	CategoryScience  PrimaryCategory = "科技"
	CategorySociety  PrimaryCategory = "社会"
	CategoryExchange PrimaryCategory = "交流"
)

func (category PrimaryCategory) Valid() bool {
	switch category {
	case CategoryPolitics, CategoryMilitary, CategoryCulture, CategoryScience, CategorySociety, CategoryExchange:
		return true
	default:
		return false
	}
}

type EntityReference struct {
	ID                  string
	Name                string
	DisambiguationLabel *string
}

type Event struct {
	ID                string
	Slug              string
	PublicationStatus PublicationStatus
	CurrentRevisionID *string
	LockVersion       int64
	Draft             *Draft
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type Revision struct {
	ID               string
	EventID          string
	RevisionNo       int
	Title            string
	Summary          string
	Narrative        string
	Time             TimeExpression
	PrimaryCategory  PrimaryCategory
	Prominence       int
	DisplayOrder     int
	Regions          []EntityReference
	Places           []EntityReference
	Periods          []EntityReference
	Figures          []EntityReference
	TopicTags        []EntityReference
	PublishedBy      string
	PublishedByEmail string
	PublishedAt      time.Time
	Current          bool
}

type PublishedEvent struct {
	ID       string
	Slug     string
	Revision Revision
}

type PublishCommand struct {
	RevisionID           string
	ExpectedDraftVersion int64
	PublishedBy          string
	PublishedAt          time.Time
}

type ArchiveCommand struct {
	ExpectedEventVersion int64
	ArchivedBy           string
	ArchivedAt           time.Time
}

type PublishedEventQuery struct {
	From              float64
	To                float64
	EventFrom         float64
	EventTo           float64
	SearchTerm        string
	PeriodIDs         []string
	RegionIDs         []string
	FigureIDs         []string
	PrimaryCategories []PrimaryCategory
	MaximumProminence int
}

type PublishedEventQueryResult struct {
	Events        []PublishedEvent
	SourceTotal   int
	TotalMatching int
}

type PublishedEventBounds struct {
	Start float64
	End   float64
}

type PeriodFilterGroup struct {
	Context EntityReference
	Periods []EntityReference
}

type PublishedEventMetadata struct {
	PeriodGroups      []PeriodFilterGroup
	Regions           []EntityReference
	Figures           []EntityReference
	PrimaryCategories []PrimaryCategory
}

type Draft struct {
	EventID           string
	Title             string
	Summary           string
	Narrative         string
	Time              *TimeExpression
	PrimaryCategory   *PrimaryCategory
	Prominence        *int
	DisplayOrder      int
	RegionIDs         []string
	PlaceIDs          []string
	PeriodIDs         []string
	FigureIDs         []string
	TopicTagIDs       []string
	Regions           []EntityReference
	Places            []EntityReference
	Periods           []EntityReference
	Figures           []EntityReference
	TopicTags         []EntityReference
	LockVersion       int64
	BasedOnRevisionNo *int
	CreatedBy         string
	UpdatedBy         string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type DraftFromRevisionCommand struct {
	CreatedBy string
	CreatedAt time.Time
}

type DraftUpdate struct {
	Title           string
	Summary         string
	Narrative       string
	Time            *TimeExpression
	PrimaryCategory *PrimaryCategory
	Prominence      *int
	DisplayOrder    int
	RegionIDs       []string
	PlaceIDs        []string
	PeriodIDs       []string
	FigureIDs       []string
	TopicTagIDs     []string
	ExpectedVersion int64
	UpdatedBy       string
	UpdatedAt       time.Time
}

type SlugUpdate struct {
	Slug            string
	ExpectedVersion int64
	UpdatedBy       string
	UpdatedAt       time.Time
}

var slugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

func NormalizeSlug(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) == 0 || len(value) > 120 || !slugPattern.MatchString(value) {
		return "", ErrInvalidEvent
	}
	return value, nil
}

func NormalizeDraft(draft Draft) (Draft, error) {
	draft.Title = strings.TrimSpace(draft.Title)
	draft.Summary = strings.TrimSpace(draft.Summary)
	draft.Narrative = strings.TrimSpace(draft.Narrative)
	if strings.ContainsRune(draft.Title, '\x00') || strings.ContainsRune(draft.Summary, '\x00') || strings.ContainsRune(draft.Narrative, '\x00') {
		return Draft{}, ErrInvalidEvent
	}
	if len([]rune(draft.Title)) > 200 || len([]rune(draft.Summary)) > 1000 || len([]rune(draft.Narrative)) > 100000 {
		return Draft{}, ErrInvalidEvent
	}
	if draft.PrimaryCategory != nil && !draft.PrimaryCategory.Valid() {
		return Draft{}, ErrInvalidEvent
	}
	if draft.Prominence != nil && (*draft.Prominence < 1 || *draft.Prominence > 3) {
		return Draft{}, ErrInvalidEvent
	}
	if draft.DisplayOrder < -1_000_000 || draft.DisplayOrder > 1_000_000 {
		return Draft{}, ErrInvalidEvent
	}
	if draft.Time != nil {
		normalized, err := NormalizeTimeExpression(*draft.Time)
		if err != nil {
			return Draft{}, err
		}
		draft.Time = &normalized
	}

	associationLists := []*[]string{
		&draft.RegionIDs, &draft.PlaceIDs, &draft.PeriodIDs, &draft.FigureIDs, &draft.TopicTagIDs,
	}
	for _, values := range associationLists {
		normalized, err := NormalizeIDs(*values)
		if err != nil {
			return Draft{}, err
		}
		*values = normalized
	}
	if len(draft.RegionIDs) > MaxEventRegions || len(draft.PlaceIDs) > MaxEventPlaces ||
		len(draft.PeriodIDs) > MaxEventPeriods || len(draft.FigureIDs) > MaxEventFigures ||
		len(draft.TopicTagIDs) > MaxEventTopicTags {
		return Draft{}, ErrInvalidEvent
	}
	return draft, nil
}

// ValidateForPublication applies the stricter boundary used when an active
// draft is promoted to an immutable event revision. Draft persistence itself
// intentionally permits these fields to be absent.
func ValidateForPublication(draft Draft) error {
	normalized, err := NormalizeDraft(draft)
	if err != nil {
		return err
	}
	if normalized.Title == "" || normalized.Summary == "" || normalized.Narrative == "" ||
		normalized.Time == nil || normalized.PrimaryCategory == nil || normalized.Prominence == nil ||
		len(normalized.RegionIDs) == 0 {
		return ErrIncompleteDraft
	}
	if len([]rune(normalized.Summary)) > 500 || len([]rune(normalized.Narrative)) > 20_000 ||
		normalized.DisplayOrder < 0 || normalized.DisplayOrder > 1_000_000 {
		return ErrIncompleteDraft
	}
	if len(normalized.RegionIDs) > MaxEventRegions || len(normalized.PlaceIDs) > MaxEventPlaces ||
		len(normalized.PeriodIDs) > MaxEventPeriods || len(normalized.FigureIDs) > MaxEventFigures ||
		len(normalized.TopicTagIDs) > MaxEventTopicTags {
		return ErrIncompleteDraft
	}
	return nil
}

func ValidateSlugForPublication(slug string) error {
	normalized, err := NormalizeSlug(slug)
	if err != nil || len(normalized) < 3 {
		return ErrIncompleteDraft
	}
	return nil
}

func NormalizeIDs(values []string) ([]string, error) {
	unique := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if !identifier.Valid(value) {
			return nil, ErrInvalidAssociation
		}
		unique[value] = struct{}{}
	}
	normalized := make([]string, 0, len(unique))
	for value := range unique {
		normalized = append(normalized, value)
	}
	sort.Strings(normalized)
	return normalized, nil
}
