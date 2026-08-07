package service

import (
	"context"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/Fl0rencess720/agentland/app/be/configs"
	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

type previewActivityStore interface {
	TouchRuntimeByPreviewToken(context.Context, string, time.Time) error
}

func PreviewProxy(activity previewActivityStore) gin.HandlerFunc {
	publicURLTemplate := strings.TrimSpace(viper.GetString("preview.public_url_template"))
	if publicURLTemplate == "" {
		publicURLTemplate = configs.DefaultPreviewPublicURLTemplate
	}
	return newPreviewProxy(
		viper.GetString("agentland-gateway.url"),
		publicURLTemplate,
		http.DefaultTransport,
		activity,
	)
}

func newPreviewProxy(rawTarget, publicURLTemplate string, transport http.RoundTripper, activity previewActivityStore) gin.HandlerFunc {
	target, err := url.Parse(strings.TrimRight(strings.TrimSpace(rawTarget), "/"))
	if err != nil || target.Scheme == "" || target.Host == "" || configs.ValidatePreviewPublicURLTemplate(publicURLTemplate) != nil {
		return func(c *gin.Context) { c.AbortWithStatus(http.StatusBadGateway) }
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director
	proxy.Director = func(request *http.Request) {
		originalDirector(request)
		request.Host = target.Host
		request.Header.Del("Authorization")
		request.Header.Del("Cookie")
		request.Header.Del("X-Agentland-Session")
	}
	proxy.Transport = transport
	proxy.FlushInterval = -1
	proxy.ModifyResponse = func(response *http.Response) error {
		response.Header.Del("Set-Cookie")
		response.Header.Set("Content-Security-Policy", "sandbox allow-scripts allow-forms allow-popups allow-same-origin")
		response.Header.Set("Access-Control-Allow-Origin", "*")
		response.Header.Del("Access-Control-Allow-Credentials")
		response.Header.Set("Referrer-Policy", "no-referrer")
		response.Header.Set("X-Content-Type-Options", "nosniff")
		if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices && activity != nil && response.Request != nil {
			if token := previewToken(response.Request.URL.Path); token != "" {
				if touchErr := activity.TouchRuntimeByPreviewToken(response.Request.Context(), token, time.Now().UTC()); touchErr != nil {
					zap.L().Warn("touch preview runtime activity failed", zap.Error(touchErr))
				}
			}
		}
		return nil
	}
	proxy.ErrorHandler = func(writer http.ResponseWriter, _ *http.Request, _ error) { writer.WriteHeader(http.StatusBadGateway) }
	return func(c *gin.Context) {
		publicURL, buildErr := configs.PreviewPublicURL(publicURLTemplate, previewToken(c.Request.URL.Path))
		if buildErr != nil {
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "preview not found"})
			return
		}
		expected, _ := url.Parse(publicURL)
		if !strings.EqualFold(strings.TrimSpace(c.Request.Host), expected.Host) {
			c.AbortWithStatusJSON(http.StatusMisdirectedRequest, gin.H{"error": "preview origin mismatch"})
			return
		}
		proxy.ServeHTTP(c.Writer, c.Request)
	}
}

func previewToken(path string) string {
	trimmed := strings.TrimPrefix(path, "/p/")
	if trimmed == path || trimmed == "" {
		return ""
	}
	return strings.SplitN(trimmed, "/", 2)[0]
}
