package jupyter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

type Client struct {
	baseURL    *url.URL
	token      string
	httpClient *http.Client
}

type createSessionRequest struct {
	Path   string `json:"path"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Kernel struct {
		Name string `json:"name"`
	} `json:"kernel"`
}

func NewClient(baseURL, token string) (*Client, error) {
	trimmed := strings.TrimSpace(baseURL)
	if trimmed == "" {
		return nil, fmt.Errorf("jupyter base url is empty")
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("parse jupyter base url failed: %w", err)
	}
	u.Path = strings.TrimRight(u.Path, "/")

	return &Client{
		baseURL: u,
		token:   strings.TrimSpace(token),
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}, nil
}

func (c *Client) withPath(p string) string {
	u := *c.baseURL
	clean := strings.TrimLeft(p, "/")
	u.Path = path.Join(c.baseURL.Path, clean)
	if !strings.HasPrefix(u.Path, "/") {
		u.Path = "/" + u.Path
	}
	return u.String()
}

func (c *Client) doJSON(ctx context.Context, method, p string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request failed: %w", err)
		}
		reader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.withPath(p), reader)
	if err != nil {
		return fmt.Errorf("build request failed: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Token "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &HTTPError{Status: resp.StatusCode, Body: strings.TrimSpace(string(respBody))}
	}

	if out != nil {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("decode response failed: %w", err)
		}
	}

	return nil
}

func (c *Client) GetKernelSpecs(ctx context.Context) (*KernelSpecs, error) {
	var specs KernelSpecs
	if err := c.doJSON(ctx, http.MethodGet, "api/kernelspecs", nil, &specs); err != nil {
		return nil, err
	}
	return &specs, nil
}

func (c *Client) CreateSession(ctx context.Context, name, notebookPath, kernelName string) (*Session, error) {
	req := &createSessionRequest{
		Path: notebookPath,
		Name: name,
		Type: "notebook",
	}
	req.Kernel.Name = kernelName

	var sess Session
	if err := c.doJSON(ctx, http.MethodPost, "api/sessions", req, &sess); err != nil {
		return nil, err
	}

	return &sess, nil
}

func (c *Client) DeleteSession(ctx context.Context, sessionID string) error {
	return c.doJSON(ctx, http.MethodDelete, "api/sessions/"+url.PathEscape(sessionID), nil, nil)
}

func (c *Client) InterruptKernel(ctx context.Context, kernelID string) error {
	return c.doJSON(ctx, http.MethodPost, "api/kernels/"+url.PathEscape(kernelID)+"/interrupt", nil, nil)
}

func (c *Client) KernelChannelsURL(kernelID string) (string, error) {
	u := *c.baseURL
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	default:
		return "", fmt.Errorf("unsupported jupyter scheme: %s", c.baseURL.Scheme)
	}
	u.Path = path.Join(c.baseURL.Path, "api/kernels", kernelID, "channels")
	if !strings.HasPrefix(u.Path, "/") {
		u.Path = "/" + u.Path
	}

	if c.token != "" {
		q := u.Query()
		q.Set("token", c.token)
		u.RawQuery = q.Encode()
	}
	return u.String(), nil
}
