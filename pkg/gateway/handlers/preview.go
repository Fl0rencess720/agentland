package handlers

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Fl0rencess720/agentland/pkg/gateway/config"
	"github.com/Fl0rencess720/agentland/pkg/gateway/pkgs/db"
	"github.com/Fl0rencess720/agentland/pkg/gateway/pkgs/response"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	defaultPreviewTTL = time.Hour
	maxPreviewTTL     = 24 * time.Hour
)

type PreviewStore interface {
	Create(ctx context.Context, token string, info *db.PreviewInfo, ttl time.Duration) error
	Get(ctx context.Context, token string) (*db.PreviewInfo, error)
}

type PreviewHandler struct {
	sessionStore   SessionStore
	previewStore   PreviewStore
	tokenSigner    TokenSigner
	proxyEngine    *ProxyEngine
	tokenGenerator func() string
	now            func() time.Time
}

type CreatePreviewReq struct {
	Port             int `json:"port"`
	ExpiresInSeconds int `json:"expires_in_seconds,omitempty"`
}

type CreatePreviewResp struct {
	SessionID    string    `json:"session_id"`
	Port         int       `json:"port"`
	PreviewToken string    `json:"preview_token"`
	PreviewURL   string    `json:"preview_url"`
	ExpiresAt    time.Time `json:"expires_at"`
}

func InitPreviewApi(api *gin.RouterGroup, root gin.IRoutes, cfg *config.Config) {
	signer, err := BuildTokenSigner(cfg)
	if err != nil {
		zap.L().Error("Init Preview TokenSigner failed", zap.Error(err))
		return
	}

	h := &PreviewHandler{
		sessionStore: db.NewSessionStore(),
		previewStore: db.NewPreviewStore(),
		tokenSigner:  signer,
		proxyEngine:  NewProxyEngine(),
		tokenGenerator: func() string {
			return uuid.NewString()
		},
		now: time.Now,
	}

	api.POST("/previews", h.CreatePreview)
	root.Any("/p/:previewToken", h.ProxyPreview)
	root.Any("/p/:previewToken/*path", h.ProxyPreview)
}

func (h *PreviewHandler) CreatePreview(ctx *gin.Context) {
	var req CreatePreviewReq
	_, ok := bindJSONWithBody(ctx, &req)
	if !ok {
		return
	}

	sessionID := strings.TrimSpace(ctx.GetHeader(SessionHeader))
	if sessionID == "" || req.Port < 1 || req.Port > 65535 {
		response.ErrorResponse(ctx, response.FormError)
		return
	}

	ttl := defaultPreviewTTL
	if req.ExpiresInSeconds != 0 {
		ttl = time.Duration(req.ExpiresInSeconds) * time.Second
	}
	if ttl <= 0 || ttl > maxPreviewTTL {
		response.ErrorResponse(ctx, response.FormError)
		return
	}

	reqCtx, _ := initRequestContext(ctx)
	if _, err := h.sessionStore.GetSession(reqCtx, sessionID); err != nil {
		if errors.Is(err, db.ErrSessionNotFound) {
			ctx.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
			return
		}
		response.ErrorResponse(ctx, response.ServerError)
		return
	}
	if err := h.sessionStore.UpdateLatestActivity(reqCtx, sessionID); err != nil {
		if errors.Is(err, db.ErrSessionNotFound) {
			ctx.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
			return
		}
		response.ErrorResponse(ctx, response.ServerError)
		return
	}

	previewToken := h.tokenGenerator()
	if err := h.previewStore.Create(reqCtx, previewToken, &db.PreviewInfo{SessionID: sessionID, Port: req.Port}, ttl); err != nil {
		response.ErrorResponse(ctx, response.ServerError)
		return
	}

	now := h.now().UTC()
	response.SuccessResponse(ctx, CreatePreviewResp{
		SessionID:    sessionID,
		Port:         req.Port,
		PreviewToken: previewToken,
		PreviewURL:   buildPreviewURL(ctx, previewToken),
		ExpiresAt:    now.Add(ttl),
	})
}

func (h *PreviewHandler) ProxyPreview(ctx *gin.Context) {
	previewToken := strings.TrimSpace(ctx.Param("previewToken"))
	if previewToken == "" {
		response.ErrorResponse(ctx, response.FormError)
		return
	}

	bodyBytes, ok := readRequestBody(ctx)
	if !ok {
		return
	}
	if len(bodyBytes) == 0 {
		bodyBytes = nil
	}

	reqCtx, requestID := initRequestContext(ctx)
	info, err := h.previewStore.Get(reqCtx, previewToken)
	if err != nil {
		if errors.Is(err, db.ErrPreviewNotFound) {
			ctx.JSON(http.StatusNotFound, gin.H{"error": "preview not found"})
			return
		}
		response.ErrorResponse(ctx, response.ServerError)
		return
	}

	sandboxInfo, err := h.sessionStore.GetSession(reqCtx, info.SessionID)
	if err != nil {
		if errors.Is(err, db.ErrSessionNotFound) {
			ctx.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
			return
		}
		response.ErrorResponse(ctx, response.ServerError)
		return
	}
	if err := h.sessionStore.UpdateLatestActivity(reqCtx, info.SessionID); err != nil {
		if errors.Is(err, db.ErrSessionNotFound) {
			ctx.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
			return
		}
		response.ErrorResponse(ctx, response.ServerError)
		return
	}

	sandboxToken, err := h.tokenSigner.Sign(info.SessionID, "", 0)
	if err != nil {
		response.ErrorResponse(ctx, response.ServerError)
		return
	}

	target, err := resolveSandboxTarget(sandboxInfo.GrpcEndpoint)
	if err != nil {
		response.ErrorResponse(ctx, response.ServerError)
		return
	}

	internalPath := "/api/proxy/by-port/" + strconv.Itoa(info.Port)
	if subPath := ctx.Param("path"); subPath != "" {
		internalPath += subPath
	}

	h.proxyEngine.Forward(ctx, ProxyConfig{
		Target:       target,
		Method:       ctx.Request.Method,
		InternalPath: internalPath,
		Body:         bodyBytes,
		SessionID:    info.SessionID,
		SandboxToken: sandboxToken,
		RequestID:    requestID,
	})
}

func buildPreviewURL(ctx *gin.Context, previewToken string) string {
	scheme := strings.TrimSpace(ctx.GetHeader("X-Forwarded-Proto"))
	if scheme == "" {
		if ctx.Request.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	host := strings.TrimSpace(ctx.GetHeader("X-Forwarded-Host"))
	if host == "" {
		host = ctx.Request.Host
	}
	return scheme + "://" + host + "/p/" + previewToken + "/"
}
