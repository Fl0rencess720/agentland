package handlers

import (
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

type ProxyOptions struct {
	Transport http.RoundTripper
}

type ProxyHandler struct {
	opts ProxyOptions
}

func InitProxyApi(group *gin.RouterGroup, opts ProxyOptions) {
	h := &ProxyHandler{opts: opts}
	group.Any("/proxy/by-port/:port", h.ProxyByPort)
	group.Any("/proxy/by-port/:port/*path", h.ProxyByPort)
}

func (h *ProxyHandler) ProxyByPort(c *gin.Context) {
	port, err := strconv.Atoi(strings.TrimSpace(c.Param("port")))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "port must be an integer"})
		return
	}

	if err := h.validatePort(port); err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}

	scheme := strings.TrimSpace(strings.ToLower(c.DefaultQuery("scheme", "http")))
	if scheme != "http" && scheme != "https" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "scheme must be http or https"})
		return
	}

	target := &url.URL{
		Scheme: scheme,
		Host:   net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
	}

	upstreamPath := c.Param("path")
	if upstreamPath == "" {
		upstreamPath = "/"
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	if h.opts.Transport != nil {
		proxy.Transport = h.opts.Transport
	} else {
		proxy.Transport = &http.Transport{
			MaxIdleConns:        200,
			MaxIdleConnsPerHost: 20,
			IdleConnTimeout:     90 * time.Second,
		}
	}

	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Method = c.Request.Method
		req.URL.Path = upstreamPath
		req.URL.RawPath = upstreamPath
		req.URL.RawQuery = sanitizedQuery(c.Request.URL.Query()).Encode()
		req.Host = target.Host
		req.Header = c.Request.Header.Clone()

		// Never forward internal auth/session headers to user workloads.
		req.Header.Del("Authorization")
		req.Header.Del("X-Agentland-Session")
		req.Header.Del("x-agentland-session")
	}

	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		http.Error(w, "upstream is unreachable", http.StatusBadGateway)
	}

	proxy.ServeHTTP(closeNotifySafeWriter{ResponseWriter: c.Writer}, c.Request)
}

func (h *ProxyHandler) validatePort(port int) error {
	if port < 1 || port > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	return nil
}

func sanitizedQuery(values url.Values) url.Values {
	cloned := make(url.Values, len(values))
	for k, v := range values {
		copied := make([]string, len(v))
		copy(copied, v)
		cloned[k] = copied
	}
	cloned.Del("scheme")
	return cloned
}

type closeNotifySafeWriter struct {
	gin.ResponseWriter
}

func (w closeNotifySafeWriter) CloseNotify() <-chan bool {
	return nil
}
