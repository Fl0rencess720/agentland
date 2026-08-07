package httpbind

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/Fl0rencess720/agentland/app/be/internal/pkgs/response"
	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
)

func JSON(c *gin.Context, target any, maxBytes int64) *response.APIError {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
	decoder := json.NewDecoder(c.Request.Body)
	if err := decoder.Decode(target); err != nil {
		return jsonError(err)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return response.InvalidArgumentError("request", "multiple JSON values")
		}
		return jsonError(err)
	}
	if err := binding.Validator.ValidateStruct(target); err != nil {
		return response.ValidationError(err)
	}
	return nil
}

func jsonError(err error) *response.APIError {
	var sizeErr *http.MaxBytesError
	if errors.As(err, &sizeErr) {
		return response.RequestTooLargeError()
	}
	return response.ValidationError(err)
}
