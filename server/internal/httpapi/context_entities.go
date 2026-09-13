package httpapi

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/zhouxinghang/history_wiki/server/internal/audit"
	"github.com/zhouxinghang/history_wiki/server/internal/catalog"
	"github.com/zhouxinghang/history_wiki/server/internal/identifier"
)

type regionReferenceResponse struct {
	ID                  string  `json:"id"`
	Name                string  `json:"name"`
	DisambiguationLabel *string `json:"disambiguationLabel"`
}

type contextEntityResponse struct {
	ID                  string                    `json:"id"`
	Name                string                    `json:"name"`
	DisambiguationLabel *string                   `json:"disambiguationLabel"`
	Status              catalog.EntityStatus      `json:"status"`
	MergedIntoID        *string                   `json:"mergedIntoId"`
	Regions             []regionReferenceResponse `json:"regions"`
	LockVersion         int64                     `json:"lockVersion"`
	CreatedAt           string                    `json:"createdAt"`
	UpdatedAt           string                    `json:"updatedAt"`
}

type placeListResponse struct {
	Places []contextEntityResponse `json:"places"`
}

type historicalPeriodListResponse struct {
	Periods []contextEntityResponse `json:"periods"`
}

type createContextEntityRequest struct {
	Name                string          `json:"name"`
	DisambiguationLabel json.RawMessage `json:"disambiguationLabel"`
	RegionIDs           json.RawMessage `json:"regionIds"`
}

type updateContextEntityRequest struct {
	Name                string                `json:"name"`
	DisambiguationLabel json.RawMessage       `json:"disambiguationLabel"`
	Status              *catalog.EntityStatus `json:"status"`
	RegionIDs           json.RawMessage       `json:"regionIds"`
}

func (server *Server) listPlaces(writer http.ResponseWriter, request *http.Request) {
	if !server.requireContextEntityStore(writer, request) {
		return
	}
	filter, ok := parseContextEntityFilter(writer, request)
	if !ok {
		return
	}
	places, err := server.contextEntityStore.ListPlaces(request.Context(), filter)
	if err != nil {
		server.logger.Error("list places", "error", err, "request_id", requestID(request))
		writeProblem(writer, request, http.StatusServiceUnavailable, "place_store_unavailable", "暂时无法读取地点", "数据库查询失败，请稍后重试。")
		return
	}
	response := make([]contextEntityResponse, 0, len(places))
	for _, place := range places {
		response = append(response, responsePlace(place))
	}
	writeJSON(writer, http.StatusOK, placeListResponse{Places: response})
}

func (server *Server) createPlace(writer http.ResponseWriter, request *http.Request) {
	if !server.requireContextEntityStore(writer, request) {
		return
	}
	var input createContextEntityRequest
	if err := decodeJSONBody(writer, request, &input); err != nil {
		server.recordContextEntityWriteFailure(request, "place.create", "place", "invalid_request", nil)
		writeJSONBodyProblem(writer, request, "地点创建请求无效", err)
		return
	}
	disambiguationLabel, requestedRegionIDs, err := requiredContextEntityFields(input.DisambiguationLabel, input.RegionIDs)
	if err != nil {
		server.recordContextEntityWriteFailure(request, "place.create", "place", "invalid_request", nil)
		writeProblem(writer, request, http.StatusBadRequest, "invalid_request", "地点创建请求无效", "disambiguationLabel 必须显式提交字符串或 null，regionIds 必须显式提交数组。")
		return
	}
	name, label, err := catalog.NormalizePlaceFields(input.Name, disambiguationLabel)
	regionIDs, regionErr := validateRegionIDs(requestedRegionIDs)
	if err != nil || regionErr != nil {
		server.recordContextEntityWriteFailure(request, "place.create", "place", "invalid_place", nil)
		writeProblem(writer, request, http.StatusUnprocessableEntity, "invalid_place", "地点信息无效", "名称不能为空，名称与消歧名称均不能超过 120 个字符，地区标识必须是 UUID。")
		return
	}
	idempotencyKey, ok := server.requireEntityIdempotencyKey(writer, request, "place.create", "place")
	if !ok {
		return
	}
	placeID, err := identifier.New()
	if err != nil {
		server.logger.Error("generate place id", "error", err, "request_id", requestID(request))
		writeProblem(writer, request, http.StatusServiceUnavailable, "place_store_unavailable", "暂时无法创建地点", "无法生成安全标识，请稍后重试。")
		return
	}
	current := principalFromContext(request.Context())
	now := server.config.Now().UTC()
	place := catalog.Place{
		ID: placeID, Name: name, DisambiguationLabel: label, Status: catalog.StatusActive,
		RegionIDs: regionIDs, CreatedBy: current.Session.User.ID, UpdatedBy: current.Session.User.ID,
		CreatedAt: now, UpdatedAt: now,
	}
	requestHash := contextEntityRequestHash(name, label, regionIDs)
	created, replayed, err := server.contextEntityStore.CreatePlace(request.Context(), place, idempotencyKey, requestHash, server.auditEntry(request, audit.Entry{
		RequestID: requestID(request), Action: "place.create", TargetType: "place", Outcome: audit.OutcomeSuccess,
		Details: map[string]any{"name": name, "disambiguationLabel": label, "status": catalog.StatusActive, "regionIds": regionIDs},
	}))
	if server.handleContextEntityWriteError(writer, request, err, contextEntityErrorOptions{
		action: "place.create", targetType: "place", targetID: nil,
		idempotencyConflictTitle: "重复提交内容不一致", idempotencyConflictDetail: "该 Idempotency-Key 已用于另一份地点创建请求。",
		associationTitle: "地点地区关系无效", associationDetail: "地点只能关联当前有效且存在的地区。",
		unavailableCode: "place_store_unavailable", unavailableTitle: "暂时无法创建地点",
	}) {
		return
	}
	if replayed {
		writer.Header().Set("Idempotency-Replayed", "true")
	}
	writer.Header().Set("Location", "/api/v1/admin/places/"+created.ID)
	writer.Header().Set("ETag", regionETag(created.LockVersion))
	writeJSON(writer, http.StatusCreated, responsePlace(created))
}

func (server *Server) updatePlace(writer http.ResponseWriter, request *http.Request) {
	if !server.requireContextEntityStore(writer, request) {
		return
	}
	placeID := chi.URLParam(request, "placeID")
	if !identifier.Valid(placeID) {
		writeProblem(writer, request, http.StatusBadRequest, "invalid_place_id", "地点标识无效", "地点标识必须是 UUID。")
		return
	}
	expectedVersion, ok := requireEntityVersion(writer, request, "地点")
	if !ok {
		return
	}
	var input updateContextEntityRequest
	if err := decodeJSONBody(writer, request, &input); err != nil {
		server.recordContextEntityWriteFailure(request, "place.update", "place", "invalid_request", &placeID)
		writeJSONBodyProblem(writer, request, "地点修改请求无效", err)
		return
	}
	disambiguationLabel, requestedRegionIDs, err := requiredContextEntityFields(input.DisambiguationLabel, input.RegionIDs)
	if err != nil {
		server.recordContextEntityWriteFailure(request, "place.update", "place", "invalid_request", &placeID)
		writeProblem(writer, request, http.StatusBadRequest, "invalid_request", "地点修改请求无效", "disambiguationLabel 必须显式提交字符串或 null，regionIds 必须显式提交数组。")
		return
	}
	name, label, err := catalog.NormalizePlaceFields(input.Name, disambiguationLabel)
	regionIDs, regionErr := validateRegionIDs(requestedRegionIDs)
	if err != nil || regionErr != nil || input.Status == nil || !input.Status.Writable() {
		server.recordContextEntityWriteFailure(request, "place.update", "place", "invalid_place", &placeID)
		writeProblem(writer, request, http.StatusUnprocessableEntity, "invalid_place", "地点信息无效", "名称不能为空，状态必须是 active 或 inactive，地区标识必须是 UUID。")
		return
	}
	current := principalFromContext(request.Context())
	updated, err := server.contextEntityStore.UpdatePlace(request.Context(), placeID, catalog.PlaceUpdate{
		Name: name, DisambiguationLabel: label, Status: *input.Status, RegionIDs: regionIDs,
		ExpectedVersion: expectedVersion, UpdatedBy: current.Session.User.ID, UpdatedAt: server.config.Now().UTC(),
	}, server.auditEntry(request, audit.Entry{
		RequestID: requestID(request), Action: "place.update", TargetType: "place", Outcome: audit.OutcomeSuccess,
		Details: map[string]any{"name": name, "disambiguationLabel": label, "status": *input.Status, "regionIds": regionIDs},
	}))
	if server.handleContextEntityWriteError(writer, request, err, contextEntityErrorOptions{
		action: "place.update", targetType: "place", targetID: &placeID,
		notFound: catalog.ErrPlaceNotFound, notFoundCode: "place_not_found", notFoundTitle: "地点不存在", notFoundDetail: "未找到指定地点。",
		versionConflict: catalog.ErrPlaceVersionConflict, versionCode: "place_version_conflict", versionTitle: "地点已被修改", versionDetail: "请刷新地点列表并基于最新版本重试。",
		notWritable: catalog.ErrPlaceNotWritable, notWritableCode: "place_not_writable", notWritableTitle: "地点不可修改",
		associationTitle: "地点地区关系无效", associationDetail: "地点只能关联当前有效且存在的地区。",
		unavailableCode: "place_store_unavailable", unavailableTitle: "暂时无法修改地点",
	}) {
		return
	}
	writer.Header().Set("ETag", regionETag(updated.LockVersion))
	writeJSON(writer, http.StatusOK, responsePlace(updated))
}

func (server *Server) listHistoricalPeriods(writer http.ResponseWriter, request *http.Request) {
	if !server.requireContextEntityStore(writer, request) {
		return
	}
	filter, ok := parseContextEntityFilter(writer, request)
	if !ok {
		return
	}
	periods, err := server.contextEntityStore.ListHistoricalPeriods(request.Context(), filter)
	if err != nil {
		server.logger.Error("list historical periods", "error", err, "request_id", requestID(request))
		writeProblem(writer, request, http.StatusServiceUnavailable, "historical_period_store_unavailable", "暂时无法读取历史时期", "数据库查询失败，请稍后重试。")
		return
	}
	response := make([]contextEntityResponse, 0, len(periods))
	for _, period := range periods {
		response = append(response, responseHistoricalPeriod(period))
	}
	writeJSON(writer, http.StatusOK, historicalPeriodListResponse{Periods: response})
}

func (server *Server) createHistoricalPeriod(writer http.ResponseWriter, request *http.Request) {
	if !server.requireContextEntityStore(writer, request) {
		return
	}
	var input createContextEntityRequest
	if err := decodeJSONBody(writer, request, &input); err != nil {
		server.recordContextEntityWriteFailure(request, "historical_period.create", "historical_period", "invalid_request", nil)
		writeJSONBodyProblem(writer, request, "历史时期创建请求无效", err)
		return
	}
	disambiguationLabel, requestedRegionIDs, err := requiredContextEntityFields(input.DisambiguationLabel, input.RegionIDs)
	if err != nil {
		server.recordContextEntityWriteFailure(request, "historical_period.create", "historical_period", "invalid_request", nil)
		writeProblem(writer, request, http.StatusBadRequest, "invalid_request", "历史时期创建请求无效", "disambiguationLabel 必须显式提交字符串或 null，regionIds 必须显式提交数组。")
		return
	}
	name, label, err := catalog.NormalizeHistoricalPeriodFields(input.Name, disambiguationLabel)
	regionIDs, regionErr := validateRegionIDs(requestedRegionIDs)
	if err != nil || regionErr != nil || len(regionIDs) == 0 {
		server.recordContextEntityWriteFailure(request, "historical_period.create", "historical_period", "invalid_historical_period", nil)
		writeProblem(writer, request, http.StatusUnprocessableEntity, "invalid_historical_period", "历史时期信息无效", "名称不能为空，历史时期必须关联至少一个地区，且地区标识必须是 UUID。")
		return
	}
	idempotencyKey, ok := server.requireEntityIdempotencyKey(writer, request, "historical_period.create", "historical_period")
	if !ok {
		return
	}
	periodID, err := identifier.New()
	if err != nil {
		server.logger.Error("generate historical period id", "error", err, "request_id", requestID(request))
		writeProblem(writer, request, http.StatusServiceUnavailable, "historical_period_store_unavailable", "暂时无法创建历史时期", "无法生成安全标识，请稍后重试。")
		return
	}
	current := principalFromContext(request.Context())
	now := server.config.Now().UTC()
	period := catalog.HistoricalPeriod{
		ID: periodID, Name: name, DisambiguationLabel: label, Status: catalog.StatusActive,
		RegionIDs: regionIDs, CreatedBy: current.Session.User.ID, UpdatedBy: current.Session.User.ID,
		CreatedAt: now, UpdatedAt: now,
	}
	requestHash := contextEntityRequestHash(name, label, regionIDs)
	created, replayed, err := server.contextEntityStore.CreateHistoricalPeriod(request.Context(), period, idempotencyKey, requestHash, server.auditEntry(request, audit.Entry{
		RequestID: requestID(request), Action: "historical_period.create", TargetType: "historical_period", Outcome: audit.OutcomeSuccess,
		Details: map[string]any{"name": name, "disambiguationLabel": label, "status": catalog.StatusActive, "regionIds": regionIDs},
	}))
	if server.handleContextEntityWriteError(writer, request, err, contextEntityErrorOptions{
		action: "historical_period.create", targetType: "historical_period",
		idempotencyConflictTitle: "重复提交内容不一致", idempotencyConflictDetail: "该 Idempotency-Key 已用于另一份历史时期创建请求。",
		associationTitle: "历史时期地区关系无效", associationDetail: "有效历史时期必须关联至少一个当前有效且存在的地区。",
		unavailableCode: "historical_period_store_unavailable", unavailableTitle: "暂时无法创建历史时期",
	}) {
		return
	}
	if replayed {
		writer.Header().Set("Idempotency-Replayed", "true")
	}
	writer.Header().Set("Location", "/api/v1/admin/periods/"+created.ID)
	writer.Header().Set("ETag", regionETag(created.LockVersion))
	writeJSON(writer, http.StatusCreated, responseHistoricalPeriod(created))
}

func (server *Server) updateHistoricalPeriod(writer http.ResponseWriter, request *http.Request) {
	if !server.requireContextEntityStore(writer, request) {
		return
	}
	periodID := chi.URLParam(request, "periodID")
	if !identifier.Valid(periodID) {
		writeProblem(writer, request, http.StatusBadRequest, "invalid_historical_period_id", "历史时期标识无效", "历史时期标识必须是 UUID。")
		return
	}
	expectedVersion, ok := requireEntityVersion(writer, request, "历史时期")
	if !ok {
		return
	}
	var input updateContextEntityRequest
	if err := decodeJSONBody(writer, request, &input); err != nil {
		server.recordContextEntityWriteFailure(request, "historical_period.update", "historical_period", "invalid_request", &periodID)
		writeJSONBodyProblem(writer, request, "历史时期修改请求无效", err)
		return
	}
	disambiguationLabel, requestedRegionIDs, err := requiredContextEntityFields(input.DisambiguationLabel, input.RegionIDs)
	if err != nil {
		server.recordContextEntityWriteFailure(request, "historical_period.update", "historical_period", "invalid_request", &periodID)
		writeProblem(writer, request, http.StatusBadRequest, "invalid_request", "历史时期修改请求无效", "disambiguationLabel 必须显式提交字符串或 null，regionIds 必须显式提交数组。")
		return
	}
	name, label, err := catalog.NormalizeHistoricalPeriodFields(input.Name, disambiguationLabel)
	regionIDs, regionErr := validateRegionIDs(requestedRegionIDs)
	if err != nil || regionErr != nil || input.Status == nil || !input.Status.Writable() || (*input.Status == catalog.StatusActive && len(regionIDs) == 0) {
		server.recordContextEntityWriteFailure(request, "historical_period.update", "historical_period", "invalid_historical_period", &periodID)
		writeProblem(writer, request, http.StatusUnprocessableEntity, "invalid_historical_period", "历史时期信息无效", "有效历史时期必须关联至少一个地区，状态必须是 active 或 inactive，且地区标识必须是 UUID。")
		return
	}
	current := principalFromContext(request.Context())
	updated, err := server.contextEntityStore.UpdateHistoricalPeriod(request.Context(), periodID, catalog.HistoricalPeriodUpdate{
		Name: name, DisambiguationLabel: label, Status: *input.Status, RegionIDs: regionIDs,
		ExpectedVersion: expectedVersion, UpdatedBy: current.Session.User.ID, UpdatedAt: server.config.Now().UTC(),
	}, server.auditEntry(request, audit.Entry{
		RequestID: requestID(request), Action: "historical_period.update", TargetType: "historical_period", Outcome: audit.OutcomeSuccess,
		Details: map[string]any{"name": name, "disambiguationLabel": label, "status": *input.Status, "regionIds": regionIDs},
	}))
	if server.handleContextEntityWriteError(writer, request, err, contextEntityErrorOptions{
		action: "historical_period.update", targetType: "historical_period", targetID: &periodID,
		notFound: catalog.ErrHistoricalPeriodNotFound, notFoundCode: "historical_period_not_found", notFoundTitle: "历史时期不存在", notFoundDetail: "未找到指定历史时期。",
		versionConflict: catalog.ErrHistoricalPeriodVersionConflict, versionCode: "historical_period_version_conflict", versionTitle: "历史时期已被修改", versionDetail: "请刷新历史时期列表并基于最新版本重试。",
		notWritable: catalog.ErrHistoricalPeriodNotWritable, notWritableCode: "historical_period_not_writable", notWritableTitle: "历史时期不可修改",
		associationTitle: "历史时期地区关系无效", associationDetail: "有效历史时期必须关联至少一个当前有效且存在的地区。",
		unavailableCode: "historical_period_store_unavailable", unavailableTitle: "暂时无法修改历史时期",
	}) {
		return
	}
	writer.Header().Set("ETag", regionETag(updated.LockVersion))
	writeJSON(writer, http.StatusOK, responseHistoricalPeriod(updated))
}

func parseContextEntityFilter(writer http.ResponseWriter, request *http.Request) (catalog.ContextEntityListFilter, bool) {
	query := strings.TrimSpace(request.URL.Query().Get("q"))
	if utf8.RuneCountInString(query) > catalog.MaxRegionSearchLength {
		writeInvalidQuery(writer, request, fmt.Errorf("q 最长为 %d 个字符", catalog.MaxRegionSearchLength))
		return catalog.ContextEntityListFilter{}, false
	}
	status := catalog.EntityStatus(request.URL.Query().Get("status"))
	if !status.ValidFilter() {
		writeInvalidQuery(writer, request, errors.New("status 必须是 active、inactive 或 merged"))
		return catalog.ContextEntityListFilter{}, false
	}
	limit := defaultRegionListLimit
	if rawLimit := request.URL.Query().Get("limit"); rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil || parsed < 1 || parsed > 100 {
			writeInvalidQuery(writer, request, errors.New("limit 必须是 1 到 100 之间的整数"))
			return catalog.ContextEntityListFilter{}, false
		}
		limit = parsed
	}
	return catalog.ContextEntityListFilter{Query: query, Status: status, Limit: limit}, true
}

func (server *Server) requireContextEntityStore(writer http.ResponseWriter, request *http.Request) bool {
	if server.contextEntityStore != nil {
		return true
	}
	writeProblem(writer, request, http.StatusServiceUnavailable, "context_entity_store_unavailable", "语境实体管理暂不可用", "地点与历史时期存储尚未配置。")
	return false
}

func validateRegionIDs(values []string) ([]string, error) {
	regionIDs := catalog.NormalizeRegionIDs(values)
	for _, regionID := range regionIDs {
		if !identifier.Valid(regionID) {
			return nil, catalog.ErrInvalidRegionAssociation
		}
	}
	return regionIDs, nil
}

func requiredContextEntityFields(disambiguationLabel, regionIDs json.RawMessage) (*string, []string, error) {
	label, err := requiredNullableString(disambiguationLabel)
	if err != nil {
		return nil, nil, err
	}
	if len(regionIDs) == 0 || string(regionIDs) == "null" {
		return nil, nil, errors.New("required region ID array is missing")
	}
	var values []string
	if err := json.Unmarshal(regionIDs, &values); err != nil {
		return nil, nil, errors.New("regionIds must be an array of UUID strings")
	}
	return label, values, nil
}

func contextEntityRequestHash(name string, label *string, regionIDs []string) []byte {
	payload, _ := json.Marshal(struct {
		Name                string   `json:"name"`
		DisambiguationLabel *string  `json:"disambiguationLabel"`
		RegionIDs           []string `json:"regionIds"`
	}{Name: name, DisambiguationLabel: label, RegionIDs: regionIDs})
	hash := sha256.Sum256(payload)
	return hash[:]
}

func (server *Server) requireEntityIdempotencyKey(writer http.ResponseWriter, request *http.Request, action, targetType string) (string, bool) {
	key, err := catalog.NormalizeIdempotencyKey(request.Header.Get("Idempotency-Key"))
	if errors.Is(err, catalog.ErrIdempotencyKeyMissing) {
		server.recordContextEntityWriteFailure(request, action, targetType, "idempotency_key_required", nil)
		writeProblem(writer, request, http.StatusBadRequest, "idempotency_key_required", "缺少幂等键", "创建规范实体必须提供 Idempotency-Key 请求头。")
		return "", false
	}
	if err != nil {
		server.recordContextEntityWriteFailure(request, action, targetType, "invalid_idempotency_key", nil)
		writeProblem(writer, request, http.StatusBadRequest, "invalid_idempotency_key", "幂等键无效", "Idempotency-Key 最长为 200 个字符。")
		return "", false
	}
	return key, true
}

func requireEntityVersion(writer http.ResponseWriter, request *http.Request, entityName string) (int64, bool) {
	version, err := parseRegionETag(request.Header.Get("If-Match"))
	if errors.Is(err, errPreconditionRequired) {
		writeProblem(writer, request, http.StatusPreconditionRequired, "precondition_required", "缺少并发版本", "修改"+entityName+"必须通过 If-Match 提交当前 ETag。")
		return 0, false
	}
	if err != nil {
		writeProblem(writer, request, http.StatusBadRequest, "invalid_if_match", "并发版本无效", "If-Match 必须是规范实体响应中的强 ETag。")
		return 0, false
	}
	return version, true
}

type contextEntityErrorOptions struct {
	action, targetType        string
	targetID                  *string
	notFound                  error
	notFoundCode              string
	notFoundTitle             string
	notFoundDetail            string
	versionConflict           error
	versionCode               string
	versionTitle              string
	versionDetail             string
	notWritable               error
	notWritableCode           string
	notWritableTitle          string
	idempotencyConflictTitle  string
	idempotencyConflictDetail string
	associationTitle          string
	associationDetail         string
	unavailableCode           string
	unavailableTitle          string
}

func (server *Server) handleContextEntityWriteError(writer http.ResponseWriter, request *http.Request, err error, options contextEntityErrorOptions) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, catalog.ErrIdempotencyConflict):
		server.recordContextEntityWriteFailure(request, options.action, options.targetType, "idempotency_conflict", options.targetID)
		writeProblem(writer, request, http.StatusConflict, "idempotency_conflict", options.idempotencyConflictTitle, options.idempotencyConflictDetail)
	case errors.Is(err, catalog.ErrInvalidRegionAssociation):
		server.recordContextEntityWriteFailure(request, options.action, options.targetType, "invalid_region_association", options.targetID)
		writeProblem(writer, request, http.StatusUnprocessableEntity, "invalid_region_association", options.associationTitle, options.associationDetail)
	case options.notFound != nil && errors.Is(err, options.notFound):
		server.recordContextEntityWriteFailure(request, options.action, options.targetType, "not_found", options.targetID)
		writeProblem(writer, request, http.StatusNotFound, options.notFoundCode, options.notFoundTitle, options.notFoundDetail)
	case options.versionConflict != nil && errors.Is(err, options.versionConflict):
		server.recordContextEntityWriteFailure(request, options.action, options.targetType, "version_conflict", options.targetID)
		writeProblem(writer, request, http.StatusConflict, options.versionCode, options.versionTitle, options.versionDetail)
	case options.notWritable != nil && errors.Is(err, options.notWritable):
		server.recordContextEntityWriteFailure(request, options.action, options.targetType, "merged", options.targetID)
		writeProblem(writer, request, http.StatusConflict, options.notWritableCode, options.notWritableTitle, "已合并规范实体必须通过后续治理流程处理。")
	default:
		server.logger.Error("write context entity", "error", err, "request_id", requestID(request), "target_type", options.targetType)
		server.recordContextEntityWriteFailure(request, options.action, options.targetType, "store_unavailable", options.targetID)
		writeProblem(writer, request, http.StatusServiceUnavailable, options.unavailableCode, options.unavailableTitle, "数据库写入失败，请稍后重试。")
	}
	return true
}

func (server *Server) recordContextEntityWriteFailure(request *http.Request, action, targetType, reason string, targetID *string) {
	actorID := principalFromContext(request.Context()).Session.User.ID
	server.recordAudit(request, audit.Entry{
		RequestID: requestID(request), ActorUserID: &actorID, Action: action, TargetType: targetType,
		TargetID: targetID, Outcome: audit.OutcomeFailure, Details: map[string]any{"reason": reason},
	})
}

func responsePlace(place catalog.Place) contextEntityResponse {
	return responseContextEntity(place.ID, place.Name, place.DisambiguationLabel, place.Status, place.MergedIntoID, place.Regions, place.LockVersion, place.CreatedAt.UTC().Format(timeFormat), place.UpdatedAt.UTC().Format(timeFormat))
}

func responseHistoricalPeriod(period catalog.HistoricalPeriod) contextEntityResponse {
	return responseContextEntity(period.ID, period.Name, period.DisambiguationLabel, period.Status, period.MergedIntoID, period.Regions, period.LockVersion, period.CreatedAt.UTC().Format(timeFormat), period.UpdatedAt.UTC().Format(timeFormat))
}

const timeFormat = "2006-01-02T15:04:05.999999999Z"

func responseContextEntity(id, name string, label *string, status catalog.EntityStatus, mergedIntoID *string, regions []catalog.RegionReference, lockVersion int64, createdAt, updatedAt string) contextEntityResponse {
	regionResponses := make([]regionReferenceResponse, 0, len(regions))
	for _, region := range regions {
		regionResponses = append(regionResponses, regionReferenceResponse{ID: region.ID, Name: region.Name, DisambiguationLabel: region.DisambiguationLabel})
	}
	return contextEntityResponse{
		ID: id, Name: name, DisambiguationLabel: label, Status: status, MergedIntoID: mergedIntoID, Regions: regionResponses,
		LockVersion: lockVersion, CreatedAt: createdAt, UpdatedAt: updatedAt,
	}
}
