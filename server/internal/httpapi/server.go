package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/zhouxinghang/history_wiki/server/internal/audit"
	"github.com/zhouxinghang/history_wiki/server/internal/auth"
	"github.com/zhouxinghang/history_wiki/server/internal/catalog"
	historyevent "github.com/zhouxinghang/history_wiki/server/internal/event"
)

const maxJSONBodyBytes = 2 << 20

type CatalogStore interface {
	Ping(ctx context.Context) error
	MigrationVersion(ctx context.Context) (version int64, applied bool, err error)
	PublishedEventCount(ctx context.Context) (int64, error)
}

type AuthenticationStore interface {
	UserByNormalizedEmail(ctx context.Context, normalizedEmail string) (auth.User, error)
	UserByID(ctx context.Context, userID string) (auth.User, error)
	ListUsers(ctx context.Context) ([]auth.User, error)
	CreateUser(ctx context.Context, user auth.User, entry audit.Entry) (auth.User, error)
	UpdateUser(ctx context.Context, userID string, role auth.Role, disabled bool, expectedVersion int64, updatedAt time.Time, entry audit.Entry) (auth.User, error)
	ChangePassword(ctx context.Context, userID, passwordHash string, updatedAt time.Time, entry audit.Entry) error
	ResetPassword(ctx context.Context, userID, passwordHash string, updatedAt time.Time, entry audit.Entry) error
	CreateSession(ctx context.Context, session auth.Session, entry audit.Entry) error
	SessionByTokenDigest(ctx context.Context, digest []byte) (auth.Session, error)
	TouchSession(ctx context.Context, sessionID string, lastSeenAt, idleExpiresAt time.Time) error
	RevokeSession(ctx context.Context, sessionID string, revokedAt time.Time, entry audit.Entry) error
	AppendAudit(ctx context.Context, entry audit.Entry) error
}

type RegionStore interface {
	ListRegions(ctx context.Context, filter catalog.RegionListFilter) ([]catalog.Region, error)
	CreateRegion(ctx context.Context, region catalog.Region, idempotencyKey string, requestHash []byte, entry audit.Entry) (catalog.Region, bool, error)
	UpdateRegion(ctx context.Context, regionID string, update catalog.RegionUpdate, entry audit.Entry) (catalog.Region, error)
}

type CanonicalEntityStore interface {
	ListCanonicalEntities(ctx context.Context, kind catalog.CanonicalEntityKind, filter catalog.CanonicalEntityListFilter) ([]catalog.CanonicalEntity, error)
	CreateCanonicalEntity(ctx context.Context, kind catalog.CanonicalEntityKind, entity catalog.CanonicalEntity, idempotencyKey string, requestHash []byte, entry audit.Entry) (catalog.CanonicalEntity, bool, error)
	UpdateCanonicalEntity(ctx context.Context, kind catalog.CanonicalEntityKind, entityID string, update catalog.CanonicalEntityUpdate, entry audit.Entry) (catalog.CanonicalEntity, error)
}

type EntityGovernanceStore interface {
	CanonicalEntityMergeImpact(ctx context.Context, kind catalog.CanonicalEntityKind, entityID string) (catalog.EntityMergeImpact, error)
	MergeCanonicalEntity(ctx context.Context, kind catalog.CanonicalEntityKind, entityID string, command catalog.CanonicalEntityMerge, entry audit.Entry) (catalog.CanonicalEntity, catalog.EntityMergeImpact, error)
}

type ContextEntityStore interface {
	ListPlaces(ctx context.Context, filter catalog.ContextEntityListFilter) ([]catalog.Place, error)
	CreatePlace(ctx context.Context, place catalog.Place, idempotencyKey string, requestHash []byte, entry audit.Entry) (catalog.Place, bool, error)
	UpdatePlace(ctx context.Context, placeID string, update catalog.PlaceUpdate, entry audit.Entry) (catalog.Place, error)
	ListHistoricalPeriods(ctx context.Context, filter catalog.ContextEntityListFilter) ([]catalog.HistoricalPeriod, error)
	CreateHistoricalPeriod(ctx context.Context, period catalog.HistoricalPeriod, idempotencyKey string, requestHash []byte, entry audit.Entry) (catalog.HistoricalPeriod, bool, error)
	UpdateHistoricalPeriod(ctx context.Context, periodID string, update catalog.HistoricalPeriodUpdate, entry audit.Entry) (catalog.HistoricalPeriod, error)
}

type EventStore interface {
	ListManagedEvents(ctx context.Context) ([]historyevent.Event, error)
	ManagedEventByID(ctx context.Context, eventID string) (historyevent.Event, error)
	CreateManagedEvent(ctx context.Context, managedEvent historyevent.Event, idempotencyKey string, requestHash []byte, entry audit.Entry) (historyevent.Event, bool, error)
	UpdateManagedEventSlug(ctx context.Context, eventID string, update historyevent.SlugUpdate, entry audit.Entry) (historyevent.Event, error)
	UpdateEventDraft(ctx context.Context, eventID string, update historyevent.DraftUpdate, entry audit.Entry) (historyevent.Event, error)
	CreateDraftFromCurrentRevision(ctx context.Context, eventID string, command historyevent.DraftFromRevisionCommand, entry audit.Entry) (historyevent.Event, error)
	PublishEvent(ctx context.Context, eventID string, command historyevent.PublishCommand, entry audit.Entry) (historyevent.PublishedEvent, error)
	ArchiveEvent(ctx context.Context, eventID string, command historyevent.ArchiveCommand, entry audit.Entry) (historyevent.Event, error)
	ListEventRevisions(ctx context.Context, eventID string) ([]historyevent.Revision, error)
	EventRevisionByNumber(ctx context.Context, eventID string, revisionNo int) (historyevent.Revision, error)
	RestoreEventRevision(ctx context.Context, eventID string, revisionNo int, command historyevent.DraftFromRevisionCommand, entry audit.Entry) (historyevent.Event, error)
	ValidateEventImport(ctx context.Context, records []historyevent.ImportRecord) ([]historyevent.ImportIssue, error)
	ImportEvents(ctx context.Context, batch historyevent.ImportBatch, records []historyevent.ImportRecord, actorUserID, idempotencyKey string, requestHash []byte, entry audit.Entry) (historyevent.ImportBatch, bool, error)
}

type PublicEventStore interface {
	PublishedEventBounds(ctx context.Context) (*historyevent.PublishedEventBounds, error)
	PublishedEventMetadata(ctx context.Context) (historyevent.PublishedEventMetadata, error)
	QueryPublishedEvents(ctx context.Context, query historyevent.PublishedEventQuery) (historyevent.PublishedEventQueryResult, error)
	PublishedEventByID(ctx context.Context, eventID string) (historyevent.PublishedEvent, error)
}

type Config struct {
	PublicBaseURL      string
	SessionCookieName  string
	CSRFCookieName     string
	StaticDirectory    string
	TrustedProxyCount  int
	SessionIdleTimeout time.Duration
	SessionMaxLifetime time.Duration
	Now                func() time.Time
	DummyPasswordHash  string
	PasswordParams     auth.Argon2idParams
	AllowInsecureHTTP  bool
}

func DefaultConfig() Config {
	return Config{
		SessionCookieName:  "__Host-history_wiki_session",
		CSRFCookieName:     "__Host-history_wiki_csrf",
		SessionIdleTimeout: 8 * time.Hour,
		SessionMaxLifetime: 7 * 24 * time.Hour,
		Now:                time.Now,
		PasswordParams:     auth.DefaultArgon2idParams(),
	}
}

type Server struct {
	store                    CatalogStore
	authStore                AuthenticationStore
	regionStore              RegionStore
	canonicalEntityStore     CanonicalEntityStore
	contextEntityStore       ContextEntityStore
	entityGovernanceStore    EntityGovernanceStore
	eventStore               EventStore
	publicEventStore         PublicEventStore
	expectedMigrationVersion int64
	logger                   *slog.Logger
	config                   Config
	dummyPasswordHash        string
	metrics                  *metricsRegistry
	loginFailureLimiter      *tokenBucketLimiter
	publicQueryLimiter       *tokenBucketLimiter
	managementWriteLimiter   *tokenBucketLimiter
	formalImportLimiter      *tokenBucketLimiter
}

type contextKey string

const (
	requestActorKey contextKey = "request-actor"
	principalKey    contextKey = "principal"
)

type requestActor struct {
	UserID string
}

type principal struct {
	Session auth.Session
}

func New(store CatalogStore, expectedMigrationVersion int64, logger *slog.Logger, options ...Config) http.Handler {
	configuration := DefaultConfig()
	if len(options) > 0 {
		configuration = withConfigDefaults(options[0])
	}
	authStore, _ := store.(AuthenticationStore)
	regionStore, _ := store.(RegionStore)
	canonicalEntityStore, _ := store.(CanonicalEntityStore)
	contextEntityStore, _ := store.(ContextEntityStore)
	entityGovernanceStore, _ := store.(EntityGovernanceStore)
	eventStore, _ := store.(EventStore)
	publicEventStore, _ := store.(PublicEventStore)
	dummyPasswordHash := configuration.DummyPasswordHash
	if authStore != nil && dummyPasswordHash == "" {
		var err error
		dummyPasswordHash, err = auth.HashPassword("invalid-password-placeholder", configuration.PasswordParams)
		if err != nil {
			panic(fmt.Errorf("create login timing hash: %w", err))
		}
	}
	server := &Server{
		store:                    store,
		authStore:                authStore,
		regionStore:              regionStore,
		canonicalEntityStore:     canonicalEntityStore,
		contextEntityStore:       contextEntityStore,
		entityGovernanceStore:    entityGovernanceStore,
		eventStore:               eventStore,
		publicEventStore:         publicEventStore,
		expectedMigrationVersion: expectedMigrationVersion,
		logger:                   logger,
		config:                   configuration,
		dummyPasswordHash:        dummyPasswordHash,
		metrics:                  newMetricsRegistry(),
		loginFailureLimiter:      newTokenBucketLimiter(10, 15*time.Minute),
		publicQueryLimiter:       newTokenBucketLimiter(120, time.Minute),
		managementWriteLimiter:   newTokenBucketLimiter(60, time.Minute),
		formalImportLimiter:      newTokenBucketLimiter(5, time.Hour),
	}

	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(server.exposeRequestID)
	router.Use(server.withRequestActor)
	router.Use(server.logRequest)
	router.Use(middleware.Recoverer)

	router.Get("/health/live", server.live)
	router.Get("/health/ready", server.ready)
	router.Get("/metrics", server.metricsHandler)
	router.Route("/api/v1", func(router chi.Router) {
		router.With(server.limitPublicQueries).Get("/event-bounds", server.eventBounds)
		router.With(server.limitPublicQueries).Get("/event-metadata", server.eventMetadata)
		router.With(server.limitPublicQueries).Get("/events", server.events)
		router.With(server.limitPublicQueries).Get("/events/{eventID}", server.eventByID)

		router.Route("/auth", func(router chi.Router) {
			router.With(server.requireSameOrigin).Post("/login", server.login)
			router.With(server.authenticate).Get("/me", server.me)
			router.With(server.authenticate, server.protectUnsafeRequest, server.limitManagementWrites).Post("/logout", server.logout)
			router.With(server.authenticate, server.protectUnsafeRequest, server.limitManagementWrites).Post("/change-password", server.changePassword)
		})

		router.Group(func(admin chi.Router) {
			admin.Use(server.authenticate)
			admin.Use(server.requireRole(auth.RoleEditor, auth.RoleAdministrator))
			admin.Use(server.protectUnsafeRequest)
			admin.Use(server.limitManagementWrites)
			admin.Get("/admin", server.adminBoundary)
			admin.Get("/admin/events", server.listManagedEvents)
			admin.Post("/admin/events", server.createManagedEvent)
			admin.Get("/admin/events/{eventID}", server.getManagedEvent)
			admin.Patch("/admin/events/{eventID}", server.updateManagedEventSlug)
			admin.Patch("/admin/events/{eventID}/draft", server.updateManagedEventDraft)
			admin.Post("/admin/events/{eventID}/draft", server.createManagedEventDraft)
			admin.With(server.requireRole(auth.RoleAdministrator)).Post("/admin/events/{eventID}/publish", server.publishManagedEvent)
			admin.With(server.requireRole(auth.RoleAdministrator)).Post("/admin/events/{eventID}/archive", server.archiveManagedEvent)
			admin.Get("/admin/events/{eventID}/revisions", server.listManagedEventRevisions)
			admin.Get("/admin/events/{eventID}/revisions/{revisionNo}", server.getManagedEventRevision)
			admin.Post("/admin/events/{eventID}/revisions/{revisionNo}/restore", server.restoreManagedEventRevision)
			admin.Post("/admin/event-imports/preflight", server.preflightEventImport)
			admin.With(server.requireRole(auth.RoleAdministrator), server.limitFormalImports).Post("/admin/event-imports", server.importEvents)
			admin.With(server.requireRole(auth.RoleAdministrator)).Get("/admin/users", server.listUsers)
			admin.With(server.requireRole(auth.RoleAdministrator)).Post("/admin/users", server.createUser)
			admin.With(server.requireRole(auth.RoleAdministrator)).Patch("/admin/users/{userID}", server.updateUser)
			admin.With(server.requireRole(auth.RoleAdministrator)).Post("/admin/users/{userID}/reset-password", server.resetPassword)
			admin.Get("/admin/regions", server.listRegions)
			admin.Post("/admin/regions", server.createRegion)
			admin.With(server.requireRole(auth.RoleAdministrator)).Patch("/admin/regions/{regionID}", server.updateRegion)
			admin.With(server.requireRole(auth.RoleAdministrator)).Get("/admin/regions/{entityID}/merge-impact", server.canonicalEntityMergeImpact(regionGovernanceDefinition))
			admin.With(server.requireRole(auth.RoleAdministrator)).Post("/admin/regions/{entityID}/merge", server.mergeCanonicalEntity(regionGovernanceDefinition))
			admin.Get("/admin/figures", server.listCanonicalEntities(historicalFigureHTTPDefinition))
			admin.Post("/admin/figures", server.createCanonicalEntity(historicalFigureHTTPDefinition))
			admin.With(server.requireRole(auth.RoleAdministrator)).Patch("/admin/figures/{entityID}", server.updateCanonicalEntity(historicalFigureHTTPDefinition))
			admin.With(server.requireRole(auth.RoleAdministrator)).Get("/admin/figures/{entityID}/merge-impact", server.canonicalEntityMergeImpact(historicalFigureGovernanceDefinition))
			admin.With(server.requireRole(auth.RoleAdministrator)).Post("/admin/figures/{entityID}/merge", server.mergeCanonicalEntity(historicalFigureGovernanceDefinition))
			admin.Get("/admin/topic-tags", server.listCanonicalEntities(topicTagHTTPDefinition))
			admin.Post("/admin/topic-tags", server.createCanonicalEntity(topicTagHTTPDefinition))
			admin.With(server.requireRole(auth.RoleAdministrator)).Patch("/admin/topic-tags/{entityID}", server.updateCanonicalEntity(topicTagHTTPDefinition))
			admin.With(server.requireRole(auth.RoleAdministrator)).Get("/admin/topic-tags/{entityID}/merge-impact", server.canonicalEntityMergeImpact(topicTagGovernanceDefinition))
			admin.With(server.requireRole(auth.RoleAdministrator)).Post("/admin/topic-tags/{entityID}/merge", server.mergeCanonicalEntity(topicTagGovernanceDefinition))
			admin.Get("/admin/places", server.listPlaces)
			admin.Post("/admin/places", server.createPlace)
			admin.With(server.requireRole(auth.RoleAdministrator)).Patch("/admin/places/{placeID}", server.updatePlace)
			admin.With(server.requireRole(auth.RoleAdministrator)).Get("/admin/places/{entityID}/merge-impact", server.canonicalEntityMergeImpact(placeGovernanceDefinition))
			admin.With(server.requireRole(auth.RoleAdministrator)).Post("/admin/places/{entityID}/merge", server.mergeCanonicalEntity(placeGovernanceDefinition))
			admin.Get("/admin/periods", server.listHistoricalPeriods)
			admin.Post("/admin/periods", server.createHistoricalPeriod)
			admin.With(server.requireRole(auth.RoleAdministrator)).Patch("/admin/periods/{periodID}", server.updateHistoricalPeriod)
			admin.With(server.requireRole(auth.RoleAdministrator)).Get("/admin/periods/{entityID}/merge-impact", server.canonicalEntityMergeImpact(historicalPeriodGovernanceDefinition))
			admin.With(server.requireRole(auth.RoleAdministrator)).Post("/admin/periods/{entityID}/merge", server.mergeCanonicalEntity(historicalPeriodGovernanceDefinition))
		})
	})
	if configuration.StaticDirectory != "" {
		router.Handle("/*", spaHandler(configuration.StaticDirectory))
	}

	return router
}

func withConfigDefaults(configuration Config) Config {
	defaults := DefaultConfig()
	if configuration.SessionCookieName == "" {
		configuration.SessionCookieName = defaults.SessionCookieName
	}
	if configuration.CSRFCookieName == "" {
		configuration.CSRFCookieName = defaults.CSRFCookieName
	}
	if configuration.SessionIdleTimeout == 0 {
		configuration.SessionIdleTimeout = defaults.SessionIdleTimeout
	}
	if configuration.SessionMaxLifetime == 0 {
		configuration.SessionMaxLifetime = defaults.SessionMaxLifetime
	}
	if configuration.Now == nil {
		configuration.Now = defaults.Now
	}
	if configuration.PasswordParams.Memory == 0 {
		configuration.PasswordParams = defaults.PasswordParams
	}
	return configuration
}

func (server *Server) live(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]string{"status": "ok"})
}

func (server *Server) ready(writer http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
	defer cancel()

	if err := server.store.Ping(ctx); err != nil {
		writeProblem(writer, request, http.StatusServiceUnavailable, "service_not_ready", "服务尚未就绪", "数据库连接不可用。")
		return
	}
	version, applied, err := server.store.MigrationVersion(ctx)
	if err != nil || !applied || version != server.expectedMigrationVersion {
		writeProblem(writer, request, http.StatusServiceUnavailable, "migration_not_ready", "服务尚未就绪", "数据库迁移版本与服务不一致。")
		return
	}

	writeJSON(writer, http.StatusOK, map[string]any{
		"status":           "ready",
		"migrationVersion": version,
	})
}

func (server *Server) eventBounds(writer http.ResponseWriter, request *http.Request) {
	server.readPublishedEventBounds(writer, request)
}

func (server *Server) eventMetadata(writer http.ResponseWriter, request *http.Request) {
	server.readPublishedEventMetadata(writer, request)
}

func (server *Server) events(writer http.ResponseWriter, request *http.Request) {
	from, err := requiredFiniteCoordinate(request, "from")
	if err != nil {
		writeInvalidQuery(writer, request, err)
		return
	}
	to, err := requiredFiniteCoordinate(request, "to")
	if err != nil {
		writeInvalidQuery(writer, request, err)
		return
	}
	if from >= to {
		writeInvalidQuery(writer, request, errors.New("from 必须小于 to"))
		return
	}
	if query := strings.TrimSpace(request.URL.Query().Get("q")); len([]rune(query)) > 100 {
		writeInvalidQuery(writer, request, errors.New("q 最长为 100 个字符"))
		return
	}
	padding := 0.0
	if raw := request.URL.Query().Get("pad"); raw != "" {
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil || math.IsInf(value, 0) || math.IsNaN(value) || value < 0 {
			writeInvalidQuery(writer, request, errors.New("pad 必须是非负有限数值"))
			return
		}
		padding = value
	}
	if math.IsInf(from-padding, 0) || math.IsInf(to+padding, 0) {
		writeInvalidQuery(writer, request, errors.New("pad 过大，事件窗口超出可表示范围"))
		return
	}
	server.readPublishedEvents(writer, request, from, to, padding)
}

func (server *Server) eventByID(writer http.ResponseWriter, request *http.Request) {
	server.readPublishedEventByID(writer, request)
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type currentUserResponse struct {
	User currentUser `json:"user"`
}

type currentUser struct {
	ID    string    `json:"id"`
	Email string    `json:"email"`
	Role  auth.Role `json:"role"`
}

func (server *Server) login(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	if server.authStore == nil {
		writeProblem(writer, request, http.StatusServiceUnavailable, "authentication_unavailable", "认证服务暂不可用", "认证存储尚未配置。")
		return
	}
	loginKey := server.clientIP(request)
	if allowed, retryAfter := server.loginFailureLimiter.take(loginKey, server.config.Now()); !allowed {
		writeRateLimitProblem(writer, request, retryAfter, "login_rate_limited", "登录尝试过于频繁")
		return
	}
	failedCredentials := false
	defer func() {
		if !failedCredentials {
			server.loginFailureLimiter.refund(loginKey, server.config.Now())
		}
	}()
	var input loginRequest
	if err := decodeJSONBody(writer, request, &input); err != nil {
		writeJSONBodyProblem(writer, request, "登录请求无效", err)
		return
	}

	normalizedEmail, emailErr := auth.NormalizeEmail(input.Email)
	user, findErr := server.authStore.UserByNormalizedEmail(request.Context(), normalizedEmail)
	passwordHash := server.dummyPasswordHash
	if findErr == nil {
		passwordHash = user.PasswordHash
	}
	passwordCandidate := input.Password
	passwordTooLong := len(passwordCandidate) > 1024
	if passwordTooLong {
		passwordCandidate = strings.Repeat("x", 1024)
	}
	matched, verifyErr := auth.VerifyPassword(passwordHash, passwordCandidate)
	if verifyErr != nil {
		server.logger.Error("verify password", "error", verifyErr, "request_id", requestID(request))
		writeProblem(writer, request, http.StatusServiceUnavailable, "authentication_unavailable", "认证服务暂不可用", "认证信息暂时无法校验。")
		return
	}
	if findErr != nil && !errors.Is(findErr, auth.ErrUserNotFound) {
		server.logger.Error("find login user", "error", findErr, "request_id", requestID(request))
		writeProblem(writer, request, http.StatusServiceUnavailable, "authentication_unavailable", "认证服务暂不可用", "认证信息暂时无法校验。")
		return
	}
	if emailErr != nil || findErr != nil || passwordTooLong || !matched || user.DisabledAt != nil {
		failedCredentials = true
		server.recordAudit(request, audit.Entry{
			RequestID:  requestID(request),
			Action:     "authentication.login",
			TargetType: "session",
			Outcome:    audit.OutcomeFailure,
			Details: map[string]any{
				"attemptedEmail": strings.ToLower(strings.TrimSpace(input.Email)),
				"reason":         "invalid_credentials",
			},
		})
		writeProblem(writer, request, http.StatusUnauthorized, "invalid_credentials", "无法登录", "邮箱或密码不正确。")
		return
	}

	now := server.config.Now().UTC()
	sessionID, token, tokenDigest, csrfToken, csrfDigest, err := newSessionSecrets()
	if err != nil {
		server.logger.Error("generate session", "error", err, "request_id", requestID(request))
		writeProblem(writer, request, http.StatusServiceUnavailable, "authentication_unavailable", "认证服务暂不可用", "无法建立安全会话。")
		return
	}
	session := auth.Session{
		ID:              sessionID,
		TokenDigest:     tokenDigest,
		CSRFTokenDigest: csrfDigest,
		User:            user,
		CreatedAt:       now,
		LastSeenAt:      now,
		IdleExpiresAt:   now.Add(server.config.SessionIdleTimeout),
		AbsoluteExpires: now.Add(server.config.SessionMaxLifetime),
	}
	actorID := user.ID
	if err := server.authStore.CreateSession(request.Context(), session, server.auditEntry(request, audit.Entry{
		RequestID:   requestID(request),
		ActorUserID: &actorID,
		Action:      "authentication.login",
		TargetType:  "session",
		TargetID:    &sessionID,
		Outcome:     audit.OutcomeSuccess,
	})); err != nil {
		server.logger.Error("create session", "error", err, "request_id", requestID(request))
		writeProblem(writer, request, http.StatusServiceUnavailable, "authentication_unavailable", "认证服务暂不可用", "无法建立安全会话。")
		return
	}
	setActor(request.Context(), user.ID)
	server.setSessionCookies(writer, token, csrfToken, session.AbsoluteExpires)
	writeJSON(writer, http.StatusOK, currentUserResponse{User: responseUser(user)})
}

func (server *Server) me(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	current := principalFromContext(request.Context())
	writeJSON(writer, http.StatusOK, currentUserResponse{User: responseUser(current.Session.User)})
}

func (server *Server) logout(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	current := principalFromContext(request.Context())
	actorID := current.Session.User.ID
	sessionID := current.Session.ID
	if err := server.authStore.RevokeSession(request.Context(), sessionID, server.config.Now().UTC(), server.auditEntry(request, audit.Entry{
		RequestID:   requestID(request),
		ActorUserID: &actorID,
		Action:      "authentication.logout",
		TargetType:  "session",
		TargetID:    &sessionID,
		Outcome:     audit.OutcomeSuccess,
	})); err != nil {
		server.logger.Error("revoke session", "error", err, "request_id", requestID(request), "user_id", actorID)
		writeProblem(writer, request, http.StatusServiceUnavailable, "authentication_unavailable", "暂时无法退出", "会话撤销失败，请稍后重试。")
		return
	}
	server.clearSessionCookies(writer)
	writer.WriteHeader(http.StatusNoContent)
}

func (server *Server) adminBoundary(writer http.ResponseWriter, request *http.Request) {
	current := principalFromContext(request.Context())
	writeJSON(writer, http.StatusOK, map[string]any{
		"status": "authenticated",
		"user":   responseUser(current.Session.User),
	})
}

func (server *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "no-store")
		if server.authStore == nil {
			writeProblem(writer, request, http.StatusServiceUnavailable, "authentication_unavailable", "认证服务暂不可用", "认证存储尚未配置。")
			return
		}
		cookie, err := request.Cookie(server.config.SessionCookieName)
		if err != nil || cookie.Value == "" {
			writeProblem(writer, request, http.StatusUnauthorized, "authentication_required", "需要登录", "请先登录管理区。")
			return
		}
		digest := auth.TokenDigest(cookie.Value)
		session, err := server.authStore.SessionByTokenDigest(request.Context(), digest[:])
		if errors.Is(err, auth.ErrSessionNotFound) {
			server.clearSessionCookies(writer)
			writeProblem(writer, request, http.StatusUnauthorized, "authentication_required", "需要登录", "登录会话无效。")
			return
		}
		if err != nil {
			server.logger.Error("load session", "error", err, "request_id", requestID(request))
			writeProblem(writer, request, http.StatusServiceUnavailable, "authentication_unavailable", "认证服务暂不可用", "登录会话暂时无法校验。")
			return
		}

		now := server.config.Now().UTC()
		if !session.ActiveAt(now) {
			server.clearSessionCookies(writer)
			if session.RevokedAt == nil && session.User.DisabledAt == nil {
				actorID := session.User.ID
				sessionID := session.ID
				if revokeErr := server.authStore.RevokeSession(request.Context(), sessionID, now, server.auditEntry(request, audit.Entry{
					RequestID:   requestID(request),
					ActorUserID: &actorID,
					Action:      "authentication.session_expired",
					TargetType:  "session",
					TargetID:    &sessionID,
					Outcome:     audit.OutcomeFailure,
				})); revokeErr != nil {
					server.logger.Error("record expired session", "error", revokeErr, "request_id", requestID(request), "user_id", actorID)
				}
			}
			writeProblem(writer, request, http.StatusUnauthorized, "session_expired", "登录已失效", "请重新登录管理区。")
			return
		}
		// Preflight is specified as a read-only operation, including avoiding the
		// normal sliding-session timestamp write.
		if request.URL.Path != "/api/v1/admin/event-imports/preflight" {
			if err := server.authStore.TouchSession(request.Context(), session.ID, now, now.Add(server.config.SessionIdleTimeout)); err != nil {
				server.logger.Error("refresh session", "error", err, "request_id", requestID(request), "user_id", session.User.ID)
				writeProblem(writer, request, http.StatusServiceUnavailable, "authentication_unavailable", "认证服务暂不可用", "登录会话暂时无法刷新。")
				return
			}
		}
		setActor(request.Context(), session.User.ID)
		ctx := context.WithValue(request.Context(), principalKey, principal{Session: session})
		next.ServeHTTP(writer, request.WithContext(ctx))
	})
}

func (server *Server) protectUnsafeRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet || request.Method == http.MethodHead || request.Method == http.MethodOptions {
			next.ServeHTTP(writer, request)
			return
		}
		if !server.sameOrigin(request) {
			server.auditSecurityFailure(request, "security.origin_rejected", "origin")
			writeProblem(writer, request, http.StatusForbidden, "origin_validation_failed", "请求来源无效", "管理写请求必须来自本站。")
			return
		}
		current := principalFromContext(request.Context())
		csrfCookie, cookieErr := request.Cookie(server.config.CSRFCookieName)
		csrfHeader := request.Header.Get("X-CSRF-Token")
		if cookieErr != nil || csrfCookie.Value == "" || csrfHeader == "" ||
			subtle.ConstantTimeCompare([]byte(csrfCookie.Value), []byte(csrfHeader)) != 1 {
			server.auditSecurityFailure(request, "security.csrf_rejected", "token_mismatch")
			writeProblem(writer, request, http.StatusForbidden, "csrf_validation_failed", "安全校验失败", "请刷新页面后重试。")
			return
		}
		digest := auth.TokenDigest(csrfHeader)
		if subtle.ConstantTimeCompare(digest[:], current.Session.CSRFTokenDigest) != 1 {
			server.auditSecurityFailure(request, "security.csrf_rejected", "token_invalid")
			writeProblem(writer, request, http.StatusForbidden, "csrf_validation_failed", "安全校验失败", "请刷新页面后重试。")
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func (server *Server) requireRole(allowed ...auth.Role) func(http.Handler) http.Handler {
	accepted := make(map[auth.Role]struct{}, len(allowed))
	for _, role := range allowed {
		accepted[role] = struct{}{}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			current := principalFromContext(request.Context())
			if _, ok := accepted[current.Session.User.Role]; !ok {
				server.auditSecurityFailure(request, "authorization.denied", "role")
				writeProblem(writer, request, http.StatusForbidden, "permission_denied", "无权执行此操作", "当前账号角色没有所需权限。")
				return
			}
			next.ServeHTTP(writer, request)
		})
	}
}

func (server *Server) requireSameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !server.sameOrigin(request) {
			server.auditSecurityFailure(request, "security.origin_rejected", "login_origin")
			writeProblem(writer, request, http.StatusForbidden, "origin_validation_failed", "请求来源无效", "登录请求必须来自本站。")
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func (server *Server) sameOrigin(request *http.Request) bool {
	if server.config.AllowInsecureHTTP {
		return true
	}
	source := request.Header.Get("Origin")
	if source == "" {
		source = request.Header.Get("Referer")
	}
	parsedSource, err := url.Parse(source)
	if err != nil || parsedSource.Scheme == "" || parsedSource.Host == "" || parsedSource.User != nil {
		return false
	}
	expected := server.config.PublicBaseURL
	if expected == "" {
		scheme := "http"
		if request.TLS != nil {
			scheme = "https"
		}
		expected = scheme + "://" + request.Host
	}
	parsedExpected, err := url.Parse(expected)
	if err != nil || parsedExpected.Scheme == "" || parsedExpected.Host == "" {
		return false
	}
	return strings.EqualFold(parsedSource.Scheme, parsedExpected.Scheme) &&
		strings.EqualFold(parsedSource.Host, parsedExpected.Host)
}

func (server *Server) setSessionCookies(writer http.ResponseWriter, token, csrfToken string, expires time.Time) {
	http.SetCookie(writer, &http.Cookie{
		Name:     server.config.SessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		Secure:   !server.config.AllowInsecureHTTP,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	http.SetCookie(writer, &http.Cookie{
		Name:     server.config.CSRFCookieName,
		Value:    csrfToken,
		Path:     "/",
		Expires:  expires,
		Secure:   !server.config.AllowInsecureHTTP,
		HttpOnly: false,
		SameSite: http.SameSiteLaxMode,
	})
}

func (server *Server) clearSessionCookies(writer http.ResponseWriter) {
	for _, cookieName := range []string{server.config.SessionCookieName, server.config.CSRFCookieName} {
		http.SetCookie(writer, &http.Cookie{
			Name:     cookieName,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			Expires:  time.Unix(1, 0),
			Secure:   !server.config.AllowInsecureHTTP,
			HttpOnly: cookieName == server.config.SessionCookieName,
			SameSite: http.SameSiteLaxMode,
		})
	}
}

func newSessionSecrets() (sessionID, token string, tokenDigest []byte, csrfToken string, csrfDigest []byte, err error) {
	sessionID, err = auth.NewID()
	if err != nil {
		return "", "", nil, "", nil, err
	}
	var tokenHash [32]byte
	token, tokenHash, err = auth.NewToken()
	if err != nil {
		return "", "", nil, "", nil, err
	}
	var csrfHash [32]byte
	csrfToken, csrfHash, err = auth.NewToken()
	if err != nil {
		return "", "", nil, "", nil, err
	}
	return sessionID, token, tokenHash[:], csrfToken, csrfHash[:], nil
}

func responseUser(user auth.User) currentUser {
	return currentUser{ID: user.ID, Email: user.Email, Role: user.Role}
}

func principalFromContext(ctx context.Context) principal {
	value, _ := ctx.Value(principalKey).(principal)
	return value
}

func (server *Server) withRequestActor(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		actor := &requestActor{}
		next.ServeHTTP(writer, request.WithContext(context.WithValue(request.Context(), requestActorKey, actor)))
	})
}

func setActor(ctx context.Context, userID string) {
	actor, _ := ctx.Value(requestActorKey).(*requestActor)
	if actor != nil {
		actor.UserID = userID
	}
}

func requestActorID(ctx context.Context) string {
	actor, _ := ctx.Value(requestActorKey).(*requestActor)
	if actor == nil {
		return ""
	}
	return actor.UserID
}

func (server *Server) auditEntry(request *http.Request, entry audit.Entry) audit.Entry {
	entry.SourceIP = server.clientIP(request)
	entry.UserAgent = request.UserAgent()
	return entry
}

func (server *Server) recordAudit(request *http.Request, entry audit.Entry) {
	if server.authStore == nil {
		return
	}
	if err := server.authStore.AppendAudit(request.Context(), server.auditEntry(request, entry)); err != nil {
		server.logger.Error("append audit log", "error", err, "request_id", requestID(request), "action", entry.Action)
	}
}

func (server *Server) auditSecurityFailure(request *http.Request, action, reason string) {
	entry := audit.Entry{
		RequestID:  requestID(request),
		Action:     action,
		TargetType: "request",
		Outcome:    audit.OutcomeFailure,
		Details:    map[string]any{"reason": reason, "path": request.URL.Path},
	}
	if actorID := requestActorID(request.Context()); actorID != "" {
		entry.ActorUserID = &actorID
	}
	server.recordAudit(request, entry)
}

func (server *Server) clientIP(request *http.Request) string {
	host := remoteHost(request.RemoteAddr)
	if server.config.TrustedProxyCount <= 0 {
		return host
	}
	forwarded := strings.Split(request.Header.Get("X-Forwarded-For"), ",")
	addresses := make([]string, 0, len(forwarded)+1)
	for _, value := range forwarded {
		value = strings.TrimSpace(value)
		if net.ParseIP(value) == nil {
			return host
		}
		addresses = append(addresses, value)
	}
	addresses = append(addresses, host)
	clientIndex := len(addresses) - server.config.TrustedProxyCount - 1
	if clientIndex < 0 {
		return host
	}
	return addresses[clientIndex]
}

func remoteHost(remoteAddress string) string {
	host, _, err := net.SplitHostPort(remoteAddress)
	if err == nil {
		return host
	}
	return remoteAddress
}

func requestID(request *http.Request) string {
	if value := middleware.GetReqID(request.Context()); value != "" {
		return value
	}
	return "unknown"
}

func decodeJSONBody(writer http.ResponseWriter, request *http.Request, target any) error {
	if request.ContentLength > maxJSONBodyBytes {
		return &http.MaxBytesError{Limit: maxJSONBodyBytes}
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maxJSONBodyBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("JSON 请求体无效: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("JSON 请求体只能包含一个对象")
	}
	return nil
}

func writeJSONBodyProblem(writer http.ResponseWriter, request *http.Request, title string, err error) {
	var sizeError *http.MaxBytesError
	if errors.As(err, &sizeError) {
		writeProblem(writer, request, http.StatusRequestEntityTooLarge, "request_body_too_large", "请求体过大", "普通 JSON 请求体不得超过 2 MiB。")
		return
	}
	writeProblem(writer, request, http.StatusBadRequest, "invalid_request", title, err.Error())
}

func (server *Server) requireEmptyCatalog(writer http.ResponseWriter, request *http.Request) bool {
	count, err := server.store.PublishedEventCount(request.Context())
	if err != nil {
		server.logger.Error("query published event catalog", "error", err, "request_id", requestID(request))
		writeProblem(writer, request, http.StatusServiceUnavailable, "database_unavailable", "暂时无法读取历史事件", "数据库查询失败，请稍后重试。")
		return false
	}
	if count != 0 {
		writeProblem(writer, request, http.StatusNotImplemented, "published_event_query_pending", "公开查询尚未启用", "数据库已经包含已发布历史事件，但当前服务版本只支持空数据库读取。")
		return false
	}
	return true
}

func requiredFiniteCoordinate(request *http.Request, name string) (float64, error) {
	raw := request.URL.Query().Get(name)
	if raw == "" {
		return 0, fmt.Errorf("缺少查询参数 %s", name)
	}
	coordinate, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsInf(coordinate, 0) || math.IsNaN(coordinate) {
		return 0, fmt.Errorf("查询参数 %s 必须是有限数值", name)
	}
	return coordinate, nil
}

func prominenceForSpan(span float64) int {
	if span >= 4000 {
		return 1
	}
	if span >= 1200 {
		return 2
	}
	return 3
}

func writeInvalidQuery(writer http.ResponseWriter, request *http.Request, err error) {
	writeProblem(writer, request, http.StatusBadRequest, "invalid_query", "查询参数无效", err.Error())
}

type problem struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Detail    string `json:"detail"`
	Code      string `json:"code"`
	RequestID string `json:"requestId,omitempty"`
}

func writeProblem(writer http.ResponseWriter, request *http.Request, status int, code, title, detail string) {
	writer.Header().Set("Content-Type", "application/problem+json")
	writeJSONWithStatus(writer, status, problem{
		Type:      "https://history-wiki.example/problems/" + code,
		Title:     title,
		Status:    status,
		Detail:    detail,
		Code:      code,
		RequestID: requestID(request),
	})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writeJSONWithStatus(writer, status, value)
}

func writeJSONWithStatus(writer http.ResponseWriter, status int, value any) {
	writer.WriteHeader(status)
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		panic(err)
	}
}

func (server *Server) logRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		wrapped := middleware.NewWrapResponseWriter(writer, request.ProtoMajor)
		startedAt := time.Now()
		server.metrics.inFlight.Add(1)
		defer server.metrics.inFlight.Add(-1)
		next.ServeHTTP(wrapped, request)
		status := wrapped.Status()
		if status == 0 {
			status = http.StatusOK
		}
		duration := time.Since(startedAt)
		route := metricRoute(request)
		server.metrics.recordHTTP(metricMethod(request.Method), route, status, duration)
		if request.Method == http.MethodPost && strings.HasSuffix(route, "/publish") {
			server.metrics.recordResult("publish", metricOutcome(status, false))
		}
		if request.Method == http.MethodPost && route == "/api/v1/admin/event-imports" {
			server.metrics.recordResult("import", metricOutcome(status, wrapped.Header().Get("Idempotency-Replayed") == "true"))
		}
		server.logger.Info("http request",
			"request_id", requestID(request),
			"actor_user_id", requestActorID(request.Context()),
			"method", request.Method,
			"route", route,
			"status", status,
			"duration_ms", duration.Milliseconds(),
		)
	})
}

func (server *Server) exposeRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("X-Request-ID", requestID(request))
		next.ServeHTTP(writer, request)
	})
}

func metricOutcome(status int, replayed bool) string {
	if replayed && status >= 200 && status < 300 {
		return "replayed"
	}
	if status >= 200 && status < 300 {
		return "success"
	}
	return "failure"
}
