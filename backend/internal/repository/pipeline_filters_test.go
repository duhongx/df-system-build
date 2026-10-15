package repository

import (
	"df-build-server/internal/model"
	"df-build-server/internal/testutil"
	"testing"
)

func TestPipelineSearchAndTypeApplyBeforePagination(t *testing.T) {
	DB = testutil.OpenGormPostgres(t)
	if err := DB.AutoMigrate(&model.Pipeline{}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []model.Pipeline{{PipelineNo: "java-match-1", AppName: "match-service", AppType: "java", Status: "SUCCESS"}, {PipelineNo: "java-match-2", AppName: "match-service", AppType: "java", Status: "FAILED"}, {PipelineNo: "web-match-1", AppName: "match-web", AppType: "vue", Status: "SUCCESS"}, {PipelineNo: "other-1", AppName: "other", AppType: "java", Status: "SUCCESS"}} {
		if err := DB.Create(&p).Error; err != nil {
			t.Fatal(err)
		}
	}
	repo := NewPipelineRepo()
	params := PipelineListParams{Page: 1, PageSize: 1, Search: "MATCH", AppType: "java"}
	first, total, err := repo.List(params)
	if err != nil || total != 2 || len(first) != 1 {
		t.Fatalf("first page: %v %d %v", first, total, err)
	}
	params.Page = 2
	second, total, err := repo.List(params)
	if err != nil || total != 2 || len(second) != 1 || second[0].ID == first[0].ID {
		t.Fatalf("second page: %v %d %v", second, total, err)
	}
	params.Page = 1
	params.Status = "FAILED"
	rows, total, err := repo.List(params)
	if err != nil || total != 1 || len(rows) != 1 || rows[0].Status != "FAILED" {
		t.Fatalf("combined filters: %v %d %v", rows, total, err)
	}
}

func TestCancelActivePreservesCompletedHistory(t *testing.T) {
	DB = testutil.OpenGormPostgres(t)
	if err := DB.AutoMigrate(&model.Pipeline{}); err != nil {
		t.Fatal(err)
	}
	repo := NewPipelineRepo()
	for _, status := range []string{"SUCCESS", "FAILED", "CANCELED", "PENDING", "RUNNING", "DEPLOYING", "IMAGE_READY"} {
		p := model.Pipeline{PipelineNo: "cancel-" + status, Status: status}
		if err := DB.Create(&p).Error; err != nil {
			t.Fatal(err)
		}
		changed, err := repo.CancelActive(p.ID)
		active := status == "PENDING" || status == "RUNNING" || status == "DEPLOYING" || status == "IMAGE_READY"
		if err != nil || changed != active {
			t.Fatalf("cancel %s: %t %v", status, changed, err)
		}
		var saved model.Pipeline
		if err := DB.First(&saved, p.ID).Error; err != nil {
			t.Fatal(err)
		}
		expected := status
		if active {
			expected = "CANCELED"
		}
		if saved.Status != expected {
			t.Fatalf("%s changed to %s", status, saved.Status)
		}
	}
}
