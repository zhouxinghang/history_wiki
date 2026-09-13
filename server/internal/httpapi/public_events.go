package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	historyevent "github.com/zhouxinghang/history_wiki/server/internal/event"
	"github.com/zhouxinghang/history_wiki/server/internal/identifier"
)

type publishedEventResponse struct {
	ID              string                       `json:"id"`
	Slug            string                       `json:"slug"`
	Title           string                       `json:"title"`
	Summary         string                       `json:"summary"`
	Narrative       string                       `json:"narrative"`
	Time            historyevent.TimeExpression  `json:"time"`
	PrimaryCategory historyevent.PrimaryCategory `json:"primaryCategory"`
	Periods         []eventReferenceResponse     `json:"periods"`
	Regions         []eventReferenceResponse     `json:"regions"`
	Places          []eventReferenceResponse     `json:"places"`
	Figures         []eventReferenceResponse     `json:"figures"`
	TopicTags       []eventReferenceResponse     `json:"topicTags"`
	Prominence      int                          `json:"prominence"`
	DisplayOrder    int                          `json:"displayOrder"`
}

type publishedEventQueryResponse struct {
	Events             []publishedEventResponse `json:"events"`
	SourceTotal        int                      `json:"sourceTotal"`
	TotalMatching      int                      `json:"totalMatching"`
	ReturnedProminence int                      `json:"returnedProminence"`
}

type periodFilterGroupResponse struct {
	Context eventReferenceResponse   `json:"context"`
	Periods []eventReferenceResponse `json:"periods"`
}

type publishedEventMetadataResponse struct {
	PeriodGroups      []periodFilterGroupResponse    `json:"periodGroups"`
	Regions           []eventReferenceResponse       `json:"regions"`
	Figures           []eventReferenceResponse       `json:"figures"`
	PrimaryCategories []historyevent.PrimaryCategory `json:"primaryCategories"`
}

func (server *Server) readPublishedEventBounds(writer http.ResponseWriter, request *http.Request) {
	if server.publicEventStore == nil {
		if !server.requireEmptyCatalog(writer, request) {
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"hasEvents": false, "start": nil, "end": nil})
		return
	}
	bounds, err := server.publicEventStore.PublishedEventBounds(request.Context())
	if err != nil {
		server.logger.Error("read published event bounds", "error", err, "request_id", requestID(request))
		writeProblem(writer, request, http.StatusServiceUnavailable, "event_store_unavailable", "暂时无法读取历史事件", "数据库查询失败，请稍后重试。")
		return
	}
	if bounds == nil {
		writeJSON(writer, http.StatusOK, map[string]any{"hasEvents": false, "start": nil, "end": nil})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"hasEvents": true, "start": bounds.Start, "end": bounds.End})
}

func (server *Server) readPublishedEventMetadata(writer http.ResponseWriter, request *http.Request) {
	if server.publicEventStore == nil {
		if !server.requireEmptyCatalog(writer, request) {
			return
		}
		writeJSON(writer, http.StatusOK, publishedEventMetadataResponse{
			PeriodGroups: []periodFilterGroupResponse{}, Regions: []eventReferenceResponse{},
			Figures: []eventReferenceResponse{}, PrimaryCategories: []historyevent.PrimaryCategory{},
		})
		return
	}
	metadata, err := server.publicEventStore.PublishedEventMetadata(request.Context())
	if err != nil {
		server.logger.Error("read published event metadata", "error", err, "request_id", requestID(request))
		writeProblem(writer, request, http.StatusServiceUnavailable, "event_store_unavailable", "暂时无法读取历史事件", "数据库查询失败，请稍后重试。")
		return
	}
	response := publishedEventMetadataResponse{
		PeriodGroups: make([]periodFilterGroupResponse, 0, len(metadata.PeriodGroups)),
		Regions:      responseEventReferences(metadata.Regions), Figures: responseEventReferences(metadata.Figures),
		PrimaryCategories: append([]historyevent.PrimaryCategory(nil), metadata.PrimaryCategories...),
	}
	if response.Regions == nil {
		response.Regions = []eventReferenceResponse{}
	}
	if response.Figures == nil {
		response.Figures = []eventReferenceResponse{}
	}
	if response.PrimaryCategories == nil {
		response.PrimaryCategories = []historyevent.PrimaryCategory{}
	}
	for _, group := range metadata.PeriodGroups {
		response.PeriodGroups = append(response.PeriodGroups, periodFilterGroupResponse{
			Context: responseEventReference(group.Context), Periods: responseEventReferences(group.Periods),
		})
	}
	writeJSON(writer, http.StatusOK, response)
}

func (server *Server) readPublishedEvents(writer http.ResponseWriter, request *http.Request, from, to float64) {
	returnedProminence := prominenceForSpan(to - from)
	if server.publicEventStore == nil {
		if !server.requireEmptyCatalog(writer, request) {
			return
		}
		writeJSON(writer, http.StatusOK, publishedEventQueryResponse{
			Events: []publishedEventResponse{}, ReturnedProminence: returnedProminence,
		})
		return
	}
	periodIDs, ok := publicEntityFilter(writer, request, "period")
	if !ok {
		return
	}
	regionIDs, ok := publicEntityFilter(writer, request, "region")
	if !ok {
		return
	}
	figureIDs, ok := publicEntityFilter(writer, request, "figure")
	if !ok {
		return
	}
	categories, ok := publicCategoryFilter(writer, request)
	if !ok {
		return
	}
	result, err := server.publicEventStore.QueryPublishedEvents(request.Context(), historyevent.PublishedEventQuery{
		From: from, To: to, SearchTerm: strings.TrimSpace(request.URL.Query().Get("q")),
		PeriodIDs: periodIDs, RegionIDs: regionIDs, FigureIDs: figureIDs,
		PrimaryCategories: categories, MaximumProminence: returnedProminence,
	})
	if errors.Is(err, historyevent.ErrResultSetTooLarge) {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "result_set_too_large", "查询结果过多", "显著度筛选后的历史事件超过 5,000 条，请缩小时间范围。")
		return
	}
	if err != nil {
		server.logger.Error("query published events", "error", err, "request_id", requestID(request))
		writeProblem(writer, request, http.StatusServiceUnavailable, "event_store_unavailable", "暂时无法读取历史事件", "数据库查询失败，请稍后重试。")
		return
	}
	events := make([]publishedEventResponse, 0, len(result.Events))
	for _, published := range result.Events {
		events = append(events, responsePublishedEvent(published))
	}
	writeJSON(writer, http.StatusOK, publishedEventQueryResponse{
		Events: events, SourceTotal: result.SourceTotal, TotalMatching: result.TotalMatching,
		ReturnedProminence: returnedProminence,
	})
}

func (server *Server) readPublishedEventByID(writer http.ResponseWriter, request *http.Request) {
	eventID := chi.URLParam(request, "eventID")
	if !identifier.Valid(eventID) {
		writeProblem(writer, request, http.StatusBadRequest, "invalid_event_id", "历史事件标识无效", "历史事件标识必须是 UUID。")
		return
	}
	if server.publicEventStore == nil {
		if !server.requireEmptyCatalog(writer, request) {
			return
		}
		writeProblem(writer, request, http.StatusNotFound, "event_not_found", "历史事件不存在", "没有可公开查看的历史事件。")
		return
	}
	published, err := server.publicEventStore.PublishedEventByID(request.Context(), eventID)
	if errors.Is(err, historyevent.ErrEventNotFound) {
		writeProblem(writer, request, http.StatusNotFound, "event_not_found", "历史事件不存在", "未找到可公开查看的历史事件。")
		return
	}
	if err != nil {
		server.logger.Error("read published event", "error", err, "request_id", requestID(request), "event_id", eventID)
		writeProblem(writer, request, http.StatusServiceUnavailable, "event_store_unavailable", "暂时无法读取历史事件", "数据库查询失败，请稍后重试。")
		return
	}
	writeJSON(writer, http.StatusOK, responsePublishedEvent(published))
}

func publicEntityFilter(writer http.ResponseWriter, request *http.Request, name string) ([]string, bool) {
	values := request.URL.Query()[name]
	for _, value := range values {
		if !identifier.Valid(value) {
			writeInvalidQuery(writer, request, errors.New(name+" 筛选值必须是 UUID"))
			return nil, false
		}
	}
	return values, true
}

func publicCategoryFilter(writer http.ResponseWriter, request *http.Request) ([]historyevent.PrimaryCategory, bool) {
	values := request.URL.Query()["category"]
	categories := make([]historyevent.PrimaryCategory, 0, len(values))
	for _, value := range values {
		category := historyevent.PrimaryCategory(value)
		if !category.Valid() {
			writeInvalidQuery(writer, request, errors.New("category 筛选值不是固定主分类"))
			return nil, false
		}
		categories = append(categories, category)
	}
	return categories, true
}

func responsePublishedEvent(value historyevent.PublishedEvent) publishedEventResponse {
	revision := value.Revision
	return publishedEventResponse{
		ID: value.ID, Slug: value.Slug, Title: revision.Title, Summary: revision.Summary,
		Narrative: revision.Narrative, Time: revision.Time, PrimaryCategory: revision.PrimaryCategory,
		Periods: responseEventReferences(revision.Periods), Regions: responseEventReferences(revision.Regions),
		Places: responseEventReferences(revision.Places), Figures: responseEventReferences(revision.Figures),
		TopicTags: responseEventReferences(revision.TopicTags), Prominence: revision.Prominence,
		DisplayOrder: revision.DisplayOrder,
	}
}

func responseEventReference(value historyevent.EntityReference) eventReferenceResponse {
	return eventReferenceResponse{ID: value.ID, Name: value.Name, DisambiguationLabel: value.DisambiguationLabel}
}
