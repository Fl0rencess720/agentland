package httpbind

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Fl0rencess720/agentland/app/be/internal/models"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestJSONRejectsOversizedBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"value":"`+strings.Repeat("x", 128)+`"}`))
	context, _ := gin.CreateTestContext(recorder)
	context.Request = request
	var target struct {
		Value string `json:"value"`
	}

	apiErr := JSON(context, &target, 64)
	require.NotNil(t, apiErr)
	require.Equal(t, http.StatusRequestEntityTooLarge, apiErr.StatusCode)
	require.Equal(t, "REQUEST_TOO_LARGE", apiErr.Data.Type)
}

func TestJSONRequiresSHAFieldAndAcceptsExplicitEmptySHA(t *testing.T) {
	gin.SetMode(gin.TestMode)

	decode := func(body string) (*models.FileContentUpdateReq, bool) {
		request := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(body))
		context, _ := gin.CreateTestContext(httptest.NewRecorder())
		context.Request = request
		target := &models.FileContentUpdateReq{}
		return target, JSON(context, target, 1024) == nil
	}

	target, ok := decode(`{"content":"restored","sha":""}`)
	require.True(t, ok)
	require.NotNil(t, target.SHA)
	require.Empty(t, *target.SHA)

	_, ok = decode(`{"content":"restored"}`)
	require.False(t, ok)
}

func TestJSONValidatesAndRejectsMultipleValues(t *testing.T) {
	gin.SetMode(gin.TestMode)
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"value":"ok"} {"value":"extra"}`))
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = request
	var target struct {
		Value string `json:"value" binding:"required"`
	}

	apiErr := JSON(context, &target, 1024)
	require.NotNil(t, apiErr)
	require.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
}
