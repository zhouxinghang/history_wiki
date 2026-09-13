package httpapi

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/zhouxinghang/history_wiki/server/internal/audit"
	"github.com/zhouxinghang/history_wiki/server/internal/catalog"
	historyevent "github.com/zhouxinghang/history_wiki/server/internal/event"
	"github.com/zhouxinghang/history_wiki/server/internal/identifier"
)

type eventReferenceResponse struct {
	ID                  string  `json:"id"`
	Name                string  `json:"name"`
	DisambiguationLabel *string `json:"disambiguationLabel"`
}

type managedEventDraftResponse struct {
	Title             string                        `json:"title"`
	Summary           string                        `json:"summary"`
	Narrative         string                        `json:"narrative"`
	Time              *historyevent.TimeExpression  `json:"time"`
	PrimaryCategory   *historyevent.PrimaryCategory `json:"primaryCategory"`
	Prominence        *int                          `json:"prominence"`
	DisplayOrder      int                           `json:"displayOrder"`
	Regions           []eventReferenceResponse      `json:"regions"`
	Places            []eventReferenceResponse      `json:"places"`
	Periods           []eventReferenceResponse      `json:"periods"`
	Figures           []eventReferenceResponse      `json:"figures"`
	TopicTags         []eventReferenceResponse      `json:"topicTags"`
	LockVersion       int64                         `json:"lockVersion"`
	BasedOnRevisionNo *int                          `json:"basedOnRevisionNo"`
	CreatedAt         string                        `json:"createdAt"`
	UpdatedAt         string                        `json:"updatedAt"`
}

type managedEventResponse struct {
	ID                string                         `json:"id"`
	Slug              string                         `json:"slug"`
	PublicationStatus historyevent.PublicationStatus `json:"publicationStatus"`
	LockVersion       int64                          `json:"lockVersion"`
	Draft             *managedEventDraftResponse     `json:"draft"`
	CreatedAt         string                         `json:"createdAt"`
	UpdatedAt         string                         `json:"updatedAt"`
}

type managedEventListResponse struct {
	Events []managedEventResponse `json:"events"`
}

type eventRevisionPublisherResponse struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

type managedEventRevisionSummaryResponse struct {
	ID          string                         `json:"id"`
	RevisionNo  int                            `json:"revisionNo"`
	Title       string                         `json:"title"`
	PublishedBy eventRevisionPublisherResponse `json:"publishedBy"`
	PublishedAt string                         `json:"publishedAt"`
	Current     bool                           `json:"current"`
}

type managedEventRevisionListResponse struct {
	Revisions []managedEventRevisionSummaryResponse `json:"revisions"`
}

type managedEventRevisionResponse struct {
	ID              string                         `json:"id"`
	EventID         string                         `json:"eventId"`
	RevisionNo      int                            `json:"revisionNo"`
	Title           string                         `json:"title"`
	Summary         string                         `json:"summary"`
	Narrative       string                         `json:"narrative"`
	Time            historyevent.TimeExpression    `json:"time"`
	PrimaryCategory historyevent.PrimaryCategory   `json:"primaryCategory"`
	Prominence      int                            `json:"prominence"`
	DisplayOrder    int                            `json:"displayOrder"`
	Regions         []eventReferenceResponse       `json:"regions"`
	Places          []eventReferenceResponse       `json:"places"`
	Periods         []eventReferenceResponse       `json:"periods"`
	Figures         []eventReferenceResponse       `json:"figures"`
	TopicTags       []eventReferenceResponse       `json:"topicTags"`
	PublishedBy     eventRevisionPublisherResponse `json:"publishedBy"`
	PublishedAt     string                         `json:"publishedAt"`
	Current         bool                           `json:"current"`
}

type eventDraftRequest struct {
	Title           string                        `json:"title"`
	Summary         string                        `json:"summary"`
	Narrative       string                        `json:"narrative"`
	Time            *historyevent.TimeExpression  `json:"time"`
	PrimaryCategory *historyevent.PrimaryCategory `json:"primaryCategory"`
	Prominence      *int                          `json:"prominence"`
	DisplayOrder    *int                          `json:"displayOrder"`
	RegionIDs       []string                      `json:"regionIds"`
	PlaceIDs        []string                      `json:"placeIds"`
	PeriodIDs       []string                      `json:"periodIds"`
	FigureIDs       []string                      `json:"figureIds"`
	TopicTagIDs     []string                      `json:"topicTagIds"`
}

type createManagedEventRequest struct {
	Slug string `json:"slug"`
	eventDraftRequest
}

type updateManagedEventSlugRequest struct {
	Slug string `json:"slug"`
}

func (server *Server) listManagedEvents(writer http.ResponseWriter, request *http.Request) {
	if !server.requireEventStore(writer, request) {
		return
	}
	events, err := server.eventStore.ListManagedEvents(request.Context())
	if err != nil {
		server.logger.Error("list managed events", "error", err, "request_id", requestID(request))
		writeProblem(writer, request, http.StatusServiceUnavailable, "event_store_unavailable", "暂时无法读取历史事件", "数据库查询失败，请稍后重试。")
		return
	}
	response := make([]managedEventResponse, 0, len(events))
	for _, managedEvent := range events {
		response = append(response, responseManagedEvent(managedEvent))
	}
	writeJSON(writer, http.StatusOK, managedEventListResponse{Events: response})
}

func (server *Server) getManagedEvent(writer http.ResponseWriter, request *http.Request) {
	if !server.requireEventStore(writer, request) {
		return
	}
	eventID := chi.URLParam(request, "eventID")
	if !identifier.Valid(eventID) {
		writeProblem(writer, request, http.StatusBadRequest, "invalid_event_id", "历史事件标识无效", "历史事件标识必须是 UUID。")
		return
	}
	managedEvent, err := server.eventStore.ManagedEventByID(request.Context(), eventID)
	if errors.Is(err, historyevent.ErrEventNotFound) {
		writeProblem(writer, request, http.StatusNotFound, "event_not_found", "历史事件不存在", "未找到指定历史事件。")
		return
	}
	if err != nil {
		server.logger.Error("get managed event", "error", err, "request_id", requestID(request), "event_id", eventID)
		writeProblem(writer, request, http.StatusServiceUnavailable, "event_store_unavailable", "暂时无法读取历史事件", "数据库查询失败，请稍后重试。")
		return
	}
	if managedEvent.Draft != nil {
		writer.Header().Set("ETag", draftETag(managedEvent.Draft.LockVersion))
	}
	writeJSON(writer, http.StatusOK, responseManagedEvent(managedEvent))
}

func (server *Server) createManagedEvent(writer http.ResponseWriter, request *http.Request) {
	if !server.requireEventStore(writer, request) {
		return
	}
	var input createManagedEventRequest
	if err := decodeJSONBody(writer, request, &input); err != nil {
		server.recordEventWriteFailure(request, "event.create", "invalid_request", nil)
		writeJSONBodyProblem(writer, request, "历史事件创建请求无效", err)
		return
	}
	slug, err := historyevent.NormalizeSlug(input.Slug)
	if err != nil {
		server.recordEventWriteFailure(request, "event.create", "invalid_event", nil)
		writeProblem(writer, request, http.StatusUnprocessableEntity, "invalid_event", "历史事件信息无效", "slug 只能包含小写字母、数字和单个连字符，且最长为 120 个字符。")
		return
	}
	draft, err := normalizeDraftRequest(input.eventDraftRequest)
	if err != nil {
		server.recordEventWriteFailure(request, "event.create", "invalid_draft", nil)
		writeProblem(writer, request, http.StatusUnprocessableEntity, "invalid_event_draft", "活动草稿信息无效", draftValidationDetail(err))
		return
	}
	idempotencyKey, err := catalog.NormalizeIdempotencyKey(request.Header.Get("Idempotency-Key"))
	if errors.Is(err, catalog.ErrIdempotencyKeyMissing) {
		server.recordEventWriteFailure(request, "event.create", "idempotency_key_required", nil)
		writeProblem(writer, request, http.StatusBadRequest, "idempotency_key_required", "缺少幂等键", "创建历史事件必须提供 Idempotency-Key 请求头。")
		return
	}
	if err != nil {
		server.recordEventWriteFailure(request, "event.create", "invalid_idempotency_key", nil)
		writeProblem(writer, request, http.StatusBadRequest, "invalid_idempotency_key", "幂等键无效", "Idempotency-Key 最长为 200 个字符。")
		return
	}
	eventID, err := identifier.New()
	if err != nil {
		server.logger.Error("generate event id", "error", err, "request_id", requestID(request))
		writeProblem(writer, request, http.StatusServiceUnavailable, "event_store_unavailable", "暂时无法创建历史事件", "无法生成安全标识，请稍后重试。")
		return
	}
	current := principalFromContext(request.Context())
	now := server.config.Now().UTC()
	draft.EventID = eventID
	draft.CreatedBy = current.Session.User.ID
	draft.UpdatedBy = current.Session.User.ID
	draft.CreatedAt = now
	draft.UpdatedAt = now
	managedEvent := historyevent.Event{
		ID: eventID, Slug: slug, PublicationStatus: historyevent.StatusUnpublished,
		LockVersion: 1, Draft: &draft, CreatedAt: now, UpdatedAt: now,
	}
	requestHash := managedEventRequestHash(slug, draft)
	created, replayed, err := server.eventStore.CreateManagedEvent(request.Context(), managedEvent, idempotencyKey, requestHash, server.auditEntry(request, audit.Entry{
		RequestID: requestID(request), Action: "event.create", TargetType: "event", Outcome: audit.OutcomeSuccess,
		Details: eventAuditDetails(slug, draft),
	}))
	if server.handleEventWriteError(writer, request, err, "event.create", nil) {
		return
	}
	if replayed {
		writer.Header().Set("Idempotency-Replayed", "true")
	}
	writer.Header().Set("Location", "/api/v1/admin/events/"+created.ID)
	if created.Draft != nil {
		writer.Header().Set("ETag", draftETag(created.Draft.LockVersion))
	}
	writeJSON(writer, http.StatusCreated, responseManagedEvent(created))
}

func (server *Server) updateManagedEventSlug(writer http.ResponseWriter, request *http.Request) {
	if !server.requireEventStore(writer, request) {
		return
	}
	eventID := chi.URLParam(request, "eventID")
	if !identifier.Valid(eventID) {
		server.recordEventWriteFailure(request, "event.metadata_update", "invalid_event_id", nil)
		writeProblem(writer, request, http.StatusBadRequest, "invalid_event_id", "历史事件标识无效", "历史事件标识必须是 UUID。")
		return
	}
	expectedVersion, ok := requirePrefixedETag(writer, request, "event", "历史事件元数据")
	if !ok {
		return
	}
	var input updateManagedEventSlugRequest
	if err := decodeJSONBody(writer, request, &input); err != nil {
		server.recordEventWriteFailure(request, "event.metadata_update", "invalid_request", &eventID)
		writeJSONBodyProblem(writer, request, "历史事件修改请求无效", err)
		return
	}
	slug, err := historyevent.NormalizeSlug(input.Slug)
	if err != nil {
		server.recordEventWriteFailure(request, "event.metadata_update", "invalid_event", &eventID)
		writeProblem(writer, request, http.StatusUnprocessableEntity, "invalid_event", "历史事件信息无效", "slug 只能包含小写字母、数字和单个连字符，且最长为 120 个字符。")
		return
	}
	current := principalFromContext(request.Context())
	updated, err := server.eventStore.UpdateManagedEventSlug(request.Context(), eventID, historyevent.SlugUpdate{
		Slug: slug, ExpectedVersion: expectedVersion, UpdatedBy: current.Session.User.ID, UpdatedAt: server.config.Now().UTC(),
	}, server.auditEntry(request, audit.Entry{
		RequestID: requestID(request), Action: "event.metadata_update", TargetType: "event", Outcome: audit.OutcomeSuccess,
		Details: map[string]any{"slug": slug},
	}))
	if server.handleEventWriteError(writer, request, err, "event.metadata_update", &eventID) {
		return
	}
	writer.Header().Set("ETag", eventETag(updated.LockVersion))
	writeJSON(writer, http.StatusOK, responseManagedEvent(updated))
}

func (server *Server) updateManagedEventDraft(writer http.ResponseWriter, request *http.Request) {
	if !server.requireEventStore(writer, request) {
		return
	}
	eventID := chi.URLParam(request, "eventID")
	if !identifier.Valid(eventID) {
		server.recordEventWriteFailure(request, "event.draft_update", "invalid_event_id", nil)
		writeProblem(writer, request, http.StatusBadRequest, "invalid_event_id", "历史事件标识无效", "历史事件标识必须是 UUID。")
		return
	}
	expectedVersion, ok := requirePrefixedETag(writer, request, "draft", "活动草稿")
	if !ok {
		return
	}
	var input eventDraftRequest
	if err := decodeJSONBody(writer, request, &input); err != nil {
		server.recordEventWriteFailure(request, "event.draft_update", "invalid_request", &eventID)
		writeJSONBodyProblem(writer, request, "活动草稿修改请求无效", err)
		return
	}
	draft, err := normalizeDraftRequest(input)
	if err != nil {
		server.recordEventWriteFailure(request, "event.draft_update", "invalid_draft", &eventID)
		writeProblem(writer, request, http.StatusUnprocessableEntity, "invalid_event_draft", "活动草稿信息无效", draftValidationDetail(err))
		return
	}
	current := principalFromContext(request.Context())
	updated, err := server.eventStore.UpdateEventDraft(request.Context(), eventID, historyevent.DraftUpdate{
		Title: draft.Title, Summary: draft.Summary, Narrative: draft.Narrative, Time: draft.Time,
		PrimaryCategory: draft.PrimaryCategory, Prominence: draft.Prominence, DisplayOrder: draft.DisplayOrder,
		RegionIDs: draft.RegionIDs, PlaceIDs: draft.PlaceIDs, PeriodIDs: draft.PeriodIDs,
		FigureIDs: draft.FigureIDs, TopicTagIDs: draft.TopicTagIDs,
		ExpectedVersion: expectedVersion, UpdatedBy: current.Session.User.ID, UpdatedAt: server.config.Now().UTC(),
	}, server.auditEntry(request, audit.Entry{
		RequestID: requestID(request), Action: "event.draft_update", TargetType: "event", Outcome: audit.OutcomeSuccess,
		Details: eventAuditDetails("", draft),
	}))
	if server.handleEventWriteError(writer, request, err, "event.draft_update", &eventID) {
		return
	}
	if updated.Draft != nil {
		writer.Header().Set("ETag", draftETag(updated.Draft.LockVersion))
	}
	writeJSON(writer, http.StatusOK, responseManagedEvent(updated))
}

func (server *Server) createManagedEventDraft(writer http.ResponseWriter, request *http.Request) {
	if !server.requireEventStore(writer, request) {
		return
	}
	eventID := chi.URLParam(request, "eventID")
	if !identifier.Valid(eventID) {
		server.recordEventWriteFailure(request, "event.draft_create", "invalid_event_id", nil)
		writeProblem(writer, request, http.StatusBadRequest, "invalid_event_id", "历史事件标识无效", "历史事件标识必须是 UUID。")
		return
	}
	current := principalFromContext(request.Context())
	created, err := server.eventStore.CreateDraftFromCurrentRevision(request.Context(), eventID, historyevent.DraftFromRevisionCommand{
		CreatedBy: current.Session.User.ID,
		CreatedAt: server.config.Now().UTC(),
	}, server.auditEntry(request, audit.Entry{
		RequestID: requestID(request), Action: "event.draft_create", TargetType: "event", Outcome: audit.OutcomeSuccess,
	}))
	if server.handleEventWriteError(writer, request, err, "event.draft_create", &eventID) {
		return
	}
	writer.Header().Set("Location", "/api/v1/admin/events/"+eventID)
	writer.Header().Set("ETag", draftETag(created.Draft.LockVersion))
	writeJSON(writer, http.StatusCreated, responseManagedEvent(created))
}

func (server *Server) publishManagedEvent(writer http.ResponseWriter, request *http.Request) {
	if !server.requireEventStore(writer, request) {
		return
	}
	eventID := chi.URLParam(request, "eventID")
	if !identifier.Valid(eventID) {
		server.recordEventWriteFailure(request, "event.publish", "invalid_event_id", nil)
		writeProblem(writer, request, http.StatusBadRequest, "invalid_event_id", "历史事件标识无效", "历史事件标识必须是 UUID。")
		return
	}
	expectedVersion, ok := requirePrefixedETag(writer, request, "draft", "活动草稿")
	if !ok {
		return
	}
	revisionID, err := identifier.New()
	if err != nil {
		server.logger.Error("generate event revision id", "error", err, "request_id", requestID(request))
		writeProblem(writer, request, http.StatusServiceUnavailable, "event_store_unavailable", "暂时无法发布历史事件", "无法生成安全标识，请稍后重试。")
		return
	}
	current := principalFromContext(request.Context())
	published, err := server.eventStore.PublishEvent(request.Context(), eventID, historyevent.PublishCommand{
		RevisionID: revisionID, ExpectedDraftVersion: expectedVersion,
		PublishedBy: current.Session.User.ID, PublishedAt: server.config.Now().UTC(),
	}, server.auditEntry(request, audit.Entry{
		RequestID: requestID(request), Action: "event.publish", TargetType: "event", Outcome: audit.OutcomeSuccess,
	}))
	if server.handleEventWriteError(writer, request, err, "event.publish", &eventID) {
		return
	}
	writer.Header().Set("Location", "/api/v1/events/"+published.ID)
	writeJSON(writer, http.StatusOK, responsePublishedEvent(published))
}

func (server *Server) archiveManagedEvent(writer http.ResponseWriter, request *http.Request) {
	if !server.requireEventStore(writer, request) {
		return
	}
	eventID := chi.URLParam(request, "eventID")
	if !identifier.Valid(eventID) {
		server.recordEventWriteFailure(request, "event.archive", "invalid_event_id", nil)
		writeProblem(writer, request, http.StatusBadRequest, "invalid_event_id", "历史事件标识无效", "历史事件标识必须是 UUID。")
		return
	}
	expectedVersion, ok := requirePrefixedETag(writer, request, "event", "历史事件")
	if !ok {
		return
	}
	current := principalFromContext(request.Context())
	archived, err := server.eventStore.ArchiveEvent(request.Context(), eventID, historyevent.ArchiveCommand{
		ExpectedEventVersion: expectedVersion,
		ArchivedBy:           current.Session.User.ID,
		ArchivedAt:           server.config.Now().UTC(),
	}, server.auditEntry(request, audit.Entry{
		RequestID: requestID(request), Action: "event.archive", TargetType: "event", Outcome: audit.OutcomeSuccess,
	}))
	if server.handleEventWriteError(writer, request, err, "event.archive", &eventID) {
		return
	}
	writer.Header().Set("ETag", eventETag(archived.LockVersion))
	writeJSON(writer, http.StatusOK, responseManagedEvent(archived))
}

func (server *Server) listManagedEventRevisions(writer http.ResponseWriter, request *http.Request) {
	if !server.requireEventStore(writer, request) {
		return
	}
	eventID := chi.URLParam(request, "eventID")
	if !identifier.Valid(eventID) {
		writeProblem(writer, request, http.StatusBadRequest, "invalid_event_id", "历史事件标识无效", "历史事件标识必须是 UUID。")
		return
	}
	revisions, err := server.eventStore.ListEventRevisions(request.Context(), eventID)
	if server.handleEventRevisionReadError(writer, request, err, eventID) {
		return
	}
	response := make([]managedEventRevisionSummaryResponse, 0, len(revisions))
	for _, revision := range revisions {
		response = append(response, managedEventRevisionSummaryResponse{
			ID: revision.ID, RevisionNo: revision.RevisionNo, Title: revision.Title,
			PublishedBy: responseRevisionPublisher(revision), PublishedAt: revision.PublishedAt.UTC().Format(timeFormat),
			Current: revision.Current,
		})
	}
	writeJSON(writer, http.StatusOK, managedEventRevisionListResponse{Revisions: response})
}

func (server *Server) getManagedEventRevision(writer http.ResponseWriter, request *http.Request) {
	if !server.requireEventStore(writer, request) {
		return
	}
	eventID, revisionNo, ok := managedEventRevisionParameters(writer, request)
	if !ok {
		return
	}
	_, err := server.eventStore.ManagedEventByID(request.Context(), eventID)
	if server.handleEventRevisionReadError(writer, request, err, eventID) {
		return
	}
	revision, err := server.eventStore.EventRevisionByNumber(request.Context(), eventID, revisionNo)
	if server.handleEventRevisionReadError(writer, request, err, eventID) {
		return
	}
	writeJSON(writer, http.StatusOK, responseManagedEventRevision(revision, revision.Current))
}

func (server *Server) restoreManagedEventRevision(writer http.ResponseWriter, request *http.Request) {
	if !server.requireEventStore(writer, request) {
		return
	}
	eventID, revisionNo, ok := managedEventRevisionParameters(writer, request)
	if !ok {
		server.recordEventWriteFailure(request, "event.revision_restore", "invalid_revision", nil)
		return
	}
	current := principalFromContext(request.Context())
	restored, err := server.eventStore.RestoreEventRevision(request.Context(), eventID, revisionNo, historyevent.DraftFromRevisionCommand{
		CreatedBy: current.Session.User.ID,
		CreatedAt: server.config.Now().UTC(),
	}, server.auditEntry(request, audit.Entry{
		RequestID: requestID(request), Action: "event.revision_restore", TargetType: "event", Outcome: audit.OutcomeSuccess,
		Details: map[string]any{"revisionNo": revisionNo},
	}))
	if server.handleEventWriteError(writer, request, err, "event.revision_restore", &eventID) {
		return
	}
	writer.Header().Set("Location", "/api/v1/admin/events/"+eventID)
	writer.Header().Set("ETag", draftETag(restored.Draft.LockVersion))
	writeJSON(writer, http.StatusCreated, responseManagedEvent(restored))
}

func managedEventRevisionParameters(writer http.ResponseWriter, request *http.Request) (string, int, bool) {
	eventID := chi.URLParam(request, "eventID")
	if !identifier.Valid(eventID) {
		writeProblem(writer, request, http.StatusBadRequest, "invalid_event_id", "历史事件标识无效", "历史事件标识必须是 UUID。")
		return "", 0, false
	}
	revisionNo, err := strconv.Atoi(chi.URLParam(request, "revisionNo"))
	if err != nil || revisionNo < 1 {
		writeProblem(writer, request, http.StatusBadRequest, "invalid_revision_no", "事件版本号无效", "事件版本号必须是大于零的整数。")
		return "", 0, false
	}
	return eventID, revisionNo, true
}

func (server *Server) handleEventRevisionReadError(writer http.ResponseWriter, request *http.Request, err error, eventID string) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, historyevent.ErrEventNotFound) {
		writeProblem(writer, request, http.StatusNotFound, "event_not_found", "历史事件不存在", "未找到指定历史事件。")
		return true
	}
	if errors.Is(err, historyevent.ErrRevisionNotFound) {
		writeProblem(writer, request, http.StatusNotFound, "event_revision_not_found", "事件版本不存在", "未找到指定的历史事件版本。")
		return true
	}
	server.logger.Error("read managed event revision", "error", err, "request_id", requestID(request), "event_id", eventID)
	writeProblem(writer, request, http.StatusServiceUnavailable, "event_store_unavailable", "暂时无法读取事件版本", "数据库查询失败，请稍后重试。")
	return true
}

func normalizeDraftRequest(input eventDraftRequest) (historyevent.Draft, error) {
	displayOrder := 1000
	if input.DisplayOrder != nil {
		displayOrder = *input.DisplayOrder
	}
	return historyevent.NormalizeDraft(historyevent.Draft{
		Title: input.Title, Summary: input.Summary, Narrative: input.Narrative, Time: input.Time,
		PrimaryCategory: input.PrimaryCategory, Prominence: input.Prominence, DisplayOrder: displayOrder,
		RegionIDs: input.RegionIDs, PlaceIDs: input.PlaceIDs, PeriodIDs: input.PeriodIDs,
		FigureIDs: input.FigureIDs, TopicTagIDs: input.TopicTagIDs,
	})
}

func (server *Server) handleEventWriteError(writer http.ResponseWriter, request *http.Request, err error, action string, targetID *string) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, historyevent.ErrSlugConflict):
		server.recordEventWriteFailure(request, action, "slug_conflict", targetID)
		writeProblem(writer, request, http.StatusConflict, "event_slug_conflict", "slug 已被使用", "请为历史事件选择另一个唯一 slug。")
	case errors.Is(err, historyevent.ErrIdempotencyConflict):
		server.recordEventWriteFailure(request, action, "idempotency_conflict", targetID)
		writeProblem(writer, request, http.StatusConflict, "idempotency_conflict", "重复提交内容不一致", "该 Idempotency-Key 已用于另一份历史事件创建请求。")
	case errors.Is(err, historyevent.ErrInvalidAssociation):
		server.recordEventWriteFailure(request, action, "invalid_association", targetID)
		writeProblem(writer, request, http.StatusUnprocessableEntity, "invalid_event_association", "规范实体关联无效", "活动草稿只能关联当前有效且存在的规范实体。")
	case errors.Is(err, historyevent.ErrIncompleteDraft), errors.Is(err, historyevent.ErrInvalidEvent), errors.Is(err, historyevent.ErrInvalidTimeExpression):
		server.recordEventWriteFailure(request, action, "incomplete_draft", targetID)
		writeProblem(writer, request, http.StatusUnprocessableEntity, "incomplete_event_draft", "活动草稿尚不能发布", "请使用至少 3 个字符的 slug，完整填写标题、摘要、正文、时间表述、主分类、至少一个地区和事件显著度；摘要最多 500 字，正文最多 20,000 字，展示顺序须在 0 至 1,000,000 之间。")
	case errors.Is(err, historyevent.ErrEventNotFound):
		server.recordEventWriteFailure(request, action, "not_found", targetID)
		writeProblem(writer, request, http.StatusNotFound, "event_not_found", "历史事件不存在", "未找到指定历史事件或活动草稿。")
	case errors.Is(err, historyevent.ErrRevisionNotFound):
		server.recordEventWriteFailure(request, action, "revision_not_found", targetID)
		writeProblem(writer, request, http.StatusNotFound, "event_revision_not_found", "事件版本不存在", "未找到指定的历史事件版本。")
	case errors.Is(err, historyevent.ErrActiveDraftExists):
		server.setLatestEventETag(request, writer, targetID, true)
		server.recordEventWriteFailure(request, action, "active_draft_exists", targetID)
		writeProblem(writer, request, http.StatusConflict, "event_draft_already_exists", "历史事件已有活动草稿", "请继续编辑现有活动草稿；恢复版本不会覆盖已有草稿。")
	case errors.Is(err, historyevent.ErrNoPublishedRevision):
		server.recordEventWriteFailure(request, action, "no_published_revision", targetID)
		writeProblem(writer, request, http.StatusConflict, "event_has_no_published_revision", "历史事件尚无发布版本", "只有曾经发布的历史事件可以从当前版本建立草稿。")
	case errors.Is(err, historyevent.ErrEventNotPublished):
		server.recordEventWriteFailure(request, action, "not_published", targetID)
		writeProblem(writer, request, http.StatusConflict, "event_not_published", "历史事件当前未发布", "只有当前已发布的历史事件可以下线。")
	case errors.Is(err, historyevent.ErrEventVersionConflict):
		server.setLatestEventETag(request, writer, targetID, false)
		server.recordEventWriteFailure(request, action, "version_conflict", targetID)
		writeProblem(writer, request, http.StatusConflict, "event_version_conflict", "历史事件元数据已被修改", "请刷新历史事件并基于最新版本重试。")
	case errors.Is(err, historyevent.ErrDraftVersionConflict):
		server.setLatestEventETag(request, writer, targetID, true)
		server.recordEventWriteFailure(request, action, "version_conflict", targetID)
		writeProblem(writer, request, http.StatusConflict, "event_draft_version_conflict", "活动草稿已被修改", "其他编辑者已经保存了新版本，请刷新后重新编辑。")
	default:
		server.recordEventWriteFailure(request, action, "store_unavailable", targetID)
		server.logger.Error("write managed event", "error", err, "request_id", requestID(request), "action", action)
		writeProblem(writer, request, http.StatusServiceUnavailable, "event_store_unavailable", "暂时无法保存历史事件", "数据库写入失败，请稍后重试。")
	}
	return true
}

func (server *Server) setLatestEventETag(request *http.Request, writer http.ResponseWriter, targetID *string, draft bool) {
	if targetID == nil {
		return
	}
	current, err := server.eventStore.ManagedEventByID(request.Context(), *targetID)
	if err != nil {
		return
	}
	if draft && current.Draft != nil {
		writer.Header().Set("ETag", draftETag(current.Draft.LockVersion))
	} else if !draft {
		writer.Header().Set("ETag", eventETag(current.LockVersion))
	}
}

func (server *Server) recordEventWriteFailure(request *http.Request, action, reason string, targetID *string) {
	current := principalFromContext(request.Context())
	actorID := current.Session.User.ID
	server.recordAudit(request, audit.Entry{
		RequestID: requestID(request), ActorUserID: &actorID, Action: action, TargetType: "event",
		TargetID: targetID, Outcome: audit.OutcomeFailure, Details: map[string]any{"reason": reason},
	})
}

func (server *Server) requireEventStore(writer http.ResponseWriter, request *http.Request) bool {
	if server.eventStore != nil {
		return true
	}
	writeProblem(writer, request, http.StatusServiceUnavailable, "event_store_unavailable", "历史事件存储不可用", "当前服务未配置历史事件存储。")
	return false
}

func requirePrefixedETag(writer http.ResponseWriter, request *http.Request, prefix, resource string) (int64, bool) {
	value := request.Header.Get("If-Match")
	if value == "" {
		writeProblem(writer, request, http.StatusPreconditionRequired, "precondition_required", "缺少并发版本", "修改"+resource+"必须通过 If-Match 提交当前 ETag。")
		return 0, false
	}
	marker := `"` + prefix + "-"
	if !strings.HasPrefix(value, marker) || !strings.HasSuffix(value, `"`) {
		writeProblem(writer, request, http.StatusBadRequest, "invalid_if_match", "并发版本无效", "If-Match 必须使用最近一次历史事件响应中的强 ETag。")
		return 0, false
	}
	version, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(value, marker), `"`), 10, 64)
	if err != nil || version < 1 {
		writeProblem(writer, request, http.StatusBadRequest, "invalid_if_match", "并发版本无效", "If-Match 必须使用最近一次历史事件响应中的强 ETag。")
		return 0, false
	}
	return version, true
}

func draftETag(version int64) string { return fmt.Sprintf(`"draft-%d"`, version) }
func eventETag(version int64) string { return fmt.Sprintf(`"event-%d"`, version) }

func managedEventRequestHash(slug string, draft historyevent.Draft) []byte {
	payload, _ := json.Marshal(struct {
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
	}{
		Slug: slug, Title: draft.Title, Summary: draft.Summary, Narrative: draft.Narrative,
		Time: draft.Time, PrimaryCategory: draft.PrimaryCategory, Prominence: draft.Prominence,
		DisplayOrder: draft.DisplayOrder, RegionIDs: draft.RegionIDs, PlaceIDs: draft.PlaceIDs,
		PeriodIDs: draft.PeriodIDs, FigureIDs: draft.FigureIDs, TopicTagIDs: draft.TopicTagIDs,
	})
	hash := sha256.Sum256(payload)
	return hash[:]
}

func eventAuditDetails(slug string, draft historyevent.Draft) map[string]any {
	details := map[string]any{
		"hasTitle": draft.Title != "", "hasSummary": draft.Summary != "", "hasNarrative": draft.Narrative != "",
		"hasTime": draft.Time != nil, "regionCount": len(draft.RegionIDs), "placeCount": len(draft.PlaceIDs),
		"periodCount": len(draft.PeriodIDs), "figureCount": len(draft.FigureIDs), "topicTagCount": len(draft.TopicTagIDs),
	}
	if slug != "" {
		details["slug"] = slug
	}
	return details
}

func draftValidationDetail(err error) string {
	if errors.Is(err, historyevent.ErrInvalidTimeExpression) {
		return "时间表述必须是合法的精确日期、年份、约数年份或按时间顺序排列的闭区间；不存在公元 0 年。"
	}
	if errors.Is(err, historyevent.ErrInvalidAssociation) {
		return "规范实体标识必须是 UUID。"
	}
	return "标题、摘要、正文、主分类、显著度或展示顺序超出允许范围。"
}

func responseManagedEvent(value historyevent.Event) managedEventResponse {
	response := managedEventResponse{
		ID: value.ID, Slug: value.Slug, PublicationStatus: value.PublicationStatus,
		LockVersion: value.LockVersion, CreatedAt: value.CreatedAt.UTC().Format(timeFormat), UpdatedAt: value.UpdatedAt.UTC().Format(timeFormat),
	}
	if value.Draft != nil {
		response.Draft = &managedEventDraftResponse{
			Title: value.Draft.Title, Summary: value.Draft.Summary, Narrative: value.Draft.Narrative,
			Time: value.Draft.Time, PrimaryCategory: value.Draft.PrimaryCategory, Prominence: value.Draft.Prominence,
			DisplayOrder: value.Draft.DisplayOrder, Regions: responseEventReferences(value.Draft.Regions),
			Places: responseEventReferences(value.Draft.Places), Periods: responseEventReferences(value.Draft.Periods),
			Figures: responseEventReferences(value.Draft.Figures), TopicTags: responseEventReferences(value.Draft.TopicTags),
			LockVersion: value.Draft.LockVersion, BasedOnRevisionNo: value.Draft.BasedOnRevisionNo,
			CreatedAt: value.Draft.CreatedAt.UTC().Format(timeFormat), UpdatedAt: value.Draft.UpdatedAt.UTC().Format(timeFormat),
		}
	}
	return response
}

func responseEventReferences(values []historyevent.EntityReference) []eventReferenceResponse {
	response := make([]eventReferenceResponse, 0, len(values))
	for _, value := range values {
		response = append(response, eventReferenceResponse{ID: value.ID, Name: value.Name, DisambiguationLabel: value.DisambiguationLabel})
	}
	return response
}

func responseRevisionPublisher(value historyevent.Revision) eventRevisionPublisherResponse {
	return eventRevisionPublisherResponse{ID: value.PublishedBy, Email: value.PublishedByEmail}
}

func responseManagedEventRevision(value historyevent.Revision, current bool) managedEventRevisionResponse {
	return managedEventRevisionResponse{
		ID: value.ID, EventID: value.EventID, RevisionNo: value.RevisionNo,
		Title: value.Title, Summary: value.Summary, Narrative: value.Narrative, Time: value.Time,
		PrimaryCategory: value.PrimaryCategory, Prominence: value.Prominence, DisplayOrder: value.DisplayOrder,
		Regions: responseEventReferences(value.Regions), Places: responseEventReferences(value.Places),
		Periods: responseEventReferences(value.Periods), Figures: responseEventReferences(value.Figures),
		TopicTags: responseEventReferences(value.TopicTags), PublishedBy: responseRevisionPublisher(value),
		PublishedAt: value.PublishedAt.UTC().Format(timeFormat), Current: current,
	}
}
