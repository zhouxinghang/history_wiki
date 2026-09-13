package httpapi

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/zhouxinghang/history_wiki/server/internal/audit"
	"github.com/zhouxinghang/history_wiki/server/internal/catalog"
	"github.com/zhouxinghang/history_wiki/server/internal/identifier"
)

type entityGovernanceDefinition struct {
	Kind                catalog.CanonicalEntityKind
	DisplayName         string
	ActionPrefix        string
	TargetType          string
	ProblemPrefix       string
	VersionConflictCode string
}

var (
	regionGovernanceDefinition = entityGovernanceDefinition{
		Kind: catalog.KindRegion, DisplayName: "地区", ActionPrefix: "region", TargetType: "region",
		ProblemPrefix: "region", VersionConflictCode: "region_version_conflict",
	}
	placeGovernanceDefinition = entityGovernanceDefinition{
		Kind: catalog.KindPlace, DisplayName: "地点", ActionPrefix: "place", TargetType: "place",
		ProblemPrefix: "place", VersionConflictCode: "place_version_conflict",
	}
	historicalPeriodGovernanceDefinition = entityGovernanceDefinition{
		Kind: catalog.KindHistoricalPeriod, DisplayName: "历史时期", ActionPrefix: "historical_period", TargetType: "historical_period",
		ProblemPrefix: "historical_period", VersionConflictCode: "historical_period_version_conflict",
	}
	historicalFigureGovernanceDefinition = entityGovernanceDefinition{
		Kind: catalog.KindHistoricalFigure, DisplayName: "历史人物", ActionPrefix: "historical_figure", TargetType: "historical_figure",
		ProblemPrefix: "historical_figure", VersionConflictCode: "historical_figure_version_conflict",
	}
	topicTagGovernanceDefinition = entityGovernanceDefinition{
		Kind: catalog.KindTopicTag, DisplayName: "主题标签", ActionPrefix: "topic_tag", TargetType: "topic_tag",
		ProblemPrefix: "topic_tag", VersionConflictCode: "topic_tag_version_conflict",
	}
)

type mergeCanonicalEntityRequest struct {
	TargetID        string                     `json:"targetId"`
	ConfirmedImpact *catalog.EntityMergeImpact `json:"confirmedImpact"`
}

type mergeCanonicalEntityResponse struct {
	Entity canonicalEntityResponse   `json:"entity"`
	Impact catalog.EntityMergeImpact `json:"impact"`
}

func (server *Server) canonicalEntityMergeImpact(definition entityGovernanceDefinition) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if !server.requireEntityGovernanceStore(writer, request, definition) {
			return
		}
		entityID := chi.URLParam(request, "entityID")
		if !identifier.Valid(entityID) {
			writeProblem(writer, request, http.StatusBadRequest, "invalid_"+definition.ProblemPrefix+"_id", definition.DisplayName+"标识无效", definition.DisplayName+"标识必须是 UUID。")
			return
		}
		impact, err := server.entityGovernanceStore.CanonicalEntityMergeImpact(request.Context(), definition.Kind, entityID)
		if errors.Is(err, catalog.ErrCanonicalEntityNotFound) {
			writeProblem(writer, request, http.StatusNotFound, definition.ProblemPrefix+"_not_found", definition.DisplayName+"不存在", "未找到指定"+definition.DisplayName+"。")
			return
		}
		if errors.Is(err, catalog.ErrCanonicalEntityNotWritable) {
			writeProblem(writer, request, http.StatusConflict, definition.ProblemPrefix+"_not_writable", definition.DisplayName+"不可合并", "该实体已经合并。")
			return
		}
		if err != nil {
			server.logger.Error("read canonical entity merge impact", "error", err, "kind", definition.Kind, "entity_id", entityID, "request_id", requestID(request))
			writeProblem(writer, request, http.StatusServiceUnavailable, definition.ProblemPrefix+"_store_unavailable", "暂时无法计算合并影响", "数据库查询失败，请稍后重试。")
			return
		}
		writeJSON(writer, http.StatusOK, impact)
	}
}

func (server *Server) mergeCanonicalEntity(definition entityGovernanceDefinition) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if !server.requireEntityGovernanceStore(writer, request, definition) {
			return
		}
		entityID := chi.URLParam(request, "entityID")
		if !identifier.Valid(entityID) {
			server.recordEntityGovernanceFailure(request, definition, "invalid_id", &entityID)
			writeProblem(writer, request, http.StatusBadRequest, "invalid_"+definition.ProblemPrefix+"_id", definition.DisplayName+"标识无效", definition.DisplayName+"标识必须是 UUID。")
			return
		}
		expectedVersion, err := parseCanonicalEntityETag(request.Header.Get("If-Match"))
		if errors.Is(err, errPreconditionRequired) {
			server.recordEntityGovernanceFailure(request, definition, "precondition_required", &entityID)
			writeProblem(writer, request, http.StatusPreconditionRequired, "precondition_required", "缺少并发版本", "合并"+definition.DisplayName+"必须通过 If-Match 提交当前 ETag。")
			return
		}
		if err != nil {
			server.recordEntityGovernanceFailure(request, definition, "invalid_if_match", &entityID)
			writeProblem(writer, request, http.StatusBadRequest, "invalid_if_match", "并发版本无效", "If-Match 必须是"+definition.DisplayName+"响应中的强 ETag。")
			return
		}
		var input mergeCanonicalEntityRequest
		if err := decodeJSONBody(writer, request, &input); err != nil {
			server.recordEntityGovernanceFailure(request, definition, "invalid_request", &entityID)
			writeJSONBodyProblem(writer, request, definition.DisplayName+"合并请求无效", err)
			return
		}
		if !identifier.Valid(input.TargetID) || input.ConfirmedImpact == nil || input.ConfirmedImpact.DraftCount < 0 || input.ConfirmedImpact.RevisionCount < 0 || input.ConfirmedImpact.PublishedEventCount < 0 {
			server.recordEntityGovernanceFailure(request, definition, "invalid_request", &entityID)
			writeProblem(writer, request, http.StatusBadRequest, "invalid_request", definition.DisplayName+"合并请求无效", "targetId 必须是 UUID，且必须提交已确认的影响统计。")
			return
		}
		current := principalFromContext(request.Context())
		merged, impact, err := server.entityGovernanceStore.MergeCanonicalEntity(request.Context(), definition.Kind, entityID, catalog.CanonicalEntityMerge{
			TargetID: input.TargetID, ExpectedVersion: expectedVersion, ConfirmedImpact: *input.ConfirmedImpact,
			UpdatedBy: current.Session.User.ID, UpdatedAt: server.config.Now().UTC(),
		}, server.auditEntry(request, audit.Entry{
			RequestID: requestID(request), Action: definition.ActionPrefix + ".merge", TargetType: definition.TargetType,
			Outcome: audit.OutcomeSuccess, Details: map[string]any{"targetId": input.TargetID, "confirmedImpact": input.ConfirmedImpact},
		}))
		switch {
		case errors.Is(err, catalog.ErrCanonicalEntityNotFound):
			server.recordEntityGovernanceFailure(request, definition, "not_found", &entityID)
			writeProblem(writer, request, http.StatusNotFound, definition.ProblemPrefix+"_not_found", definition.DisplayName+"不存在", "未找到待合并的"+definition.DisplayName+"。")
			return
		case errors.Is(err, catalog.ErrVersionConflict):
			server.recordEntityGovernanceFailure(request, definition, "version_conflict", &entityID)
			writeProblem(writer, request, http.StatusConflict, definition.VersionConflictCode, definition.DisplayName+"已被修改", "请刷新列表并重新确认合并影响。")
			return
		case errors.Is(err, catalog.ErrCanonicalEntityNotWritable):
			server.recordEntityGovernanceFailure(request, definition, "already_merged", &entityID)
			writeProblem(writer, request, http.StatusConflict, definition.ProblemPrefix+"_not_writable", definition.DisplayName+"不可合并", "该实体已经合并。")
			return
		case errors.Is(err, catalog.ErrInvalidMergeTarget):
			server.recordEntityGovernanceFailure(request, definition, "invalid_target", &entityID)
			writeProblem(writer, request, http.StatusUnprocessableEntity, "invalid_merge_target", "合并目标无效", "目标必须是同类型、当前有效且尚未合并的另一条规范实体。")
			return
		case errors.Is(err, catalog.ErrMergeImpactChanged):
			server.recordEntityGovernanceFailure(request, definition, "impact_changed", &entityID)
			writeProblem(writer, request, http.StatusConflict, "merge_impact_changed", "合并影响已变化", "请重新查看影响范围并再次确认。")
			return
		case err != nil:
			server.logger.Error("merge canonical entity", "error", err, "kind", definition.Kind, "entity_id", entityID, "request_id", requestID(request))
			server.recordEntityGovernanceFailure(request, definition, "store_unavailable", &entityID)
			writeProblem(writer, request, http.StatusServiceUnavailable, definition.ProblemPrefix+"_store_unavailable", "暂时无法合并"+definition.DisplayName, "数据库写入失败，请稍后重试。")
			return
		}
		writer.Header().Set("ETag", canonicalEntityETag(merged.LockVersion))
		writeJSON(writer, http.StatusOK, mergeCanonicalEntityResponse{Entity: responseCanonicalEntity(merged), Impact: impact})
	}
}

func (server *Server) requireEntityGovernanceStore(writer http.ResponseWriter, request *http.Request, definition entityGovernanceDefinition) bool {
	if server.entityGovernanceStore != nil {
		return true
	}
	writeProblem(writer, request, http.StatusServiceUnavailable, definition.ProblemPrefix+"_store_unavailable", definition.DisplayName+"治理暂不可用", "规范实体治理存储尚未配置。")
	return false
}

func (server *Server) recordEntityGovernanceFailure(request *http.Request, definition entityGovernanceDefinition, reason string, targetID *string) {
	actorID := principalFromContext(request.Context()).Session.User.ID
	server.recordAudit(request, audit.Entry{
		RequestID: requestID(request), ActorUserID: &actorID, Action: definition.ActionPrefix + ".merge",
		TargetType: definition.TargetType, TargetID: targetID, Outcome: audit.OutcomeFailure,
		Details: map[string]any{"reason": reason},
	})
}
