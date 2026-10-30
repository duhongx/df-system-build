package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"df-build-server/internal/inspection"
	"df-build-server/internal/model"
	"df-build-server/pkg/crypto"
)

func TestConfiguredMinIOAPISavesAndCollectsWithoutAnyManagedHost(t *testing.T) {
	s, db := dailyFixture(t)
	var calls atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" || r.URL.Path != "/minio/admin/v3/info" || !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
			t.Error("collector did not use signed admin API")
		}
		fmt.Fprint(w, `{"mode":"online","backend":{"backendType":"Erasure","onlineDisks":1,"offlineDisks":0},"servers":[{"endpoint":"192.0.2.10:9000","state":"online","drives":[{"path":"/data","state":"ok","totalspace":100,"usedspace":10,"availspace":90}]}]}`)
	}))
	defer api.Close()
	request := ComponentRequest{Kind: "minio", Name: "API MinIO", Endpoint: strings.TrimPrefix(api.URL, "http://"), Username: "fixture-key", Secret: "fixture-secret", Enabled: true}
	row, err := s.SaveComponent(context.Background(), request)
	if err != nil || row.ServerID != 0 || row.Endpoint != api.URL {
		t.Fatalf("API connection still depends on SSH host: %+v %v", row, err)
	}
	plain, err := crypto.Decrypt(row.SecretEncrypted)
	if err != nil || plain != request.Secret || row.SecretEncrypted == request.Secret {
		t.Fatal("credentials not encrypted")
	}
	encoded, _ := json.Marshal(row)
	if strings.Contains(string(encoded), request.Secret) || strings.Contains(string(encoded), row.SecretEncrypted) {
		t.Fatal("component response exposes credentials")
	}
	request.ID, request.Secret = row.ID, ""
	row, err = s.SaveComponent(context.Background(), request)
	if err != nil || row.Revision != 2 {
		t.Fatal("editing without a host or changing secret failed", err)
	}
	if err := s.syncMonitoringSources(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.scheduleMonitoring(context.Background())
	waitDaily(t, db, row.ID, func(v model.MonitoringState) bool { return v.LastSuccessAt != nil && v.LeaseToken == "" })
	view, err := s.MonitoringDetail(context.Background(), "component", row.ID)
	if err != nil || view.Result == nil || view.Indicators["onlineDisks"] != 1 || view.LastError != "" {
		t.Fatalf("API monitoring unavailable: %+v %v", view, err)
	}
	// No managed hosts or SSH client exist in this fixture. File supplement
	// defaults must also be readable without attempting an SSH connection.
	checks := s.collectMinIOSupplements(context.Background(), *row)
	if len(checks) != 2 || checks[0].Status == "UNKNOWN" || checks[1].Status == "UNKNOWN" {
		t.Fatal("unconfigured optional files triggered an SSH requirement")
	}
	if calls.Load() < 1 {
		t.Fatal("background monitoring did not call API")
	}
	request.ClearSecret = true
	if _, err := s.SaveComponent(context.Background(), request); err == nil {
		t.Fatal("accepted credential removal for API monitoring")
	}
}

func TestConfiguredMinIOLegacyConnectionCanMigrateToAPI(t *testing.T) {
	s, db := dailyFixture(t)
	legacy := model.ComponentConnection{Kind: "minio", Name: "legacy", ServerID: 0, OptionsJSON: `{"alias":"prod"}`, Revision: 1, Enabled: true}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	row, err := s.SaveComponent(context.Background(), ComponentRequest{ID: legacy.ID, Kind: "minio", Name: legacy.Name, Endpoint: "192.168.199.102:9000", Username: "access", Secret: "secret", Enabled: true})
	if err != nil || row.ServerID != 0 || row.Endpoint != "http://192.168.199.102:9000" {
		t.Fatal("legacy connection could not migrate without SSH", err)
	}
	if _, err := s.SaveComponent(context.Background(), ComponentRequest{Kind: "minio", Name: "new"}); err == nil {
		t.Fatal("new connection accepted missing API address")
	}
	if _, err := s.SaveComponent(context.Background(), ComponentRequest{Kind: "minio", Name: "new", Endpoint: row.Endpoint, Username: "access", Secret: "secret", Options: ComponentOptions{DailyFile: "/tmp/daily"}}); err == nil || !strings.Contains(err.Error(), "每日记录") {
		t.Fatal("host file path without a host was not explained", err)
	}
}

func TestMinIOMemberAddressesAcceptsAPIAndLegacyNodes(t *testing.T) {
	checks := []inspection.Observation{{Code: "cloudhis-3", Tables: []inspection.Table{
		{Columns: []string{"节点地址", "状态"}, Rows: [][]string{{"http://192.0.2.10:9000", "online"}, {"192.0.2.11:9000", "online"}}},
		{Columns: []string{"项目", "信息"}, Rows: [][]string{{"节点", "● 192.0.2.10:9000"}, {"节点", "● 192.0.2.12:9000"}}},
	}}}
	got := minioMemberAddresses(checks)
	if strings.Join(got, ",") != "192.0.2.10,192.0.2.11,192.0.2.12" {
		t.Fatalf("actual MinIO nodes not matched: %v", got)
	}
}
