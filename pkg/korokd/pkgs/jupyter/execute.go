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

type ExecuteHooks struct {
	OnStdout         func(text string)
	OnStderr         func(text string)
	OnStatus         func(state string)
	OnExecutionCount func(count int64)
}

// Execute 通过 Jupyter Kernel Channels WebSocket 在指定 kernel 中执行代码并返回聚合结果
// hooks 用于将 stdout stderr 状态与计数以回调形式实时输出
func (c *Client) Execute(ctx context.Context, kernelID, code string, hooks ExecuteHooks) (*ExecuteResult, error) {
	// 通过 kernelID 计算 Jupyter 的 channels WebSocket 地址
	wsURL, err := c.KernelChannelsURL(kernelID)
	if err != nil {
		return nil, err
	}

	// 建立 WebSocket 连接并在方法退出时关闭
	cfg, err := websocket.NewConfig(wsURL, originForWSURL(wsURL))
	if err != nil {
		return nil, fmt.Errorf("build websocket config failed: %w", err)
	}
	conn, err := websocket.DialConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("connect jupyter kernel channels failed: %w", err)
	}
	defer conn.Close()

	// 为本次执行请求生成 request id 与 session id
	reqID := uuid.NewString()
	session := uuid.NewString()
	now := time.Now()

	// 组装 execute_request 的 content
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

	// 发送 execute_request 并开始计时
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
		// 单独 goroutine 负责持续读取 WebSocket 并通过 channel 交给主循环处理
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

	// 主循环聚合 stdout stderr 并透传实时回调
	var stdout strings.Builder
	var stderr strings.Builder
	var execCount int64
	hadError := false
	replyStatus := ""
	gotReply := false
	gotIdle := false

	// Jupyter 的执行完成通常需要同时等到 execute_reply 与 status=idle
	for {
		select {
		case <-ctx.Done():
			// 上层取消或超时则主动关闭连接并返回当前已聚合的输出
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

			// 过滤与本次请求无关的消息
			if r.msg.ParentHeader.MessageID != reqID {
				continue
			}

			// 根据 message_type 处理 stdout stderr 状态与计数
			switch r.msg.Header.MessageType {
			case "stream":
				var sc streamContent
				if err := json.Unmarshal(r.msg.Content, &sc); err == nil {
					if sc.Name == "stderr" {
						stderr.WriteString(sc.Text)
						// stderr 按原样聚合并回调输出
						if hooks.OnStderr != nil && sc.Text != "" {
							hooks.OnStderr(sc.Text)
						}
					} else {
						stdout.WriteString(sc.Text)
						// stdout 按原样聚合并回调输出
						if hooks.OnStdout != nil && sc.Text != "" {
							hooks.OnStdout(sc.Text)
						}
					}
				}
			case "error":
				var ec errorContent
				if err := json.Unmarshal(r.msg.Content, &ec); err == nil {
					// error 事件通常携带 traceback 需要合并进 stderr
					hadError = true
					if len(ec.Traceback) > 0 {
						tb := strings.Join(ec.Traceback, "\n") + "\n"
						stderr.WriteString(tb)
						if hooks.OnStderr != nil {
							hooks.OnStderr(tb)
						}
					} else if ec.EValue != "" {
						line := ec.EValue + "\n"
						stderr.WriteString(line)
						if hooks.OnStderr != nil {
							hooks.OnStderr(line)
						}
					} else if ec.EName != "" {
						line := ec.EName + "\n"
						stderr.WriteString(line)
						if hooks.OnStderr != nil {
							hooks.OnStderr(line)
						}
					}
				}
			case "execute_input":
				var ic executeInputContent
				if err := json.Unmarshal(r.msg.Content, &ic); err == nil {
					// 从 execute_input 获取 execution_count
					if ic.ExecutionCount > 0 {
						execCount = ic.ExecutionCount
						if hooks.OnExecutionCount != nil {
							hooks.OnExecutionCount(execCount)
						}
					}
				}
			case "execute_reply":
				var rc executeReplyContent
				if err := json.Unmarshal(r.msg.Content, &rc); err == nil {
					// execute_reply 给出最终状态与可能的 traceback
					gotReply = true
					replyStatus = rc.Status
					if rc.ExecutionCount > 0 {
						execCount = rc.ExecutionCount
						if hooks.OnExecutionCount != nil {
							hooks.OnExecutionCount(execCount)
						}
					}
					if rc.Status == "error" {
						hadError = true
						if len(rc.Traceback) > 0 {
							tb := strings.Join(rc.Traceback, "\n") + "\n"
							stderr.WriteString(tb)
							if hooks.OnStderr != nil {
								hooks.OnStderr(tb)
							}
						} else if rc.EValue != "" {
							line := rc.EValue + "\n"
							stderr.WriteString(line)
							if hooks.OnStderr != nil {
								hooks.OnStderr(line)
							}
						}
					}
				}
			case "status":
				var st statusContent
				if err := json.Unmarshal(r.msg.Content, &st); err == nil {
					// status 事件会多次出现 以 idle 作为一次执行的收尾信号
					if hooks.OnStatus != nil && st.ExecutionState != "" {
						hooks.OnStatus(st.ExecutionState)
					}
					if st.ExecutionState == "idle" {
						gotIdle = true
					}
				}
			default:
			}

			// 同时看到 reply 与 idle 才认为本次执行已结束
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
