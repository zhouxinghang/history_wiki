package event

import (
	"fmt"
	"strings"
	"testing"
)

func TestNormalizeDraftAllowsIncompleteWorkButValidatesSubmittedValues(t *testing.T) {
	draft, err := NormalizeDraft(Draft{Title: "  ", DisplayOrder: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if draft.Title != "" || draft.Time != nil || draft.PrimaryCategory != nil || len(draft.RegionIDs) != 0 {
		t.Fatalf("incomplete draft = %#v", draft)
	}

	invalidProminence := 4
	if _, err := NormalizeDraft(Draft{Prominence: &invalidProminence}); err == nil {
		t.Fatal("invalid prominence accepted")
	}
	category := PrimaryCategory("宗教")
	if _, err := NormalizeDraft(Draft{PrimaryCategory: &category}); err == nil {
		t.Fatal("invalid primary category accepted")
	}
}

func TestNormalizeDraftRejectsDatabaseIncompatibleTextAndOversizedAssociations(t *testing.T) {
	if _, err := NormalizeDraft(Draft{Title: "invalid\x00title"}); err != ErrInvalidEvent {
		t.Fatalf("null character error = %v", err)
	}

	regionIDs := make([]string, MaxEventRegions+1)
	for index := range regionIDs {
		regionIDs[index] = fmt.Sprintf("00000000-0000-7000-8000-%012d", index+1)
	}
	if _, err := NormalizeDraft(Draft{RegionIDs: regionIDs}); err != ErrInvalidEvent {
		t.Fatalf("association limit error = %v", err)
	}
}

func TestNormalizeSlugCreatesStableReadableAliases(t *testing.T) {
	if got, err := NormalizeSlug(" Qin-Unification "); err != nil || got != "qin-unification" {
		t.Fatalf("NormalizeSlug() = %q, %v", got, err)
	}
	for _, value := range []string{"", "秦统一", "no spaces", "two--dashes"} {
		if _, err := NormalizeSlug(value); err == nil {
			t.Fatalf("invalid slug %q accepted", value)
		}
	}
}

func TestPublicationRequiresCompleteContentAndAtLeastOneRegion(t *testing.T) {
	category := CategoryPolitics
	prominence := 1
	complete := Draft{
		Title: "秦统一六国", Summary: "秦结束战国割据局面。", Narrative: "完整叙述。",
		Time:            &TimeExpression{Kind: TimeYear, Year: &HistoricalYear{Era: EraBCE, Year: 221}},
		PrimaryCategory: &category, Prominence: &prominence, DisplayOrder: 1000,
		RegionIDs: []string{"00000000-0000-7000-8000-000000000001"},
	}
	if err := ValidateForPublication(complete); err != nil {
		t.Fatalf("complete draft rejected: %v", err)
	}
	complete.RegionIDs = nil
	if err := ValidateForPublication(complete); err != ErrIncompleteDraft {
		t.Fatalf("regionless publication error = %v", err)
	}
}

func TestPublicationAcceptsDraftFieldBoundaries(t *testing.T) {
	category := CategoryCulture
	prominence := 3
	draft := Draft{
		Title: strings.Repeat("题", 200), Summary: strings.Repeat("摘", 500), Narrative: strings.Repeat("文", 20000),
		Time:            &TimeExpression{Kind: TimeYear, Year: &HistoricalYear{Era: EraCE, Year: 2026}},
		PrimaryCategory: &category, Prominence: &prominence, DisplayOrder: 0,
		RegionIDs: []string{"00000000-0000-7000-8000-000000000001"},
	}
	if err := ValidateForPublication(draft); err != nil {
		t.Fatalf("draft boundary rejected: %v", err)
	}
	draft.DisplayOrder = 1_000_000
	if err := ValidateForPublication(draft); err != nil {
		t.Fatalf("positive display order boundary rejected: %v", err)
	}
	for name, mutate := range map[string]func(*Draft){
		"summary":       func(value *Draft) { value.Summary = strings.Repeat("摘", 501) },
		"narrative":     func(value *Draft) { value.Narrative = strings.Repeat("文", 20001) },
		"display order": func(value *Draft) { value.DisplayOrder = -1 },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := draft
			mutate(&invalid)
			if err := ValidateForPublication(invalid); err != ErrIncompleteDraft {
				t.Fatalf("publication boundary error = %v", err)
			}
		})
	}
}

func TestPublicationRequiresSlugWithAtLeastThreeCharacters(t *testing.T) {
	if err := ValidateSlugForPublication("qin"); err != nil {
		t.Fatalf("valid publication slug rejected: %v", err)
	}
	for _, slug := range []string{"a", "ab"} {
		if err := ValidateSlugForPublication(slug); err != ErrIncompleteDraft {
			t.Fatalf("short slug %q error = %v", slug, err)
		}
	}
}
