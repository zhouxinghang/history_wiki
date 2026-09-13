package httpapi

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/zhouxinghang/history_wiki/server/internal/audit"
	"github.com/zhouxinghang/history_wiki/server/internal/catalog"
	historyevent "github.com/zhouxinghang/history_wiki/server/internal/event"
	"github.com/zhouxinghang/history_wiki/server/internal/identifier"
)

type eventImportDocument struct {
	Events []eventImportRecordRequest `json:"events"`
}

type eventImportRecordRequest struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	eventDraftRequest
}

type eventImportValidationResponse struct {
	Valid  bool                       `json:"valid"`
	Total  int                        `json:"total"`
	Errors []historyevent.ImportIssue `json:"errors"`
}

type eventImportBatchResponse struct {
	BatchID       string   `json:"batchId"`
	ImportedCount int      `json:"importedCount"`
	EventIDs      []string `json:"eventIds"`
	CreatedAt     string   `json:"createdAt"`
}

func (server *Server) preflightEventImport(writer http.ResponseWriter, request *http.Request) {
	if !server.requireEventStore(writer, request) {
		return
	}
	document, err := decodeEventImportDocument(writer, request)
	if err != nil {
		writeEventImportDecodeError(writer, request, err)
		return
	}
	records, issues := normalizeEventImport(document)
	databaseIssues, err := server.eventStore.ValidateEventImport(request.Context(), records)
	if err != nil {
		server.logger.Error("preflight event import", "error", err, "request_id", requestID(request))
		writeProblem(writer, request, http.StatusServiceUnavailable, "event_import_store_unavailable", "暂时无法预检查导入", "数据库查询失败，请稍后重试。")
		return
	}
	issues = append(issues, databaseIssues...)
	sortImportIssues(issues)
	writeJSON(writer, http.StatusOK, eventImportValidationResponse{
		Valid: len(issues) == 0, Total: len(document.Events), Errors: issues,
	})
}

func (server *Server) importEvents(writer http.ResponseWriter, request *http.Request) {
	if !server.requireEventStore(writer, request) {
		return
	}
	document, err := decodeEventImportDocument(writer, request)
	if err != nil {
		server.recordEventImportFailure(request, "invalid_request", 0)
		writeEventImportDecodeError(writer, request, err)
		return
	}
	records, issues := normalizeEventImport(document)
	if len(issues) > 0 {
		server.recordEventImportFailure(request, "validation_failed", len(document.Events))
		writeJSON(writer, http.StatusUnprocessableEntity, eventImportValidationResponse{Valid: false, Total: len(document.Events), Errors: issues})
		return
	}
	idempotencyKey, err := catalog.NormalizeIdempotencyKey(request.Header.Get("Idempotency-Key"))
	if errors.Is(err, catalog.ErrIdempotencyKeyMissing) {
		server.recordEventImportFailure(request, "idempotency_key_required", len(records))
		writeProblem(writer, request, http.StatusBadRequest, "idempotency_key_required", "缺少幂等键", "正式导入必须提供 Idempotency-Key 请求头。")
		return
	}
	if err != nil {
		server.recordEventImportFailure(request, "invalid_idempotency_key", len(records))
		writeProblem(writer, request, http.StatusBadRequest, "invalid_idempotency_key", "幂等键无效", "Idempotency-Key 最长为 200 个字符。")
		return
	}
	batchID, err := identifier.New()
	if err != nil {
		server.logger.Error("generate event import batch id", "error", err, "request_id", requestID(request))
		writeProblem(writer, request, http.StatusServiceUnavailable, "event_import_store_unavailable", "暂时无法导入", "无法生成安全标识，请稍后重试。")
		return
	}
	current := principalFromContext(request.Context())
	now := server.config.Now().UTC()
	eventIDs := make([]string, 0, len(records))
	for index := range records {
		eventIDs = append(eventIDs, records[index].Event.ID)
		draft := records[index].Event.Draft
		draft.CreatedBy, draft.UpdatedBy = current.Session.User.ID, current.Session.User.ID
		draft.CreatedAt, draft.UpdatedAt = now, now
		records[index].Event.CreatedAt, records[index].Event.UpdatedAt = now, now
	}
	requestHash := eventImportRequestHash(records)
	batch, replayed, err := server.eventStore.ImportEvents(request.Context(), historyevent.ImportBatch{
		ID: batchID, EventIDs: eventIDs, CreatedAt: now,
	}, records, current.Session.User.ID, idempotencyKey, requestHash, server.auditEntry(request, audit.Entry{
		RequestID: requestID(request), Action: "event.import", TargetType: "event_import_batch", Outcome: audit.OutcomeSuccess,
	}))
	if err != nil {
		var validationError *historyevent.ImportValidationError
		switch {
		case errors.As(err, &validationError):
			server.recordEventImportFailure(request, "validation_failed", len(records))
			writeJSON(writer, http.StatusUnprocessableEntity, eventImportValidationResponse{Valid: false, Total: len(document.Events), Errors: validationError.Issues})
		case errors.Is(err, historyevent.ErrIdempotencyConflict):
			server.recordEventImportFailure(request, "idempotency_conflict", len(records))
			writeProblem(writer, request, http.StatusConflict, "idempotency_conflict", "重复提交内容不一致", "该 Idempotency-Key 已用于另一份批量导入请求。")
		default:
			server.recordEventImportFailure(request, "store_unavailable", len(records))
			server.logger.Error("import events", "error", err, "request_id", requestID(request))
			writeProblem(writer, request, http.StatusServiceUnavailable, "event_import_store_unavailable", "暂时无法导入", "整批写入失败且已回滚，请稍后重试。")
		}
		return
	}
	if replayed {
		writer.Header().Set("Idempotency-Replayed", "true")
	}
	writeJSON(writer, http.StatusCreated, responseEventImportBatch(batch))
}

func decodeEventImportDocument(writer http.ResponseWriter, request *http.Request) (eventImportDocument, error) {
	if request.ContentLength > historyevent.MaxImportBytes {
		return eventImportDocument{}, &http.MaxBytesError{Limit: historyevent.MaxImportBytes}
	}
	request.Body = http.MaxBytesReader(writer, request.Body, historyevent.MaxImportBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var document eventImportDocument
	if err := decoder.Decode(&document); err != nil {
		return eventImportDocument{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return eventImportDocument{}, errors.New("JSON 请求体只能包含一个对象")
	}
	if len(document.Events) == 0 {
		return eventImportDocument{}, errors.New("events 必须至少包含一条历史事件草稿")
	}
	if len(document.Events) > historyevent.MaxImportRecords {
		return eventImportDocument{}, errEventImportRecordLimit
	}
	return document, nil
}

var errEventImportRecordLimit = errors.New("event import record limit exceeded")

func writeEventImportDecodeError(writer http.ResponseWriter, request *http.Request, err error) {
	var sizeError *http.MaxBytesError
	if errors.As(err, &sizeError) {
		writeProblem(writer, request, http.StatusRequestEntityTooLarge, "event_import_too_large", "导入文件过大", "批量导入请求不得超过 20 MiB。")
		return
	}
	if errors.Is(err, errEventImportRecordLimit) {
		writeProblem(writer, request, http.StatusRequestEntityTooLarge, "event_import_too_many_records", "导入记录过多", "每批最多导入 10,000 条历史事件草稿。")
		return
	}
	writeProblem(writer, request, http.StatusBadRequest, "invalid_event_import", "导入文件无效", fmt.Sprintf("JSON 请求体无效: %v", err))
}

func normalizeEventImport(document eventImportDocument) ([]historyevent.ImportRecord, []historyevent.ImportIssue) {
	records := make([]historyevent.ImportRecord, 0, len(document.Events))
	issues := make([]historyevent.ImportIssue, 0)
	seenIDs := make(map[string]int)
	seenSlugs := make(map[string]int)
	for index, input := range document.Events {
		recordIssues := make([]historyevent.ImportIssue, 0)
		addIssue := func(field, code, detail string) {
			recordIssues = append(recordIssues, historyevent.ImportIssue{Index: index, Field: field, Code: code, Detail: detail})
		}
		id := strings.ToLower(strings.TrimSpace(input.ID))
		if !identifier.ValidV7(id) {
			addIssue("id", "invalid_uuid", "id 必须是符合 RFC 9562 的 UUIDv7。")
			id = ""
		} else if first, duplicate := seenIDs[id]; duplicate {
			addIssue("id", "duplicate_uuid", fmt.Sprintf("UUID 与批次第 %d 条记录重复。", first+1))
		} else {
			seenIDs[id] = index
		}
		slug, err := historyevent.NormalizeSlug(input.Slug)
		if err != nil {
			addIssue("slug", "invalid_slug", "slug 只能包含小写字母、数字和单个连字符，且最长为 120 个字符。")
			slug = ""
		} else if first, duplicate := seenSlugs[slug]; duplicate {
			addIssue("slug", "duplicate_slug", fmt.Sprintf("slug 与批次第 %d 条记录重复。", first+1))
		} else {
			seenSlugs[slug] = index
		}

		draft := historyevent.Draft{
			Title: strings.TrimSpace(input.Title), Summary: strings.TrimSpace(input.Summary), Narrative: strings.TrimSpace(input.Narrative),
			Time: input.Time, PrimaryCategory: input.PrimaryCategory, Prominence: input.Prominence,
			DisplayOrder: 1000,
		}
		if input.DisplayOrder != nil {
			draft.DisplayOrder = *input.DisplayOrder
		}
		if utf8.RuneCountInString(draft.Title) > 200 {
			addIssue("title", "too_long", "标题最长为 200 个字符。")
		}
		if strings.ContainsRune(draft.Title, '\x00') {
			addIssue("title", "invalid_text", "标题不能包含空字符。")
		}
		if utf8.RuneCountInString(draft.Summary) > 1000 {
			addIssue("summary", "too_long", "摘要最长为 1,000 个字符。")
		}
		if strings.ContainsRune(draft.Summary, '\x00') {
			addIssue("summary", "invalid_text", "摘要不能包含空字符。")
		}
		if utf8.RuneCountInString(draft.Narrative) > 100000 {
			addIssue("narrative", "too_long", "正文最长为 100,000 个字符。")
		}
		if strings.ContainsRune(draft.Narrative, '\x00') {
			addIssue("narrative", "invalid_text", "正文不能包含空字符。")
		}
		if draft.Time != nil {
			normalized, err := historyevent.NormalizeTimeExpression(*draft.Time)
			if err != nil {
				addIssue("time", "invalid_time_expression", "时间表述必须是合法的精确日期、年份、约数年份或闭区间，且不存在公元 0 年。")
			} else {
				draft.Time = &normalized
			}
		}
		if draft.PrimaryCategory != nil && !draft.PrimaryCategory.Valid() {
			addIssue("primaryCategory", "invalid_primary_category", "主分类必须是政治、军事、文化、科技、社会或交流。")
		}
		if draft.Prominence != nil && (*draft.Prominence < 1 || *draft.Prominence > 3) {
			addIssue("prominence", "invalid_prominence", "事件显著度必须是 1、2 或 3。")
		}
		if draft.DisplayOrder < -1_000_000 || draft.DisplayOrder > 1_000_000 {
			addIssue("displayOrder", "invalid_display_order", "展示顺序必须介于 -1,000,000 和 1,000,000。")
		}
		associationInputs := []struct {
			field string
			value []string
			dest  *[]string
			max   int
		}{
			{"regionIds", input.RegionIDs, &draft.RegionIDs, historyevent.MaxEventRegions},
			{"placeIds", input.PlaceIDs, &draft.PlaceIDs, historyevent.MaxEventPlaces},
			{"periodIds", input.PeriodIDs, &draft.PeriodIDs, historyevent.MaxEventPeriods},
			{"figureIds", input.FigureIDs, &draft.FigureIDs, historyevent.MaxEventFigures},
			{"topicTagIds", input.TopicTagIDs, &draft.TopicTagIDs, historyevent.MaxEventTopicTags},
		}
		for _, association := range associationInputs {
			normalized, err := historyevent.NormalizeIDs(association.value)
			if err != nil {
				addIssue(association.field, "invalid_uuid", "规范实体标识必须是 UUID。")
			} else if len(normalized) > association.max {
				addIssue(association.field, "too_many_items", fmt.Sprintf("最多允许 %d 个规范实体关联。", association.max))
			} else {
				*association.dest = normalized
			}
		}
		issues = append(issues, recordIssues...)
		draft.EventID = id
		records = append(records, historyevent.ImportRecord{Index: index, Event: historyevent.Event{
			ID: id, Slug: slug, PublicationStatus: historyevent.StatusUnpublished, LockVersion: 1, Draft: &draft,
		}})
	}
	sortImportIssues(issues)
	return records, issues
}

func eventImportRequestHash(records []historyevent.ImportRecord) []byte {
	type hashRecord struct {
		ID              string                        `json:"id"`
		Slug            string                        `json:"slug"`
		Title           string                        `json:"title"`
		Summary         string                        `json:"summary"`
		Narrative       string                        `json:"narrative"`
		Time            *historyevent.TimeExpression  `json:"time"`
		PrimaryCategory *historyevent.PrimaryCategory `json:"primaryCategory"`
		Prominence      *int                          `json:"prominence"`
		DisplayOrder    int                           `json:"displayOrder"`
		RegionIDs       []string                      `json:"regionIds"`
		PlaceIDs        []string                      `json:"placeIds"`
		PeriodIDs       []string                      `json:"periodIds"`
		FigureIDs       []string                      `json:"figureIds"`
		TopicTagIDs     []string                      `json:"topicTagIds"`
	}
	payload := make([]hashRecord, 0, len(records))
	for _, record := range records {
		draft := record.Event.Draft
		payload = append(payload, hashRecord{
			ID: record.Event.ID, Slug: record.Event.Slug, Title: draft.Title, Summary: draft.Summary, Narrative: draft.Narrative,
			Time: draft.Time, PrimaryCategory: draft.PrimaryCategory, Prominence: draft.Prominence, DisplayOrder: draft.DisplayOrder,
			RegionIDs: draft.RegionIDs, PlaceIDs: draft.PlaceIDs, PeriodIDs: draft.PeriodIDs, FigureIDs: draft.FigureIDs, TopicTagIDs: draft.TopicTagIDs,
		})
	}
	encoded, _ := json.Marshal(payload)
	hash := sha256.Sum256(encoded)
	return hash[:]
}

func sortImportIssues(issues []historyevent.ImportIssue) {
	sort.SliceStable(issues, func(i, j int) bool {
		if issues[i].Index != issues[j].Index {
			return issues[i].Index < issues[j].Index
		}
		if issues[i].Field != issues[j].Field {
			return issues[i].Field < issues[j].Field
		}
		return issues[i].Code < issues[j].Code
	})
}

func responseEventImportBatch(batch historyevent.ImportBatch) eventImportBatchResponse {
	return eventImportBatchResponse{
		BatchID: batch.ID, ImportedCount: len(batch.EventIDs), EventIDs: batch.EventIDs,
		CreatedAt: batch.CreatedAt.UTC().Format(timeFormat),
	}
}

func (server *Server) recordEventImportFailure(request *http.Request, reason string, count int) {
	current := principalFromContext(request.Context())
	actorID := current.Session.User.ID
	server.recordAudit(request, audit.Entry{
		RequestID: requestID(request), ActorUserID: &actorID, Action: "event.import", TargetType: "event_import_batch",
		Outcome: audit.OutcomeFailure, Details: map[string]any{"reason": reason, "eventCount": count},
	})
}
