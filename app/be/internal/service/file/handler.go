package file

import (
	"net/http"

	"github.com/Fl0rencess720/agentland/app/be/internal/biz"
	"github.com/Fl0rencess720/agentland/app/be/internal/models"
	"github.com/Fl0rencess720/agentland/app/be/internal/pkgs/response"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type FileHandler struct {
	fileUseCase biz.FileUseCase
}

func NewFileHandler(fileUseCase biz.FileUseCase) *FileHandler {
	return &FileHandler{fileUseCase: fileUseCase}
}

func (h *FileHandler) Upload(c *gin.Context) {
	ctx := c.Request.Context()
	_ = ctx

	req := models.FileUploadReq{}
	if err := c.ShouldBind(&req); err != nil {
		zap.L().Error("request bind error", zap.Error(err))
		response.ErrorResponse(c, http.StatusBadRequest, "invalid_argument", gin.H{"type": "VALIDATION_ERROR"})
		return
	}

	response.MessageResponse(c, "uploaded", models.FileUploadResp{})
}

func (h *FileHandler) Detail(c *gin.Context) {
	ctx := c.Request.Context()
	_ = ctx

	response.SuccessResponse(c, models.FileMetadataResp{FileID: c.Param("file_id")})
}
