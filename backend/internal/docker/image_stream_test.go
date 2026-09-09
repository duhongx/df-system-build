package docker

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"df-build-server/pkg/logger"
	"github.com/docker/docker/client"
	"go.uber.org/zap"
)

func TestImageOperationsRequireCompleteResponseAndConfirmedResult(t *testing.T) {
	logger.Log = zap.NewNop().Sugar()
	digest := "sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		name, operation, body              string
		missingResult, mismatch, wantError bool
	}{
		{"build truncated", "build", `{"stream":"building\n"}` + "\n" + `{"stream":`, false, false, true},
		{"push truncated", "push", `{"status":"Pushing"}` + "\n" + `{"status":`, false, false, true},
		{"push incomplete EOF", "push", `{"status":"Pushing"}`, false, false, true},
		{"push detailed error", "push", `{"errorDetail":{"message":"registry denied"}}`, false, false, true},
		{"build result missing", "build", `{"stream":"finished\n"}`, true, false, true},
		{"build confirmed", "build", `{"stream":"finished\n"}`, false, false, false},
		{"push result missing", "push", `{"aux":{"Digest":"` + digest + `"}}`, true, false, true},
		{"push result mismatch", "push", `{"aux":{"Digest":"` + digest + `"}}`, false, true, true},
		{"push confirmed", "push", `{"status":"v1: digest: ` + digest + ` size: 10"}`, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "application/json")
				if r.Method == "POST" {
					_, _ = io.WriteString(w, tc.body)
					return
				}
				if tc.missingResult {
					w.WriteHeader(404)
					_, _ = io.WriteString(w, `{"message":"result missing"}`)
					return
				}
				if strings.Contains(r.URL.Path, "/distribution/") {
					actual := digest
					if tc.mismatch {
						actual = "sha256:" + strings.Repeat("b", 64)
					}
					_, _ = io.WriteString(w, `{"Descriptor":{"digest":"`+actual+`"}}`)
				} else {
					_, _ = io.WriteString(w, `{"Id":"`+digest+`"}`)
				}
			}))
			defer srv.Close()
			sdk, err := client.NewClientWithOpts(client.WithHost(srv.URL), client.WithVersion("1.43"))
			if err != nil {
				t.Fatal(err)
			}
			defer sdk.Close()
			c := &Client{cli: sdk}
			var reader *CmdReader
			if tc.operation == "build" {
				reader, err = c.BuildImageWithAuth(context.Background(), t.TempDir(), nil, "example/app:v1")
			} else {
				reader, err = c.PushImageWithAuth(context.Background(), "example/app:v1", "e30=")
			}
			if err != nil {
				t.Fatal(err)
			}
			readErr := ReadLines(reader, func(string) {})
			closeErr := reader.Close()
			failed := readErr != nil || closeErr != nil || reader.ExitCode != 0
			if failed != tc.wantError {
				t.Fatalf("read=%v close=%v code=%d", readErr, closeErr, reader.ExitCode)
			}
			if tc.wantError && closeErr == nil {
				t.Fatal("operation error missing from Close")
			}
		})
	}
}

func TestClosingDockerResponseStopsBlockedRead(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	r := dockerJSONStream(context.Background(), pr, func(context.Context, string) error { t.Error("verified incomplete stream"); return nil })
	done := make(chan error, 1)
	go func() { done <- r.Close() }()
	select {
	case err := <-done:
		if err == nil || r.ExitCode == 0 {
			t.Fatalf("early close reported success: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close blocked on Docker response")
	}
}

func TestReadLinesReportsOversizedLogFailure(t *testing.T) {
	if err := ReadLines(strings.NewReader(strings.Repeat("x", 2*1024*1024)), func(string) {}); err == nil {
		t.Fatal("oversized Docker log silently ignored")
	}
}
