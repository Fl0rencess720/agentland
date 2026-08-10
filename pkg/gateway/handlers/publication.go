package handlers

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Fl0rencess720/agentland/pkg/gateway/config"
	"github.com/Fl0rencess720/agentland/pkg/gateway/deployer"
	"github.com/Fl0rencess720/agentland/pkg/gateway/publisher"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const maxWorkspaceSnapshotBytes = int64(8 << 20)

type ImagePublisher interface {
	Build(context.Context, publisher.Request) (*publisher.Result, error)
}

type ApplicationDeployer interface {
	Deploy(context.Context, deployer.Request) (*deployer.Result, error)
}

type PublicationHandler struct {
	enabled      bool
	publisher    ImagePublisher
	deployer     ApplicationDeployer
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
		h.publisher = imagePublisher
		applicationDeployer, err := deployer.New(deployer.Config{
			Namespace: cfg.ApplicationNamespace, BaseDomain: cfg.ApplicationBaseDomain,
			IngressClass: cfg.ApplicationIngressClass, TLSSecret: cfg.ApplicationTLSSecret,
			RuntimeClass: cfg.ApplicationRuntimeClass, ImagePullSecret: cfg.ApplicationImagePullSecret,
			Port: cfg.ApplicationPort, Replicas: cfg.ApplicationReplicas, Timeout: cfg.ApplicationDeployTimeout,
			CPURequest: cfg.ApplicationCPURequest, MemoryRequest: cfg.ApplicationMemoryRequest,
			CPULimit: cfg.ApplicationCPULimit, MemoryLimit: cfg.ApplicationMemoryLimit,
		})
		if err != nil {
			return fmt.Errorf("initialize application deployer: %w", err)
		}
		h.deployer = applicationDeployer
		h.serviceToken = strings.TrimSpace(cfg.PublisherServiceToken)
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
	if h.publisher == nil || h.deployer == nil {
		publicationError(ctx, http.StatusServiceUnavailable, "PUBLISHER_UNAVAILABLE", "application deployment is not configured")
		return
	}
	request := publicationRequest{
		ProjectID: strings.TrimSpace(ctx.Query("project_id")), ReleaseID: strings.TrimSpace(ctx.Query("release_id")),
		Context: strings.TrimSpace(ctx.Query("context")), Dockerfile: strings.TrimSpace(ctx.Query("dockerfile")),
	}
	if request.ProjectID == "" || request.ReleaseID == "" {
		publicationError(ctx, http.StatusBadRequest, "INVALID_ARGUMENT", "project_id and release_id are required")
		return
	}
	snapshot, err := io.ReadAll(io.LimitReader(ctx.Request.Body, maxWorkspaceSnapshotBytes+1))
	if err != nil {
		publicationError(ctx, http.StatusBadRequest, "WORKSPACE_SNAPSHOT_INVALID", err.Error())
		return
	}
	if len(snapshot) == 0 || int64(len(snapshot)) > maxWorkspaceSnapshotBytes {
		publicationError(ctx, http.StatusRequestEntityTooLarge, "WORKSPACE_SNAPSHOT_INVALID", "workspace snapshot must be between 1 byte and 8 MiB")
		return
	}
	build, err := h.publisher.Build(ctx.Request.Context(), publisher.Request{
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
	deployment, err := h.deployer.Deploy(ctx.Request.Context(), deployer.Request{
		ProjectID: request.ProjectID, ReleaseID: request.ReleaseID, ImageRef: build.ImageRef, Digest: build.Digest,
	})
	if err != nil {
		if errors.Is(ctx.Request.Context().Err(), context.Canceled) {
			return
		}
		zap.L().Warn("Deploy application image failed", zap.String("project_id", request.ProjectID), zap.String("release_id", request.ReleaseID), zap.Error(err))
		ctx.JSON(http.StatusUnprocessableEntity, gin.H{
			"code": "APPLICATION_DEPLOY_FAILED", "message": err.Error(), "logs": build.Logs,
			"image_ref": build.ImageRef, "digest": build.Digest,
		})
		return
	}
	ctx.JSON(http.StatusOK, gin.H{
		"image_ref": build.ImageRef, "digest": build.Digest, "logs": build.Logs,
		"deployment_url": deployment.URL, "deployment_hostname": deployment.Hostname, "deployment_name": deployment.DeploymentName,
	})
}

func publicationError(ctx *gin.Context, status int, code, message string) {
	ctx.JSON(status, gin.H{"code": code, "message": message})
}
