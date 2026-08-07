package handlers

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Fl0rencess720/agentland/pkg/gateway/config"
	"github.com/Fl0rencess720/agentland/pkg/gateway/pkgs/db"
	"github.com/Fl0rencess720/agentland/pkg/gateway/publisher"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const maxWorkspaceSnapshotBytes = int64(8 << 20)

type ImagePublisher interface {
	Build(context.Context, publisher.Request) (*publisher.Result, error)
}

type PublicationHandler struct {
	enabled      bool
	sessionStore SessionStore
	tokenSigner  TokenSigner
	publisher    ImagePublisher
	httpClient   *http.Client
	serviceToken string
}

type publicationRequest struct {
	ProjectID  string `json:"project_id" binding:"required"`
	ReleaseID  string `json:"release_id" binding:"required"`
	Context    string `json:"context"`
	Dockerfile string `json:"dockerfile"`
}

func InitPublicationAPI(group *gin.RouterGroup, cfg *config.Config) error {
	h := &PublicationHandler{enabled: cfg.PublisherEnabled}
	if cfg.PublisherEnabled {
		if len(strings.TrimSpace(cfg.PublisherServiceToken)) < 32 {
			return errors.New("publisher service token must contain at least 32 characters")
		}
		signer, err := BuildTokenSigner(cfg)
		if err != nil {
			return fmt.Errorf("initialize publication token signer: %w", err)
		}
		imagePublisher, err := publisher.New(publisher.Config{
			BuildctlPath:     cfg.BuildctlPath,
			Address:          cfg.BuildKitAddress,
			RepositoryPrefix: cfg.RegistryRepositoryPrefix,
			Platform:         cfg.BuildKitPlatform,
			Timeout:          cfg.BuildKitTimeout,
			CACert:           cfg.BuildKitCACert,
			ClientCert:       cfg.BuildKitClientCert,
			ClientKey:        cfg.BuildKitClientKey,
			DockerConfig:     cfg.RegistryDockerConfig,
			AllowInsecure:    cfg.BuildKitAllowInsecure,
		})
		if err != nil {
			return fmt.Errorf("initialize image publisher: %w", err)
		}
		h.sessionStore = db.NewSessionStore()
		h.tokenSigner = signer
		h.publisher = imagePublisher
		h.serviceToken = strings.TrimSpace(cfg.PublisherServiceToken)
		h.httpClient = &http.Client{Timeout: 2 * time.Minute, Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     90 * time.Second,
		}}
	}
	group.POST("", h.Publish)
	return nil
}

func (h *PublicationHandler) Publish(ctx *gin.Context) {
	if !h.enabled {
		publicationError(ctx, http.StatusServiceUnavailable, "PUBLISHER_UNAVAILABLE", "image publishing is disabled")
		return
	}
	authorization := strings.TrimSpace(ctx.GetHeader("Authorization"))
	if !strings.HasPrefix(authorization, "Bearer ") {
		publicationError(ctx, http.StatusUnauthorized, "UNAUTHORIZED", "publisher service authentication failed")
		return
	}
	provided := strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer "))
	if len(provided) != len(h.serviceToken) || subtle.ConstantTimeCompare([]byte(provided), []byte(h.serviceToken)) != 1 {
		publicationError(ctx, http.StatusUnauthorized, "UNAUTHORIZED", "publisher service authentication failed")
		return
	}
	var request publicationRequest
	if !bindGatewayJSON(ctx, &request, 64<<10) {
		return
	}
	sessionID := strings.TrimSpace(ctx.GetHeader(SessionHeader))
	if sessionID == "" {
		publicationError(ctx, http.StatusBadRequest, "SESSION_REQUIRED", "x-agentland-session is required")
		return
	}

	snapshot, err := h.workspaceSnapshot(ctx.Request.Context(), sessionID)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, db.ErrSessionNotFound) {
			status = http.StatusGone
		}
		zap.L().Warn("Fetch workspace snapshot for publication failed", zap.String("session_id", sessionID), zap.Error(err))
		publicationError(ctx, status, "WORKSPACE_SNAPSHOT_FAILED", err.Error())
		return
	}
	result, err := h.publisher.Build(ctx.Request.Context(), publisher.Request{
		ProjectID: request.ProjectID, ReleaseID: request.ReleaseID,
		Context: request.Context, Dockerfile: request.Dockerfile, Snapshot: snapshot,
	})
	if err != nil {
		if errors.Is(ctx.Request.Context().Err(), context.Canceled) {
			return
		}
		zap.L().Warn("Build and push application image failed", zap.String("project_id", request.ProjectID), zap.String("release_id", request.ReleaseID), zap.Error(err))
		var buildErr *publisher.BuildError
		if errors.As(err, &buildErr) {
			ctx.JSON(http.StatusUnprocessableEntity, gin.H{"code": "IMAGE_BUILD_FAILED", "message": buildErr.Error(), "logs": buildErr.Logs})
		} else {
			publicationError(ctx, http.StatusUnprocessableEntity, "IMAGE_BUILD_FAILED", err.Error())
		}
		return
	}
	ctx.JSON(http.StatusOK, result)
}

func (h *PublicationHandler) workspaceSnapshot(ctx context.Context, sessionID string) ([]byte, error) {
	info, err := h.sessionStore.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if err := h.sessionStore.UpdateLatestActivity(ctx, sessionID); err != nil {
		zap.L().Warn("Update publication session activity failed", zap.String("session_id", sessionID), zap.Error(err))
	}
	token, err := h.tokenSigner.Sign(sessionID, "", 0)
	if err != nil {
		return nil, fmt.Errorf("sign workspace request: %w", err)
	}
	target, err := resolveSandboxTarget(info.GrpcEndpoint)
	if err != nil {
		return nil, err
	}
	target.Path = "/api/workspace/snapshot"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set(SessionHeader, sessionID)
	response, err := h.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxWorkspaceSnapshotBytes+1))
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sandbox returned status %d: %s", response.StatusCode, strings.TrimSpace(string(data)))
	}
	if int64(len(data)) > maxWorkspaceSnapshotBytes {
		return nil, errors.New("workspace snapshot exceeds 8 MiB")
	}
	return data, nil
}

func bindGatewayJSON(ctx *gin.Context, target any, limit int64) bool {
	ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, limit)
	if err := ctx.ShouldBindJSON(target); err != nil {
		publicationError(ctx, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return false
	}
	return true
}

func publicationError(ctx *gin.Context, status int, code, message string) {
	ctx.JSON(status, gin.H{"code": code, "message": message})
}
