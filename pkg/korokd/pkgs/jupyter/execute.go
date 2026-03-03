package jupyter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/net/websocket"
)

type ExecuteResult struct {
	Status         string
	ExecutionCount int64
	Stdout         string
	Stderr         string
	Duration       time.Duration
}

type streamContent struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

type errorContent struct {
	EName     string   `json:"ename"`
	EValue    string   `json:"evalue"`
	Traceback []string `json:"traceback"`
}

type statusContent struct {
	ExecutionState string `json:"execution_state"`
}

type executeInputContent struct {
	ExecutionCount int64 `json:"execution_count"`
}

type executeReplyContent struct {
	Status         string   `json:"status"`
	ExecutionCount int64    `json:"execution_count"`
	EName          string   `json:"ename"`
	EValue         string   `json:"evalue"`
	Traceback      []string `json:"traceback"`
}

type executeRequestContent struct {
	Code            string            `json:"code"`
	Silent          bool              `json:"silent"`
	StoreHistory    bool              `json:"store_history"`
	UserExpressions map[string]string `json:"user_expressions"`
	AllowStdin      bool              `json:"allow_stdin"`
	StopOnError     bool              `json:"stop_on_error"`
}

func (c *Client) Execute(ctx context.Context, kernelID, code string) (*ExecuteResult, error) {
	wsURL, err := c.KernelChannelsURL(kernelID)
	if err != nil {
		return nil, err
	}

	cfg, err := websocket.NewConfig(wsURL, originForWSURL(wsURL))
	if err != nil {
		return nil, fmt.Errorf("build websocket config failed: %w", err)
	}
	conn, err := websocket.DialConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("connect jupyter kernel channels failed: %w", err)
	}
	defer conn.Close()

	reqID := uuid.NewString()
	session := uuid.NewString()
	now := time.Now()

	reqContent, _ := json.Marshal(&executeRequestContent{
		Code:            code,
		Silent:          false,
		StoreHistory:    true,
		UserExpressions: map[string]string{},
		AllowStdin:      false,
		StopOnError:     false,
	})

	msg := &wireMessage{
		Header: messageHeader{
			MessageID:   reqID,
			Username:    "korokd",
			Session:     session,
			Date:        now.Format(time.RFC3339),
			MessageType: "execute_request",
			Version:     "5.3",
		},
		ParentHeader: messageHeader{},
		Metadata:     map[string]any{},
		Content:      reqContent,
		Channel:      "shell",
	}

	start := time.Now()
	if err := websocket.JSON.Send(conn, msg); err != nil {
		return nil, fmt.Errorf("send execute_request failed: %w", err)
	}

	type recv struct {
		msg wireMessage
		err error
	}
	recvCh := make(chan recv, 16)
	go func() {
		defer close(recvCh)
		for {
			var m wireMessage
			if err := websocket.JSON.Receive(conn, &m); err != nil {
				select {
				case recvCh <- recv{err: err}:
				case <-ctx.Done():
				}
				return
			}
			select {
			case recvCh <- recv{msg: m}:
			case <-ctx.Done():
				return
			}
		}
	}()

	var stdout strings.Builder
	var stderr strings.Builder
	var execCount int64
	hadError := false
	replyStatus := ""
	gotReply := false
	gotIdle := false

	for {
		select {
		case <-ctx.Done():
			_ = conn.Close()
			return &ExecuteResult{
				Status:         "timeout",
				ExecutionCount: execCount,
				Stdout:         stdout.String(),
				Stderr:         stderr.String(),
				Duration:       time.Since(start),
			}, ctx.Err()
		case r, ok := <-recvCh:
			if !ok {
				return &ExecuteResult{
					Status:         statusFrom(hadError, replyStatus),
					ExecutionCount: execCount,
					Stdout:         stdout.String(),
					Stderr:         stderr.String(),
					Duration:       time.Since(start),
				}, nil
			}
			if r.err != nil {
				return &ExecuteResult{
					Status:         "error",
					ExecutionCount: execCount,
					Stdout:         stdout.String(),
					Stderr:         stderr.String(),
					Duration:       time.Since(start),
				}, fmt.Errorf("read kernel message failed: %w", r.err)
			}

			// Filter unrelated messages.
			if r.msg.ParentHeader.MessageID != reqID {
				continue
			}

			switch r.msg.Header.MessageType {
			case "stream":
				var sc streamContent
				if err := json.Unmarshal(r.msg.Content, &sc); err == nil {
					if sc.Name == "stderr" {
						stderr.WriteString(sc.Text)
					} else {
						stdout.WriteString(sc.Text)
					}
				}
			case "error":
				var ec errorContent
				if err := json.Unmarshal(r.msg.Content, &ec); err == nil {
					hadError = true
					if len(ec.Traceback) > 0 {
						stderr.WriteString(strings.Join(ec.Traceback, "\n"))
						stderr.WriteString("\n")
					} else if ec.EValue != "" {
						stderr.WriteString(ec.EValue)
						stderr.WriteString("\n")
					} else if ec.EName != "" {
						stderr.WriteString(ec.EName)
						stderr.WriteString("\n")
					}
				}
			case "execute_input":
				var ic executeInputContent
				if err := json.Unmarshal(r.msg.Content, &ic); err == nil {
					if ic.ExecutionCount > 0 {
						execCount = ic.ExecutionCount
					}
				}
			case "execute_reply":
				var rc executeReplyContent
				if err := json.Unmarshal(r.msg.Content, &rc); err == nil {
					gotReply = true
					replyStatus = rc.Status
					if rc.ExecutionCount > 0 {
						execCount = rc.ExecutionCount
					}
					if rc.Status == "error" {
						hadError = true
						if len(rc.Traceback) > 0 {
							stderr.WriteString(strings.Join(rc.Traceback, "\n"))
							stderr.WriteString("\n")
						} else if rc.EValue != "" {
							stderr.WriteString(rc.EValue)
							stderr.WriteString("\n")
						}
					}
				}
			case "status":
				var st statusContent
				if err := json.Unmarshal(r.msg.Content, &st); err == nil {
					if st.ExecutionState == "idle" {
						gotIdle = true
					}
				}
			default:
			}

			if gotIdle && gotReply {
				return &ExecuteResult{
					Status:         statusFrom(hadError, replyStatus),
					ExecutionCount: execCount,
					Stdout:         stdout.String(),
					Stderr:         stderr.String(),
					Duration:       time.Since(start),
				}, nil
			}
		}
	}
}

func statusFrom(hadError bool, replyStatus string) string {
	if replyStatus == "error" || hadError {
		return "error"
	}
	if replyStatus == "" {
		return "ok"
	}
	return replyStatus
}

func originForWSURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "http://127.0.0.1/"
	}
	scheme := "http"
	if parsed.Scheme == "wss" {
		scheme = "https"
	}
	return scheme + "://" + parsed.Host + "/"
}
