package response

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
)

type Body struct {
	Msg  string `json:"msg"`
	Code int    `json:"code"`
	Data any    `json:"data"`
}

type ErrorDetail struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

type ErrorData struct {
	Type    string        `json:"type"`
	Details []ErrorDetail `json:"details,omitempty"`
	SHA     string        `json:"sha,omitempty"`
}

type APIError struct {
	StatusCode int
	Msg        string
	Data       ErrorData
}

func (e *APIError) Error() string {
	return e.Msg
}

func SuccessResponse(c *gin.Context, data any) {
	c.JSON(http.StatusOK, Body{Msg: "ok", Code: http.StatusOK, Data: data})
}

func MessageResponse(c *gin.Context, msg string, data any) {
	c.JSON(http.StatusOK, Body{Msg: msg, Code: http.StatusOK, Data: data})
}

func AcceptedResponse(c *gin.Context, data any) {
	c.JSON(http.StatusAccepted, Body{Msg: "accepted", Code: http.StatusAccepted, Data: data})
}

func ErrorResponse(c *gin.Context, statusCode int, msg string, data any) {
	c.JSON(statusCode, Body{Msg: msg, Code: statusCode, Data: data})
}

func WriteAPIError(c *gin.Context, err *APIError) {
	if err == nil {
		return
	}
	c.JSON(err.StatusCode, Body{Msg: err.Msg, Code: err.StatusCode, Data: err.Data})
}

func ValidationError(bindErr error) *APIError {
	details := make([]ErrorDetail, 0)
	if validationErrs, ok := bindErr.(validator.ValidationErrors); ok {
		for _, item := range validationErrs {
			details = append(details, ErrorDetail{Field: item.Field(), Reason: item.ActualTag()})
		}
	}
	if len(details) == 0 {
		details = append(details, ErrorDetail{Field: "request", Reason: bindErr.Error()})
	}
	return &APIError{
		StatusCode: http.StatusBadRequest,
		Msg:        "invalid_argument",
		Data:       ErrorData{Type: "VALIDATION_ERROR", Details: details},
	}
}

func UnauthorizedError() *APIError {
	return &APIError{
		StatusCode: http.StatusUnauthorized,
		Msg:        "unauthorized",
		Data:       ErrorData{Type: "AUTH_ERROR"},
	}
}

func NotFoundError() *APIError {
	return &APIError{
		StatusCode: http.StatusNotFound,
		Msg:        "not_found",
		Data:       ErrorData{Type: "NOT_FOUND"},
	}
}

func RequestTooLargeError() *APIError {
	return &APIError{
		StatusCode: http.StatusRequestEntityTooLarge,
		Msg:        "request_too_large",
		Data:       ErrorData{Type: "REQUEST_TOO_LARGE"},
	}
}

func RuntimeUnavailableError() *APIError {
	return &APIError{
		StatusCode: http.StatusConflict,
		Msg:        "runtime_unavailable",
		Data:       ErrorData{Type: "RUNTIME_UNAVAILABLE"},
	}
}

func ProjectRuntimeExpiredError() *APIError {
	return &APIError{
		StatusCode: http.StatusConflict,
		Msg:        "project_runtime_expired",
		Data:       ErrorData{Type: "PROJECT_RUNTIME_EXPIRED"},
	}
}

func ActiveRunConflictError() *APIError {
	return &APIError{
		StatusCode: http.StatusConflict,
		Msg:        "project_run_active",
		Data:       ErrorData{Type: "PROJECT_RUN_ACTIVE"},
	}
}

func IdempotencyConflictError() *APIError {
	return &APIError{
		StatusCode: http.StatusConflict,
		Msg:        "idempotency_conflict",
		Data:       ErrorData{Type: "IDEMPOTENCY_CONFLICT"},
	}
}

func FileConflictError(sha string) *APIError {
	return &APIError{
		StatusCode: http.StatusConflict,
		Msg:        "file_conflict",
		Data:       ErrorData{Type: "FILE_CONFLICT", SHA: sha},
	}
}

func ReplayUnavailableError(reason string) *APIError {
	return &APIError{
		StatusCode: http.StatusConflict,
		Msg:        "replay_unavailable",
		Data: ErrorData{
			Type:    "REPLAY_UNAVAILABLE",
			Details: []ErrorDetail{{Field: "run_id", Reason: reason}},
		},
	}
}

func InternalError() *APIError {
	return &APIError{
		StatusCode: http.StatusInternalServerError,
		Msg:        "internal",
		Data:       ErrorData{Type: "INTERNAL_ERROR"},
	}
}

func InvalidArgumentError(field, reason string) *APIError {
	return &APIError{
		StatusCode: http.StatusBadRequest,
		Msg:        "invalid_argument",
		Data: ErrorData{
			Type:    "VALIDATION_ERROR",
			Details: []ErrorDetail{{Field: field, Reason: reason}},
		},
	}
}
