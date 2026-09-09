package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
)

var pushedDigest = regexp.MustCompile(`(?:^|\s)digest:\s*(sha256:[a-f0-9]{64})(?:\s|$)`)

// Both a clean response stream and an independently confirmed result are required.
func dockerJSONStream(parent context.Context, body io.ReadCloser, verify func(context.Context, string) error) *CmdReader {
	ctx, cancel := context.WithCancel(parent)
	pr, pw := io.Pipe()
	done := make(chan struct{})
	r := &CmdReader{reader: pr, streamDone: done, stopStream: func() { cancel(); _ = body.Close() }}
	go func() {
		defer close(done)
		defer pw.Close()
		defer body.Close()
		defer cancel()
		fail := func(err error) {
			r.streamErr = err
			r.hasError.Store(true)
			_, _ = fmt.Fprintf(pw, "ERROR: %v\n", err)
			_ = pw.CloseWithError(err)
		}
		decoder := json.NewDecoder(body)
		digest := ""
		for {
			var msg struct {
				Stream      string `json:"stream"`
				Status      string `json:"status"`
				ID          string `json:"id"`
				Progress    string `json:"progress"`
				Error       string `json:"error"`
				ErrorDetail struct {
					Message string `json:"message"`
				} `json:"errorDetail"`
				Aux struct {
					Digest string `json:"Digest"`
				} `json:"aux"`
			}
			err := decoder.Decode(&msg)
			if ctx.Err() != nil {
				fail(fmt.Errorf("Docker 操作已取消或超时: %w", ctx.Err()))
				return
			}
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				fail(fmt.Errorf("读取 Docker 响应失败: %w", err))
				return
			}
			if msg.Error != "" {
				fail(errors.New(msg.Error))
				return
			}
			if msg.ErrorDetail.Message != "" {
				fail(errors.New(msg.ErrorDetail.Message))
				return
			}
			if msg.Aux.Digest != "" {
				digest = msg.Aux.Digest
			}
			if match := pushedDigest.FindStringSubmatch(msg.Status); len(match) == 2 {
				digest = match[1]
			}
			line := msg.Stream
			if line == "" && msg.Status != "" {
				line = fmt.Sprintf("%s %s %s\n", msg.ID, msg.Status, msg.Progress)
			}
			if line != "" {
				if _, err := io.WriteString(pw, line); err != nil {
					r.streamErr = fmt.Errorf("输出 Docker 日志失败: %w", err)
					return
				}
			}
		}
		if err := verify(ctx, digest); err != nil {
			fail(err)
			return
		}
	}()
	return r
}
