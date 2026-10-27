package handler

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"github.com/gin-gonic/gin"
)

const uploadTestID = "0123456789abcdef0123456789abcdef"

func uploadTestStore(t *testing.T) (*artifactUploadStore, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	root := t.TempDir()
	s := newArtifactUploadStore(filepath.Join(root, "sessions"), filepath.Join(root, "workspace", "upload"))
	s.apps = func() ([]model.Application, error) {
		return []model.Application{{AppName: "service", AppType: "java"}}, nil
	}
	s.persist = func(artifactVersionInput, []model.ArtifactVersionItem) error { return nil }
	return s, uploadTestRouter(s)
}

func uploadTestRouter(s *artifactUploadStore) *gin.Engine {
	h := &BatchDeployHandler{uploads: s}
	r := gin.New()
	r.Use(func(c *gin.Context) {
		owner := uint(7)
		if c.GetHeader("X-Test-Owner") == "other" {
			owner = 8
		}
		c.Set("userID", owner)
	})
	r.POST("/uploads", h.CreateArtifactUpload)
	r.GET("/uploads/:uploadID", h.GetArtifactUpload)
	r.PUT("/uploads/:uploadID/files/:fileIndex/chunks/:chunkIndex", h.PutArtifactUploadChunk)
	r.POST("/uploads/:uploadID/complete", h.CompleteArtifactUpload)
	r.DELETE("/uploads/:uploadID", h.CancelArtifactUpload)
	return r
}

func uploadRequest(r http.Handler, method, path string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	r.ServeHTTP(w, req)
	return w
}

func uploadSessionReply(t *testing.T, w *httptest.ResponseRecorder) artifactUploadSession {
	t.Helper()
	var reply struct {
		Code    int
		Message string
		Data    artifactUploadSession
	}
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || reply.Code != 0 {
		t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
	}
	return reply.Data
}

func createTestUpload(t *testing.T, r http.Handler, files []artifactUploadFile) artifactUploadSession {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"id": uploadTestID, "files": files})
	return uploadSessionReply(t, uploadRequest(r, "POST", "/uploads", body, nil))
}

func putTestChunk(r http.Handler, file, chunk int, data []byte) *httptest.ResponseRecorder {
	digest := sha256.Sum256(data)
	return uploadRequest(r, "PUT", fmt.Sprintf("/uploads/%s/files/%d/chunks/%d", uploadTestID, file, chunk), data,
		map[string]string{"X-Chunk-SHA256": hex.EncodeToString(digest[:])})
}

func waitTestUpload(t *testing.T, r http.Handler, status string) artifactUploadSession {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		session := uploadSessionReply(t, uploadRequest(r, "GET", "/uploads/"+uploadTestID, nil, nil))
		if session.Status == status {
			return session
		}
		if session.Status == "failed" && status != "failed" {
			t.Fatalf("finalize failed: %s", session.Error)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("upload did not reach %s", status)
	return artifactUploadSession{}
}

func TestArtifactChunkUploadOutOfOrderDurableResumeAndOwnership(t *testing.T) {
	s, r := uploadTestStore(t)
	data := bytes.Repeat([]byte("01234567"), int(artifactUploadChunkSize/8)+3)
	createTestUpload(t, r, []artifactUploadFile{{Path: "release/java/service.jar", Size: int64(len(data)), LastModified: 123}})
	var wg sync.WaitGroup
	for i := 1; i >= 0; i-- {
		wg.Add(1)
		go func(chunk int) {
			defer wg.Done()
			start := int64(chunk) * artifactUploadChunkSize
			w := putTestChunk(r, 0, chunk, data[start:min(int64(len(data)), start+artifactUploadChunkSize)])
			if w.Code != 200 {
				t.Errorf("chunk failed: %s", w.Body.String())
			}
		}(i)
	}
	wg.Wait()
	if w := putTestChunk(r, 0, 0, data[:artifactUploadChunkSize]); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	// A new store has no in-memory progress. It must still discover every receipt.
	restarted := newArtifactUploadStore(s.root, s.finalRoot)
	r = uploadTestRouter(restarted)
	session := createTestUpload(t, r, []artifactUploadFile{{Path: "release/java/service.jar", Size: int64(len(data)), LastModified: 123}})
	if session.ReceivedBytes != int64(len(data)) || len(session.Files[0].Chunks) != 2 || session.Files[0].Chunks[1] == "" {
		t.Fatalf("lost/duplicate receipt: %+v", session)
	}
	actual, err := os.ReadFile(filepath.Join(s.dir(uploadTestID), "files", "service.jar"))
	if err != nil || !bytes.Equal(actual, data) {
		t.Fatalf("out-of-order data mismatch: %v", err)
	}
	if w := uploadRequest(r, "GET", "/uploads/"+uploadTestID, nil, map[string]string{"X-Test-Owner": "other"}); w.Code != 403 {
		t.Fatal("another owner read upload")
	}
	if w := uploadRequest(r, "DELETE", "/uploads/"+uploadTestID, nil, map[string]string{"X-Test-Owner": "other"}); w.Code != 403 {
		t.Fatal("another owner canceled upload")
	}
	batches, err := listArtifactSourceBatches(filepath.Dir(s.finalRoot))
	if err != nil || len(batches) != 0 {
		t.Fatalf("incomplete upload exposed as deployable: %v %+v", err, batches)
	}
}

func TestArtifactChunkUploadRejectsBadDataWithoutConfirmingIt(t *testing.T) {
	s, r := uploadTestStore(t)
	createTestUpload(t, r, []artifactUploadFile{{Path: "service.jar", Size: 8}})
	for _, tc := range []struct {
		path string
		body []byte
		hash string
	}{
		{"/files/0/chunks/0", []byte("short"), strings.Repeat("0", 64)},
		{"/files/0/chunks/0", []byte("12345678"), strings.Repeat("0", 64)},
		{"/files/0/chunks/0", []byte("123456789"), strings.Repeat("0", 64)},
		{"/files/-1/chunks/0", []byte("12345678"), strings.Repeat("0", 64)},
		{"/files/0/chunks/2", []byte("12345678"), strings.Repeat("0", 64)},
	} {
		w := uploadRequest(r, "PUT", "/uploads/"+uploadTestID+tc.path, tc.body, map[string]string{"X-Chunk-SHA256": tc.hash})
		if w.Code != 400 {
			t.Fatalf("bad chunk accepted: %s", w.Body.String())
		}
	}
	session, err := s.load(uploadTestID, 7)
	if err != nil || session.ReceivedBytes != 0 {
		t.Fatal("bad data advanced progress")
	}
	if w := uploadRequest(r, "POST", "/uploads/"+uploadTestID+"/complete", nil, nil); w.Code != 409 {
		t.Fatal("incomplete batch finalized")
	}
	if w := putTestChunk(r, 0, 0, []byte("12345678")); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := putTestChunk(r, 0, 0, []byte("abcdefgh")); w.Code != 409 {
		t.Fatal("changed duplicate overwrote confirmed data")
	}
}

func TestArtifactChunkUploadLargeOffsetsAndCancel(t *testing.T) {
	s, r := uploadTestStore(t)
	size := int64(11<<30) + 123
	session := createTestUpload(t, r, []artifactUploadFile{{Path: "huge.zip", Size: size}})
	last := len(session.Files[0].Chunks) - 1
	if w := putTestChunk(r, 0, last, bytes.Repeat([]byte{1}, 123)); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	info, err := os.Stat(filepath.Join(s.dir(uploadTestID), "files", "huge.zip"))
	if err != nil || info.Size() != size {
		t.Fatalf("large offset truncated: %v", err)
	}
	session = uploadSessionReply(t, uploadRequest(r, "GET", "/uploads/"+uploadTestID, nil, nil))
	if session.ReceivedBytes != 123 || session.TotalBytes != size {
		t.Fatal("incorrect progress beyond 4 GB")
	}
	if w := uploadRequest(r, "DELETE", "/uploads/"+uploadTestID, nil, nil); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(s.dir(uploadTestID), "files")); !os.IsNotExist(err) {
		t.Fatal("canceled sparse file retained")
	}
	if w := putTestChunk(r, 0, last, bytes.Repeat([]byte{1}, 123)); w.Code == 200 {
		t.Fatal("canceled upload accepted chunk")
	}
}

func TestArtifactChunkUploadFinalizePublishesOnceAndReportsValidation(t *testing.T) {
	s, r := uploadTestStore(t)
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	part, _ := zw.Create("config.txt")
	_, _ = io.WriteString(part, "configuration")
	_ = zw.Close()
	data := archive.Bytes()
	session := createTestUpload(t, r, []artifactUploadFile{{Path: "service.jar", Size: int64(len(data))}, {Path: "broken.zip", Size: 3}})
	if w := putTestChunk(r, 0, 0, data); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := putTestChunk(r, 1, 0, []byte("bad")); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	before, _ := os.Stat(filepath.Join(s.dir(uploadTestID), "files", "service.jar"))
	entered, release := make(chan struct{}), make(chan struct{})
	var calls int
	s.persist = func(input artifactVersionInput, items []model.ArtifactVersionItem) error {
		calls++
		if len(items) != 2 || items[0].ValidateStatus != "valid" || items[1].ValidateStatus != "invalid" {
			return fmt.Errorf("archive validation was skipped")
		}
		close(entered)
		<-release
		return nil
	}
	processing := uploadSessionReply(t, uploadRequest(r, "POST", "/uploads/"+uploadTestID+"/complete", nil, nil))
	if processing.Status != "processing" {
		t.Fatal("complete request blocked instead of starting background validation")
	}
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("validation did not start")
	}
	progress := uploadSessionReply(t, uploadRequest(r, "GET", "/uploads/"+uploadTestID, nil, nil))
	if progress.CompletedFiles != 2 || progress.CurrentFile == "" {
		t.Fatalf("no validation progress: %+v", progress)
	}
	batches, err := scanSourceBatchRoot(s.finalRoot, "upload")
	if err != nil || len(batches) != 0 {
		t.Fatal("uncommitted batch exposed by scan")
	}
	if w := uploadRequest(r, "DELETE", "/uploads/"+uploadTestID, nil, nil); w.Code != 409 {
		t.Fatal("canceled active validation")
	}
	close(release)
	completed := waitTestUpload(t, r, "completed")
	if completed.Result.Count != 1 || len(completed.Result.Failed) != 1 || completed.Result.Failed[0].FileName != "broken.zip" {
		t.Fatalf("invalid result: %+v", completed.Result)
	}
	after, _ := os.Stat(filepath.Join(s.finalRoot, session.BatchID, "service.jar"))
	if !os.SameFile(before, after) {
		t.Fatal("upload was copied instead of atomically renamed")
	}
	_ = uploadSessionReply(t, uploadRequest(r, "POST", "/uploads/"+uploadTestID+"/complete", nil, nil))
	if calls != 1 {
		t.Fatal("duplicate completion generated another version")
	}
}

func TestArtifactChunkUploadRecoversAfterPublishedMetadataFailure(t *testing.T) {
	s, r := uploadTestStore(t)
	createTestUpload(t, r, []artifactUploadFile{{Path: "broken.zip", Size: 3}})
	if w := putTestChunk(r, 0, 0, []byte("bad")); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	s.persist = func(artifactVersionInput, []model.ArtifactVersionItem) error {
		return fmt.Errorf("database unavailable")
	}
	_ = uploadSessionReply(t, uploadRequest(r, "POST", "/uploads/"+uploadTestID+"/complete", nil, nil))
	failed := waitTestUpload(t, r, "failed")
	if !strings.Contains(failed.Error, "database unavailable") {
		t.Fatal(failed.Error)
	}
	// Simulate process restart with the staging directory already renamed.
	restarted := newArtifactUploadStore(s.root, s.finalRoot)
	restarted.apps, restarted.persist = s.apps, func(artifactVersionInput, []model.ArtifactVersionItem) error { return nil }
	r = uploadTestRouter(restarted)
	_ = uploadSessionReply(t, uploadRequest(r, "POST", "/uploads/"+uploadTestID+"/complete", nil, nil))
	completed := waitTestUpload(t, r, "completed")
	if completed.Result.BatchID != failed.BatchID || completed.ReceivedBytes != 3 {
		t.Fatal("recovery lost upload identity or bytes")
	}
}

func TestArtifactChunkUploadManifestIsIdempotentAndRejectsDuplicateNames(t *testing.T) {
	_, r := uploadTestStore(t)
	first := createTestUpload(t, r, []artifactUploadFile{{Path: "folder/service.jar", Size: 12}})
	second := createTestUpload(t, r, []artifactUploadFile{{Path: "folder/service.jar", Size: 12}})
	if first.BatchID != second.BatchID {
		t.Fatal("create retry allocated another batch")
	}
	for _, files := range [][]artifactUploadFile{
		{{Path: "folder/service.jar", Size: 13}},
		{{Path: "a/service.jar", Size: 12}, {Path: "b/SERVICE.jar", Size: 12}},
		{{Path: "a/file.exe", Size: 1}},
	} {
		body, _ := json.Marshal(map[string]any{"id": uploadTestID, "files": files})
		if w := uploadRequest(r, "POST", "/uploads", body, nil); w.Code == 200 {
			t.Fatal("changed/invalid manifest accepted")
		}
	}
}

func TestArtifactChunkUploadBrokenHTTPConnectionCanResume(t *testing.T) {
	_, r := uploadTestStore(t)
	createTestUpload(t, r, []artifactUploadFile{{Path: "service.jar", Size: 8}})
	server := httptest.NewServer(r)
	defer server.Close()
	connection, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("12345678"))
	_, err = fmt.Fprintf(connection, "PUT /uploads/%s/files/0/chunks/0 HTTP/1.1\r\nHost: test\r\nContent-Length: 8\r\nX-Chunk-SHA256: %x\r\n\r\n1234", uploadTestID, digest)
	if err != nil {
		t.Fatal(err)
	}
	_ = connection.Close() // client disappears halfway through the request body
	session := uploadSessionReply(t, uploadRequest(r, "GET", "/uploads/"+uploadTestID, nil, nil))
	if session.ReceivedBytes != 0 {
		t.Fatal("broken connection acknowledged partial bytes")
	}
	req, _ := http.NewRequest("PUT", server.URL+"/uploads/"+uploadTestID+"/files/0/chunks/0", strings.NewReader("12345678"))
	req.Header.Set("X-Chunk-SHA256", fmt.Sprintf("%x", digest))
	res, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("retry HTTP status=%d", res.StatusCode)
	}
	session = uploadSessionReply(t, uploadRequest(r, "GET", "/uploads/"+uploadTestID, nil, nil))
	if session.ReceivedBytes != 8 {
		t.Fatal("complete retry not acknowledged")
	}
}

func TestArtifactChunkUploadRestartsInterruptedValidation(t *testing.T) {
	s, r := uploadTestStore(t)
	createTestUpload(t, r, []artifactUploadFile{{Path: "broken.zip", Size: 3}})
	if w := putTestChunk(r, 0, 0, []byte("bad")); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	session, _ := s.load(uploadTestID, 7)
	session.Status = "processing"
	if err := s.save(session); err != nil {
		t.Fatal(err)
	}
	restarted := newArtifactUploadStore(s.root, s.finalRoot)
	restarted.apps, restarted.persist = s.apps, s.persist
	r = uploadTestRouter(restarted)
	waitTestUpload(t, r, "completed") // GET restarts a persisted processing session
}

func TestArtifactChunkUploadDatabaseVersionMatchesCommittedBytes(t *testing.T) {
	setupArtifactVersionTestDB(t) // explicitly configured disposable PostgreSQL only
	root := t.TempDir()
	s := newArtifactUploadStore(filepath.Join(root, "sessions"), filepath.Join(root, "upload"))
	r := uploadTestRouter(s)
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	f, _ := zw.Create("application.properties")
	_, _ = io.WriteString(f, "test=true")
	_ = zw.Close()
	data := archive.Bytes()
	session := createTestUpload(t, r, []artifactUploadFile{{Path: "release/his-gateway.jar", Size: int64(len(data))}})
	if w := putTestChunk(r, 0, 0, data); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	_ = uploadSessionReply(t, uploadRequest(r, "POST", "/uploads/"+uploadTestID+"/complete", nil, nil))
	completed := waitTestUpload(t, r, "completed")
	if completed.Result.Count != 1 {
		t.Fatalf("upload result: %+v", completed)
	}
	var version model.ArtifactVersion
	var item model.ArtifactVersionItem
	if err := repository.DB.Where("version_no = ?", session.BatchID).First(&version).Error; err != nil {
		t.Fatal(err)
	}
	if err := repository.DB.Where("version_no = ?", session.BatchID).First(&item).Error; err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	if version.TargetPath != filepath.Join(s.finalRoot, session.BatchID) || version.DeployableCount != 1 || item.SHA256 != fmt.Sprintf("%x", digest) || item.FileSizeBytes != int64(len(data)) || !item.Deployable {
		t.Fatalf("persisted version does not match upload: %+v %+v", version, item)
	}
	_ = uploadSessionReply(t, uploadRequest(r, "POST", "/uploads/"+uploadTestID+"/complete", nil, nil))
	var same model.ArtifactVersionItem
	if err := repository.DB.Where("version_no = ?", session.BatchID).First(&same).Error; err != nil || same.ID != item.ID {
		t.Fatal("idempotent completion replaced persisted item")
	}
}
