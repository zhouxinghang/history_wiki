package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/zhouxinghang/history_wiki/server/internal/audit"
	"github.com/zhouxinghang/history_wiki/server/internal/auth"
	"github.com/zhouxinghang/history_wiki/server/internal/identifier"
)

type managedUserResponse struct {
	ID          string    `json:"id"`
	Email       string    `json:"email"`
	Role        auth.Role `json:"role"`
	DisabledAt  *string   `json:"disabledAt"`
	LockVersion int64     `json:"lockVersion"`
	CreatedAt   string    `json:"createdAt"`
	UpdatedAt   string    `json:"updatedAt"`
}

type userListResponse struct {
	Users []managedUserResponse `json:"users"`
}

type createUserRequest struct {
	Email    string    `json:"email"`
	Password string    `json:"password"`
	Role     auth.Role `json:"role"`
}

type updateUserRequest struct {
	Role     auth.Role `json:"role"`
	Disabled *bool     `json:"disabled"`
}

type passwordRequest struct {
	Password string `json:"password"`
}

type changePasswordRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

func (server *Server) listUsers(writer http.ResponseWriter, request *http.Request) {
	users, err := server.authStore.ListUsers(request.Context())
	if err != nil {
		server.logger.Error("list users", "error", err, "request_id", requestID(request))
		writeProblem(writer, request, http.StatusServiceUnavailable, "account_store_unavailable", "暂时无法读取账号", "数据库查询失败，请稍后重试。")
		return
	}
	response := make([]managedUserResponse, 0, len(users))
	for _, user := range users {
		response = append(response, responseManagedUser(user))
	}
	writeJSON(writer, http.StatusOK, userListResponse{Users: response})
}

func (server *Server) createUser(writer http.ResponseWriter, request *http.Request) {
	var input createUserRequest
	if err := decodeJSONBody(writer, request, &input); err != nil {
		server.recordAccountFailure(request, "account.create", "invalid_request", nil)
		writeJSONBodyProblem(writer, request, "账号创建请求无效", err)
		return
	}
	normalizedEmail, emailErr := auth.NormalizeEmail(input.Email)
	if emailErr != nil || !input.Role.Valid() {
		server.recordAccountFailure(request, "account.create", "invalid_account", nil)
		writeProblem(writer, request, http.StatusUnprocessableEntity, "invalid_account", "账号信息无效", "邮箱格式无效，或角色不是 editor/administrator。")
		return
	}
	passwordHash, err := auth.HashPassword(input.Password, server.config.PasswordParams)
	if err != nil {
		server.recordAccountFailure(request, "account.create", "invalid_password", nil)
		writePasswordProblem(writer, request, err)
		return
	}
	userID, err := auth.NewID()
	if err != nil {
		server.logger.Error("generate user id", "error", err, "request_id", requestID(request))
		server.recordAccountFailure(request, "account.create", "identifier_unavailable", nil)
		writeProblem(writer, request, http.StatusServiceUnavailable, "account_store_unavailable", "暂时无法创建账号", "无法生成安全标识，请稍后重试。")
		return
	}
	now := server.config.Now().UTC()
	actorID := principalFromContext(request.Context()).Session.User.ID
	created, err := server.authStore.CreateUser(request.Context(), auth.User{
		ID:              userID,
		Email:           strings.TrimSpace(input.Email),
		NormalizedEmail: normalizedEmail,
		PasswordHash:    passwordHash,
		Role:            input.Role,
		CreatedAt:       now,
		UpdatedAt:       now,
	}, server.auditEntry(request, audit.Entry{
		RequestID:   requestID(request),
		ActorUserID: &actorID,
	}))
	if errors.Is(err, auth.ErrEmailAlreadyExists) {
		server.recordAccountFailure(request, "account.create", "email_already_exists", nil)
		writeProblem(writer, request, http.StatusConflict, "email_already_exists", "邮箱已被使用", "该邮箱已经关联到一个保留账号。")
		return
	}
	if err != nil {
		server.logger.Error("create user", "error", err, "request_id", requestID(request))
		server.recordAccountFailure(request, "account.create", "store_unavailable", nil)
		writeProblem(writer, request, http.StatusServiceUnavailable, "account_store_unavailable", "暂时无法创建账号", "数据库写入失败，请稍后重试。")
		return
	}
	writer.Header().Set("Location", "/api/v1/admin/users/"+created.ID)
	writer.Header().Set("ETag", canonicalEntityETag(created.LockVersion))
	writeJSON(writer, http.StatusCreated, responseManagedUser(created))
}

func (server *Server) updateUser(writer http.ResponseWriter, request *http.Request) {
	userID := chi.URLParam(request, "userID")
	if !identifier.Valid(userID) {
		server.recordAccountFailure(request, "account.update", "invalid_user_id", nil)
		writeProblem(writer, request, http.StatusBadRequest, "invalid_user_id", "账号标识无效", "账号标识必须是 UUID。")
		return
	}
	expectedVersion, err := parseCanonicalEntityETag(request.Header.Get("If-Match"))
	if errors.Is(err, errPreconditionRequired) {
		server.recordAccountFailure(request, "account.update", "precondition_required", &userID)
		writeProblem(writer, request, http.StatusPreconditionRequired, "precondition_required", "缺少并发版本", "修改账号必须通过 If-Match 提交当前 ETag。")
		return
	}
	if err != nil {
		server.recordAccountFailure(request, "account.update", "invalid_if_match", &userID)
		writeProblem(writer, request, http.StatusBadRequest, "invalid_if_match", "并发版本无效", "If-Match 必须是账号响应中的强 ETag。")
		return
	}
	var input updateUserRequest
	if err := decodeJSONBody(writer, request, &input); err != nil {
		server.recordAccountFailure(request, "account.update", "invalid_request", &userID)
		writeJSONBodyProblem(writer, request, "账号修改请求无效", err)
		return
	}
	if !input.Role.Valid() || input.Disabled == nil {
		server.recordAccountFailure(request, "account.update", "invalid_account", &userID)
		writeProblem(writer, request, http.StatusUnprocessableEntity, "invalid_account", "账号信息无效", "必须提交 editor/administrator 角色和停用状态。")
		return
	}
	current := principalFromContext(request.Context()).Session.User
	updated, err := server.authStore.UpdateUser(
		request.Context(),
		userID,
		input.Role,
		*input.Disabled,
		expectedVersion,
		server.config.Now().UTC(),
		server.auditEntry(request, audit.Entry{RequestID: requestID(request), ActorUserID: &current.ID}),
	)
	switch {
	case errors.Is(err, auth.ErrUserNotFound):
		server.recordAccountFailure(request, "account.update", "not_found", &userID)
		writeProblem(writer, request, http.StatusNotFound, "user_not_found", "账号不存在", "未找到指定账号。")
		return
	case errors.Is(err, auth.ErrLastAdministrator):
		server.recordAccountFailure(request, "account.update", "last_administrator", &userID)
		writeProblem(writer, request, http.StatusConflict, "last_administrator_required", "必须保留管理员", "不能停用或降级最后一个有效管理员。")
		return
	case errors.Is(err, auth.ErrUserVersionConflict):
		server.recordAccountFailure(request, "account.update", "version_conflict", &userID)
		writeProblem(writer, request, http.StatusConflict, "user_version_conflict", "账号已被修改", "请刷新账号列表并基于最新版本重试。")
		return
	case err != nil:
		server.logger.Error("update user", "error", err, "request_id", requestID(request), "user_id", userID)
		server.recordAccountFailure(request, "account.update", "store_unavailable", &userID)
		writeProblem(writer, request, http.StatusServiceUnavailable, "account_store_unavailable", "暂时无法修改账号", "数据库写入失败，请稍后重试。")
		return
	}
	if userID == current.ID && (input.Role != current.Role || *input.Disabled) {
		server.clearSessionCookies(writer)
	}
	writer.Header().Set("ETag", canonicalEntityETag(updated.LockVersion))
	writeJSON(writer, http.StatusOK, responseManagedUser(updated))
}

func (server *Server) changePassword(writer http.ResponseWriter, request *http.Request) {
	var input changePasswordRequest
	if err := decodeJSONBody(writer, request, &input); err != nil {
		server.recordAccountFailure(request, "account.password_change", "invalid_request", nil)
		writeJSONBodyProblem(writer, request, "密码修改请求无效", err)
		return
	}
	current := principalFromContext(request.Context()).Session.User
	user, err := server.authStore.UserByID(request.Context(), current.ID)
	if err != nil {
		server.logger.Error("load user for password change", "error", err, "request_id", requestID(request), "user_id", current.ID)
		server.recordAccountFailure(request, "account.password_change", "account_read_failed", &current.ID)
		writeProblem(writer, request, http.StatusServiceUnavailable, "account_store_unavailable", "暂时无法修改密码", "账号信息读取失败，请稍后重试。")
		return
	}
	matched, err := auth.VerifyPassword(user.PasswordHash, input.CurrentPassword)
	if err != nil {
		server.logger.Error("verify current password", "error", err, "request_id", requestID(request), "user_id", current.ID)
		server.recordAccountFailure(request, "account.password_change", "password_verification_failed", &current.ID)
		writeProblem(writer, request, http.StatusServiceUnavailable, "authentication_unavailable", "暂时无法修改密码", "密码暂时无法校验。")
		return
	}
	if !matched {
		server.recordAccountFailure(request, "account.password_change", "invalid_current_password", &current.ID)
		writeProblem(writer, request, http.StatusUnauthorized, "invalid_current_password", "当前密码不正确", "请重新输入当前密码。")
		return
	}
	passwordHash, err := auth.HashPassword(input.NewPassword, server.config.PasswordParams)
	if err != nil {
		server.recordAccountFailure(request, "account.password_change", "invalid_password", &current.ID)
		writePasswordProblem(writer, request, err)
		return
	}
	if err := server.authStore.ChangePassword(request.Context(), current.ID, passwordHash, server.config.Now().UTC(), server.auditEntry(request, audit.Entry{
		RequestID: requestID(request), ActorUserID: &current.ID,
	})); err != nil {
		server.logger.Error("change password", "error", err, "request_id", requestID(request), "user_id", current.ID)
		server.recordAccountFailure(request, "account.password_change", "store_unavailable", &current.ID)
		writeProblem(writer, request, http.StatusServiceUnavailable, "account_store_unavailable", "暂时无法修改密码", "数据库写入失败，请稍后重试。")
		return
	}
	server.clearSessionCookies(writer)
	writer.WriteHeader(http.StatusNoContent)
}

func (server *Server) resetPassword(writer http.ResponseWriter, request *http.Request) {
	userID := chi.URLParam(request, "userID")
	if !identifier.Valid(userID) {
		server.recordAccountFailure(request, "account.password_reset", "invalid_user_id", nil)
		writeProblem(writer, request, http.StatusBadRequest, "invalid_user_id", "账号标识无效", "账号标识必须是 UUID。")
		return
	}
	var input passwordRequest
	if err := decodeJSONBody(writer, request, &input); err != nil {
		server.recordAccountFailure(request, "account.password_reset", "invalid_request", &userID)
		writeJSONBodyProblem(writer, request, "密码重置请求无效", err)
		return
	}
	passwordHash, err := auth.HashPassword(input.Password, server.config.PasswordParams)
	if err != nil {
		server.recordAccountFailure(request, "account.password_reset", "invalid_password", &userID)
		writePasswordProblem(writer, request, err)
		return
	}
	actorID := principalFromContext(request.Context()).Session.User.ID
	err = server.authStore.ResetPassword(request.Context(), userID, passwordHash, server.config.Now().UTC(), server.auditEntry(request, audit.Entry{
		RequestID: requestID(request), ActorUserID: &actorID,
	}))
	if errors.Is(err, auth.ErrUserNotFound) {
		server.recordAccountFailure(request, "account.password_reset", "not_found", &userID)
		writeProblem(writer, request, http.StatusNotFound, "user_not_found", "账号不存在", "未找到指定账号。")
		return
	}
	if err != nil {
		server.logger.Error("reset password", "error", err, "request_id", requestID(request), "user_id", userID)
		server.recordAccountFailure(request, "account.password_reset", "store_unavailable", &userID)
		writeProblem(writer, request, http.StatusServiceUnavailable, "account_store_unavailable", "暂时无法重置密码", "数据库写入失败，请稍后重试。")
		return
	}
	if userID == actorID {
		server.clearSessionCookies(writer)
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (server *Server) recordAccountFailure(request *http.Request, action, reason string, targetID *string) {
	entry := audit.Entry{
		RequestID:  requestID(request),
		Action:     action,
		TargetType: "user",
		TargetID:   targetID,
		Outcome:    audit.OutcomeFailure,
		Details:    map[string]any{"reason": reason},
	}
	if current := principalFromContext(request.Context()).Session.User.ID; current != "" {
		entry.ActorUserID = &current
	}
	server.recordAudit(request, entry)
}

func writePasswordProblem(writer http.ResponseWriter, request *http.Request, err error) {
	if errors.Is(err, auth.ErrPasswordTooShort) || errors.Is(err, auth.ErrPasswordTooLong) {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "invalid_password", "密码不符合要求", "密码至少需要 12 个字符，且不能超过 1024 字节。")
		return
	}
	writeProblem(writer, request, http.StatusServiceUnavailable, "authentication_unavailable", "暂时无法处理密码", "密码无法安全处理，请稍后重试。")
}

func responseManagedUser(user auth.User) managedUserResponse {
	var disabledAt *string
	if user.DisabledAt != nil {
		value := user.DisabledAt.UTC().Format("2006-01-02T15:04:05.999999999Z")
		disabledAt = &value
	}
	return managedUserResponse{
		ID:          user.ID,
		Email:       user.Email,
		Role:        user.Role,
		DisabledAt:  disabledAt,
		LockVersion: user.LockVersion,
		CreatedAt:   user.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z"),
		UpdatedAt:   user.UpdatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z"),
	}
}
