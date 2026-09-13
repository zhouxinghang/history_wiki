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

const defaultRegionListLimit = 50

type regionResponse struct {
	ID                  string               `json:"id"`
	Name                string               `json:"name"`
	DisambiguationLabel *string              `json:"disambiguationLabel"`
	Status              catalog.EntityStatus `json:"status"`
	MergedIntoID        *string              `json:"mergedIntoId"`
	LockVersion         int64                `json:"lockVersion"`
	CreatedAt           string               `json:"createdAt"`
	UpdatedAt           string               `json:"updatedAt"`
}

type regionListResponse struct {
	Regions []regionResponse `json:"regions"`
}

type regionWriteRequest struct {
	Name                string                `json:"name"`
	DisambiguationLabel *string               `json:"disambiguationLabel"`
	Status              *catalog.EntityStatus `json:"status,omitempty"`
}

func (server *Server) listRegions(writer http.ResponseWriter, request *http.Request) {
	if !server.requireRegionStore(writer, request) {
		return
	}
	query := strings.TrimSpace(request.URL.Query().Get("q"))
	if utf8.RuneCountInString(query) > catalog.MaxRegionSearchLength {
		writeInvalidQuery(writer, request, fmt.Errorf("q 最长为 %d 个字符", catalog.MaxRegionSearchLength))
		return
	}
	status := catalog.EntityStatus(request.URL.Query().Get("status"))
	if !status.ValidFilter() {
		writeInvalidQuery(writer, request, errors.New("status 必须是 active、inactive 或 merged"))
		return
	}
	limit := defaultRegionListLimit
	if rawLimit := request.URL.Query().Get("limit"); rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil || parsed < 1 || parsed > 100 {
			writeInvalidQuery(writer, request, errors.New("limit 必须是 1 到 100 之间的整数"))
			return
		}
		limit = parsed
	}

	regions, err := server.regionStore.ListRegions(request.Context(), catalog.RegionListFilter{
		Query: query, Status: status, Limit: limit,
	})
	if err != nil {
		server.logger.Error("list regions", "error", err, "request_id", requestID(request))
		writeProblem(writer, request, http.StatusServiceUnavailable, "region_store_unavailable", "暂时无法读取地区", "数据库查询失败，请稍后重试。")
		return
	}
	response := make([]regionResponse, 0, len(regions))
	for _, region := range regions {
		response = append(response, responseRegion(region))
	}
	writeJSON(writer, http.StatusOK, regionListResponse{Regions: response})
}

func (server *Server) createRegion(writer http.ResponseWriter, request *http.Request) {
	if !server.requireRegionStore(writer, request) {
		return
	}
	current := principalFromContext(request.Context())
	var input regionWriteRequest
	if err := decodeJSONBody(writer, request, &input); err != nil {
		server.recordRegionWriteFailure(request, "region.create", "invalid_request", nil)
		writeJSONBodyProblem(writer, request, "地区创建请求无效", err)
		return
	}
	name, label, err := catalog.NormalizeRegionFields(input.Name, input.DisambiguationLabel)
	if err != nil {
		server.recordRegionWriteFailure(request, "region.create", "invalid_region", nil)
		writeProblem(writer, request, http.StatusUnprocessableEntity, "invalid_region", "地区信息无效", "名称不能为空且名称与消歧名称均不能超过 120 个字符。")
		return
	}
	idempotencyKey, err := catalog.NormalizeIdempotencyKey(request.Header.Get("Idempotency-Key"))
	if errors.Is(err, catalog.ErrIdempotencyKeyMissing) {
		server.recordRegionWriteFailure(request, "region.create", "idempotency_key_required", nil)
		writeProblem(writer, request, http.StatusBadRequest, "idempotency_key_required", "缺少幂等键", "创建地区必须提供 Idempotency-Key 请求头。")
		return
	}
	if err != nil {
		server.recordRegionWriteFailure(request, "region.create", "invalid_idempotency_key", nil)
		writeProblem(writer, request, http.StatusBadRequest, "invalid_idempotency_key", "幂等键无效", "Idempotency-Key 最长为 200 个字符。")
		return
	}

	regionID, err := identifier.New()
	if err != nil {
		server.logger.Error("generate region id", "error", err, "request_id", requestID(request))
		writeProblem(writer, request, http.StatusServiceUnavailable, "region_store_unavailable", "暂时无法创建地区", "无法生成安全标识，请稍后重试。")
		return
	}
	now := server.config.Now().UTC()
	region := catalog.Region{
		ID:                  regionID,
		Name:                name,
		DisambiguationLabel: label,
		Status:              catalog.StatusActive,
		CreatedBy:           current.Session.User.ID,
		UpdatedBy:           current.Session.User.ID,
		CreatedAt:           now,
		UpdatedAt:           now,
	}
	requestPayload, _ := json.Marshal(struct {
		Name                string  `json:"name"`
		DisambiguationLabel *string `json:"disambiguationLabel"`
	}{Name: name, DisambiguationLabel: label})
	requestHash := sha256.Sum256(requestPayload)
	created, replayed, err := server.regionStore.CreateRegion(request.Context(), region, idempotencyKey, requestHash[:], server.auditEntry(request, audit.Entry{
		RequestID:  requestID(request),
		Action:     "region.create",
		TargetType: "region",
		Outcome:    audit.OutcomeSuccess,
		Details: map[string]any{
			"name":                name,
			"disambiguationLabel": label,
			"status":              catalog.StatusActive,
		},
	}))
	if errors.Is(err, catalog.ErrIdempotencyConflict) {
		server.recordRegionWriteFailure(request, "region.create", "idempotency_conflict", nil)
		writeProblem(writer, request, http.StatusConflict, "idempotency_conflict", "重复提交内容不一致", "该 Idempotency-Key 已用于另一份地区创建请求。")
		return
	}
	if err != nil {
		server.logger.Error("create region", "error", err, "request_id", requestID(request))
		server.recordRegionWriteFailure(request, "region.create", "store_unavailable", nil)
		writeProblem(writer, request, http.StatusServiceUnavailable, "region_store_unavailable", "暂时无法创建地区", "数据库写入失败，请稍后重试。")
		return
	}
	if replayed {
		writer.Header().Set("Idempotency-Replayed", "true")
	}
	writer.Header().Set("Location", "/api/v1/admin/regions/"+created.ID)
	writer.Header().Set("ETag", regionETag(created.LockVersion))
	writeJSON(writer, http.StatusCreated, responseRegion(created))
}

func (server *Server) updateRegion(writer http.ResponseWriter, request *http.Request) {
	if !server.requireRegionStore(writer, request) {
		return
	}
	regionID := chi.URLParam(request, "regionID")
	if !identifier.Valid(regionID) {
		writeProblem(writer, request, http.StatusBadRequest, "invalid_region_id", "地区标识无效", "地区标识必须是 UUID。")
		return
	}
	expectedVersion, err := parseRegionETag(request.Header.Get("If-Match"))
	if errors.Is(err, errPreconditionRequired) {
		writeProblem(writer, request, http.StatusPreconditionRequired, "precondition_required", "缺少并发版本", "修改地区必须通过 If-Match 提交当前 ETag。")
		return
	}
	if err != nil {
		writeProblem(writer, request, http.StatusBadRequest, "invalid_if_match", "并发版本无效", "If-Match 必须是地区响应中的强 ETag。")
		return
	}

	var input regionWriteRequest
	if err := decodeJSONBody(writer, request, &input); err != nil {
		server.recordRegionWriteFailure(request, "region.update", "invalid_request", &regionID)
		writeJSONBodyProblem(writer, request, "地区修改请求无效", err)
		return
	}
	name, label, err := catalog.NormalizeRegionFields(input.Name, input.DisambiguationLabel)
	if err != nil || input.Status == nil || !input.Status.Writable() {
		server.recordRegionWriteFailure(request, "region.update", "invalid_region", &regionID)
		writeProblem(writer, request, http.StatusUnprocessableEntity, "invalid_region", "地区信息无效", "名称不能为空，状态必须是 active 或 inactive，名称与消歧名称均不能超过 120 个字符。")
		return
	}
	current := principalFromContext(request.Context())
	updated, err := server.regionStore.UpdateRegion(request.Context(), regionID, catalog.RegionUpdate{
		Name:                name,
		DisambiguationLabel: label,
		Status:              *input.Status,
		ExpectedVersion:     expectedVersion,
		UpdatedBy:           current.Session.User.ID,
		UpdatedAt:           server.config.Now().UTC(),
	}, server.auditEntry(request, audit.Entry{
		RequestID:  requestID(request),
		Action:     "region.update",
		TargetType: "region",
		Outcome:    audit.OutcomeSuccess,
		Details: map[string]any{
			"name":                name,
			"disambiguationLabel": label,
			"status":              *input.Status,
		},
	}))
	switch {
	case errors.Is(err, catalog.ErrRegionNotFound):
		server.recordRegionWriteFailure(request, "region.update", "not_found", &regionID)
		writeProblem(writer, request, http.StatusNotFound, "region_not_found", "地区不存在", "未找到指定地区。")
		return
	case errors.Is(err, catalog.ErrVersionConflict):
		server.recordRegionWriteFailure(request, "region.update", "version_conflict", &regionID)
		writeProblem(writer, request, http.StatusConflict, "region_version_conflict", "地区已被修改", "请刷新地区列表并基于最新版本重试。")
		return
	case errors.Is(err, catalog.ErrRegionNotWritable):
		server.recordRegionWriteFailure(request, "region.update", "merged", &regionID)
		writeProblem(writer, request, http.StatusConflict, "region_not_writable", "地区不可修改", "已合并地区必须通过规范实体治理流程处理。")
		return
	case err != nil:
		server.logger.Error("update region", "error", err, "request_id", requestID(request), "region_id", regionID)
		server.recordRegionWriteFailure(request, "region.update", "store_unavailable", &regionID)
		writeProblem(writer, request, http.StatusServiceUnavailable, "region_store_unavailable", "暂时无法修改地区", "数据库写入失败，请稍后重试。")
		return
	}
	writer.Header().Set("ETag", regionETag(updated.LockVersion))
	writeJSON(writer, http.StatusOK, responseRegion(updated))
}

func (server *Server) requireRegionStore(writer http.ResponseWriter, request *http.Request) bool {
	if server.regionStore != nil {
		return true
	}
	writeProblem(writer, request, http.StatusServiceUnavailable, "region_store_unavailable", "地区管理暂不可用", "地区存储尚未配置。")
	return false
}

func (server *Server) recordRegionWriteFailure(request *http.Request, action, reason string, targetID *string) {
	actorID := principalFromContext(request.Context()).Session.User.ID
	server.recordAudit(request, audit.Entry{
		RequestID:   requestID(request),
		ActorUserID: &actorID,
		Action:      action,
		TargetType:  "region",
		TargetID:    targetID,
		Outcome:     audit.OutcomeFailure,
		Details:     map[string]any{"reason": reason},
	})
}

func responseRegion(region catalog.Region) regionResponse {
	return regionResponse{
		ID:                  region.ID,
		Name:                region.Name,
		DisambiguationLabel: region.DisambiguationLabel,
		Status:              region.Status,
		MergedIntoID:        region.MergedIntoID,
		LockVersion:         region.LockVersion,
		CreatedAt:           region.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z"),
		UpdatedAt:           region.UpdatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z"),
	}
}

var errPreconditionRequired = errors.New("precondition required")

func parseRegionETag(value string) (int64, error) {
	if value == "" {
		return 0, errPreconditionRequired
	}
	if len(value) < 3 || value[0] != '"' || value[len(value)-1] != '"' {
		return 0, errors.New("invalid ETag")
	}
	version, err := strconv.ParseInt(value[1:len(value)-1], 10, 64)
	if err != nil || version < 1 {
		return 0, errors.New("invalid ETag")
	}
	return version, nil
}

func regionETag(version int64) string {
	return fmt.Sprintf("\"%d\"", version)
}
