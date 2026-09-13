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

const defaultCanonicalEntityListLimit = 50

type canonicalEntityHTTPDefinition struct {
	Kind          catalog.CanonicalEntityKind
	Path          string
	ListField     string
	DisplayName   string
	ActionPrefix  string
	TargetType    string
	ProblemPrefix string
}

var historicalFigureHTTPDefinition = canonicalEntityHTTPDefinition{
	Kind:          catalog.KindHistoricalFigure,
	Path:          "figures",
	ListField:     "figures",
	DisplayName:   "历史人物",
	ActionPrefix:  "historical_figure",
	TargetType:    "historical_figure",
	ProblemPrefix: "historical_figure",
}

var topicTagHTTPDefinition = canonicalEntityHTTPDefinition{
	Kind:          catalog.KindTopicTag,
	Path:          "topic-tags",
	ListField:     "topicTags",
	DisplayName:   "主题标签",
	ActionPrefix:  "topic_tag",
	TargetType:    "topic_tag",
	ProblemPrefix: "topic_tag",
}

type canonicalEntityResponse struct {
	ID                  string               `json:"id"`
	Name                string               `json:"name"`
	DisambiguationLabel *string              `json:"disambiguationLabel"`
	Status              catalog.EntityStatus `json:"status"`
	MergedIntoID        *string              `json:"mergedIntoId"`
	LockVersion         int64                `json:"lockVersion"`
	CreatedAt           string               `json:"createdAt"`
	UpdatedAt           string               `json:"updatedAt"`
}

type createCanonicalEntityRequest struct {
	Name                string          `json:"name"`
	DisambiguationLabel json.RawMessage `json:"disambiguationLabel"`
}

type updateCanonicalEntityRequest struct {
	Name                string                `json:"name"`
	DisambiguationLabel json.RawMessage       `json:"disambiguationLabel"`
	Status              *catalog.EntityStatus `json:"status"`
}

func (server *Server) listCanonicalEntities(definition canonicalEntityHTTPDefinition) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if !server.requireCanonicalEntityStore(writer, request, definition) {
			return
		}
		query := strings.TrimSpace(request.URL.Query().Get("q"))
		if utf8.RuneCountInString(query) > catalog.MaxCanonicalEntitySearchLength {
			writeInvalidQuery(writer, request, fmt.Errorf("q 最长为 %d 个字符", catalog.MaxCanonicalEntitySearchLength))
			return
		}
		status := catalog.EntityStatus(request.URL.Query().Get("status"))
		if !status.ValidFilter() {
			writeInvalidQuery(writer, request, errors.New("status 必须是 active、inactive 或 merged"))
			return
		}
		limit := defaultCanonicalEntityListLimit
		if rawLimit := request.URL.Query().Get("limit"); rawLimit != "" {
			parsed, err := strconv.Atoi(rawLimit)
			if err != nil || parsed < 1 || parsed > 100 {
				writeInvalidQuery(writer, request, errors.New("limit 必须是 1 到 100 之间的整数"))
				return
			}
			limit = parsed
		}

		entities, err := server.canonicalEntityStore.ListCanonicalEntities(request.Context(), definition.Kind, catalog.CanonicalEntityListFilter{
			Query: query, Status: status, Limit: limit,
		})
		if err != nil {
			server.logger.Error("list canonical entities", "error", err, "kind", definition.Kind, "request_id", requestID(request))
			writeProblem(writer, request, http.StatusServiceUnavailable, definition.ProblemPrefix+"_store_unavailable", "暂时无法读取"+definition.DisplayName, "数据库查询失败，请稍后重试。")
			return
		}
		response := make([]canonicalEntityResponse, 0, len(entities))
		for _, entity := range entities {
			response = append(response, responseCanonicalEntity(entity))
		}
		writeJSON(writer, http.StatusOK, map[string]any{definition.ListField: response})
	}
}

func (server *Server) createCanonicalEntity(definition canonicalEntityHTTPDefinition) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if !server.requireCanonicalEntityStore(writer, request, definition) {
			return
		}
		current := principalFromContext(request.Context())
		var input createCanonicalEntityRequest
		if err := decodeJSONBody(writer, request, &input); err != nil {
			server.recordCanonicalEntityWriteFailure(request, definition, definition.ActionPrefix+".create", "invalid_request", nil)
			writeJSONBodyProblem(writer, request, definition.DisplayName+"创建请求无效", err)
			return
		}
		disambiguationLabel, err := requiredNullableString(input.DisambiguationLabel)
		if err != nil {
			server.recordCanonicalEntityWriteFailure(request, definition, definition.ActionPrefix+".create", "invalid_request", nil)
			writeProblem(writer, request, http.StatusBadRequest, "invalid_request", definition.DisplayName+"创建请求无效", "disambiguationLabel 必须显式提交字符串或 null。")
			return
		}
		name, label, err := catalog.NormalizeCanonicalEntityFields(input.Name, disambiguationLabel)
		if err != nil {
			server.recordCanonicalEntityWriteFailure(request, definition, definition.ActionPrefix+".create", "invalid_entity", nil)
			writeProblem(writer, request, http.StatusUnprocessableEntity, "invalid_"+definition.ProblemPrefix, definition.DisplayName+"信息无效", "名称不能为空且名称与消歧名称均不能超过 120 个字符。")
			return
		}
		idempotencyKey, err := catalog.NormalizeIdempotencyKey(request.Header.Get("Idempotency-Key"))
		if errors.Is(err, catalog.ErrIdempotencyKeyMissing) {
			server.recordCanonicalEntityWriteFailure(request, definition, definition.ActionPrefix+".create", "idempotency_key_required", nil)
			writeProblem(writer, request, http.StatusBadRequest, "idempotency_key_required", "缺少幂等键", "创建"+definition.DisplayName+"必须提供 Idempotency-Key 请求头。")
			return
		}
		if err != nil {
			server.recordCanonicalEntityWriteFailure(request, definition, definition.ActionPrefix+".create", "invalid_idempotency_key", nil)
			writeProblem(writer, request, http.StatusBadRequest, "invalid_idempotency_key", "幂等键无效", "Idempotency-Key 最长为 200 个字符。")
			return
		}

		entityID, err := identifier.New()
		if err != nil {
			server.logger.Error("generate canonical entity id", "error", err, "kind", definition.Kind, "request_id", requestID(request))
			server.recordCanonicalEntityWriteFailure(request, definition, definition.ActionPrefix+".create", "identifier_unavailable", nil)
			writeProblem(writer, request, http.StatusServiceUnavailable, definition.ProblemPrefix+"_store_unavailable", "暂时无法创建"+definition.DisplayName, "无法生成安全标识，请稍后重试。")
			return
		}
		now := server.config.Now().UTC()
		entity := catalog.CanonicalEntity{
			ID:                  entityID,
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
		created, replayed, err := server.canonicalEntityStore.CreateCanonicalEntity(
			request.Context(),
			definition.Kind,
			entity,
			idempotencyKey,
			requestHash[:],
			server.auditEntry(request, audit.Entry{
				RequestID:  requestID(request),
				Action:     definition.ActionPrefix + ".create",
				TargetType: definition.TargetType,
				Outcome:    audit.OutcomeSuccess,
				Details: map[string]any{
					"name":                name,
					"disambiguationLabel": label,
					"status":              catalog.StatusActive,
				},
			}),
		)
		if errors.Is(err, catalog.ErrIdempotencyConflict) {
			server.recordCanonicalEntityWriteFailure(request, definition, definition.ActionPrefix+".create", "idempotency_conflict", nil)
			writeProblem(writer, request, http.StatusConflict, "idempotency_conflict", "重复提交内容不一致", "该 Idempotency-Key 已用于另一份"+definition.DisplayName+"创建请求。")
			return
		}
		if err != nil {
			server.logger.Error("create canonical entity", "error", err, "kind", definition.Kind, "request_id", requestID(request))
			server.recordCanonicalEntityWriteFailure(request, definition, definition.ActionPrefix+".create", "store_unavailable", nil)
			writeProblem(writer, request, http.StatusServiceUnavailable, definition.ProblemPrefix+"_store_unavailable", "暂时无法创建"+definition.DisplayName, "数据库写入失败，请稍后重试。")
			return
		}
		if replayed {
			writer.Header().Set("Idempotency-Replayed", "true")
		}
		writer.Header().Set("Location", "/api/v1/admin/"+definition.Path+"/"+created.ID)
		writer.Header().Set("ETag", canonicalEntityETag(created.LockVersion))
		writeJSON(writer, http.StatusCreated, responseCanonicalEntity(created))
	}
}

func (server *Server) updateCanonicalEntity(definition canonicalEntityHTTPDefinition) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if !server.requireCanonicalEntityStore(writer, request, definition) {
			return
		}
		entityID := chi.URLParam(request, "entityID")
		if !identifier.Valid(entityID) {
			server.recordCanonicalEntityWriteFailure(request, definition, definition.ActionPrefix+".update", "invalid_id", &entityID)
			writeProblem(writer, request, http.StatusBadRequest, "invalid_"+definition.ProblemPrefix+"_id", definition.DisplayName+"标识无效", definition.DisplayName+"标识必须是 UUID。")
			return
		}
		expectedVersion, err := parseCanonicalEntityETag(request.Header.Get("If-Match"))
		if errors.Is(err, errPreconditionRequired) {
			server.recordCanonicalEntityWriteFailure(request, definition, definition.ActionPrefix+".update", "precondition_required", &entityID)
			writeProblem(writer, request, http.StatusPreconditionRequired, "precondition_required", "缺少并发版本", "修改"+definition.DisplayName+"必须通过 If-Match 提交当前 ETag。")
			return
		}
		if err != nil {
			server.recordCanonicalEntityWriteFailure(request, definition, definition.ActionPrefix+".update", "invalid_if_match", &entityID)
			writeProblem(writer, request, http.StatusBadRequest, "invalid_if_match", "并发版本无效", "If-Match 必须是"+definition.DisplayName+"响应中的强 ETag。")
			return
		}

		var input updateCanonicalEntityRequest
		if err := decodeJSONBody(writer, request, &input); err != nil {
			server.recordCanonicalEntityWriteFailure(request, definition, definition.ActionPrefix+".update", "invalid_request", &entityID)
			writeJSONBodyProblem(writer, request, definition.DisplayName+"修改请求无效", err)
			return
		}
		disambiguationLabel, err := requiredNullableString(input.DisambiguationLabel)
		if err != nil {
			server.recordCanonicalEntityWriteFailure(request, definition, definition.ActionPrefix+".update", "invalid_request", &entityID)
			writeProblem(writer, request, http.StatusBadRequest, "invalid_request", definition.DisplayName+"修改请求无效", "disambiguationLabel 必须显式提交字符串或 null。")
			return
		}
		name, label, err := catalog.NormalizeCanonicalEntityFields(input.Name, disambiguationLabel)
		if err != nil || input.Status == nil || !input.Status.Writable() {
			server.recordCanonicalEntityWriteFailure(request, definition, definition.ActionPrefix+".update", "invalid_entity", &entityID)
			writeProblem(writer, request, http.StatusUnprocessableEntity, "invalid_"+definition.ProblemPrefix, definition.DisplayName+"信息无效", "名称不能为空，状态必须是 active 或 inactive，名称与消歧名称均不能超过 120 个字符。")
			return
		}
		current := principalFromContext(request.Context())
		updated, err := server.canonicalEntityStore.UpdateCanonicalEntity(request.Context(), definition.Kind, entityID, catalog.CanonicalEntityUpdate{
			Name:                name,
			DisambiguationLabel: label,
			Status:              *input.Status,
			ExpectedVersion:     expectedVersion,
			UpdatedBy:           current.Session.User.ID,
			UpdatedAt:           server.config.Now().UTC(),
		}, server.auditEntry(request, audit.Entry{
			RequestID:  requestID(request),
			Action:     definition.ActionPrefix + ".update",
			TargetType: definition.TargetType,
			Outcome:    audit.OutcomeSuccess,
			Details: map[string]any{
				"name":                name,
				"disambiguationLabel": label,
				"status":              *input.Status,
			},
		}))
		switch {
		case errors.Is(err, catalog.ErrCanonicalEntityNotFound):
			server.recordCanonicalEntityWriteFailure(request, definition, definition.ActionPrefix+".update", "not_found", &entityID)
			writeProblem(writer, request, http.StatusNotFound, definition.ProblemPrefix+"_not_found", definition.DisplayName+"不存在", "未找到指定"+definition.DisplayName+"。")
			return
		case errors.Is(err, catalog.ErrVersionConflict):
			server.recordCanonicalEntityWriteFailure(request, definition, definition.ActionPrefix+".update", "version_conflict", &entityID)
			writeProblem(writer, request, http.StatusConflict, definition.ProblemPrefix+"_version_conflict", definition.DisplayName+"已被修改", "请刷新列表并基于最新版本重试。")
			return
		case errors.Is(err, catalog.ErrCanonicalEntityNotWritable):
			server.recordCanonicalEntityWriteFailure(request, definition, definition.ActionPrefix+".update", "merged", &entityID)
			writeProblem(writer, request, http.StatusConflict, definition.ProblemPrefix+"_not_writable", definition.DisplayName+"不可修改", "已合并实体必须通过规范实体治理流程处理。")
			return
		case err != nil:
			server.logger.Error("update canonical entity", "error", err, "kind", definition.Kind, "request_id", requestID(request), "entity_id", entityID)
			server.recordCanonicalEntityWriteFailure(request, definition, definition.ActionPrefix+".update", "store_unavailable", &entityID)
			writeProblem(writer, request, http.StatusServiceUnavailable, definition.ProblemPrefix+"_store_unavailable", "暂时无法修改"+definition.DisplayName, "数据库写入失败，请稍后重试。")
			return
		}
		writer.Header().Set("ETag", canonicalEntityETag(updated.LockVersion))
		writeJSON(writer, http.StatusOK, responseCanonicalEntity(updated))
	}
}

func (server *Server) requireCanonicalEntityStore(writer http.ResponseWriter, request *http.Request, definition canonicalEntityHTTPDefinition) bool {
	if server.canonicalEntityStore != nil {
		return true
	}
	writeProblem(writer, request, http.StatusServiceUnavailable, definition.ProblemPrefix+"_store_unavailable", definition.DisplayName+"管理暂不可用", definition.DisplayName+"存储尚未配置。")
	return false
}

func (server *Server) recordCanonicalEntityWriteFailure(
	request *http.Request,
	definition canonicalEntityHTTPDefinition,
	action string,
	reason string,
	targetID *string,
) {
	actorID := principalFromContext(request.Context()).Session.User.ID
	server.recordAudit(request, audit.Entry{
		RequestID:   requestID(request),
		ActorUserID: &actorID,
		Action:      action,
		TargetType:  definition.TargetType,
		TargetID:    targetID,
		Outcome:     audit.OutcomeFailure,
		Details:     map[string]any{"reason": reason},
	})
}

func responseCanonicalEntity(entity catalog.CanonicalEntity) canonicalEntityResponse {
	return canonicalEntityResponse{
		ID:                  entity.ID,
		Name:                entity.Name,
		DisambiguationLabel: entity.DisambiguationLabel,
		Status:              entity.Status,
		MergedIntoID:        entity.MergedIntoID,
		LockVersion:         entity.LockVersion,
		CreatedAt:           entity.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z"),
		UpdatedAt:           entity.UpdatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z"),
	}
}

func parseCanonicalEntityETag(value string) (int64, error) {
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

func canonicalEntityETag(version int64) string {
	return fmt.Sprintf("\"%d\"", version)
}

func requiredNullableString(raw json.RawMessage) (*string, error) {
	if len(raw) == 0 {
		return nil, errors.New("required nullable string is missing")
	}
	if string(raw) == "null" {
		return nil, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, errors.New("required nullable string must be a string or null")
	}
	return &value, nil
}
