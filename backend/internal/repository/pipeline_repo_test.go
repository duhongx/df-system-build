package repository

import (
	"testing"

	"df-build-server/internal/model"
)

func TestPipelineListAppliesStatusFilterToReturnedRows(t *testing.T) {
	setupRepositoryTestDB(t)

	pipelines := []model.Pipeline{
		{PipelineNo: "success-0001", AppName: "app-success", Status: "SUCCESS", TriggerUser: "admin"},
		{PipelineNo: "ready-0001", AppName: "app-ready", Status: "IMAGE_READY", TriggerUser: "admin", BatchID: "batch-cutover", DeployMode: "manual"},
	}
	for _, pipeline := range pipelines {
		if err := DB.Create(&pipeline).Error; err != nil {
			t.Fatalf("insert pipeline %s: %v", pipeline.PipelineNo, err)
		}
	}

	list, total, err := NewPipelineRepo().List(PipelineListParams{Status: "IMAGE_READY", Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("list pipelines: %v", err)
	}
	if total != 1 {
		t.Fatalf("total = %d, want 1", total)
	}
	if len(list) != 1 {
		t.Fatalf("len(list) = %d, want 1; list=%+v", len(list), list)
	}
	if list[0].Status != "IMAGE_READY" || list[0].PipelineNo != "ready-0001" {
		t.Fatalf("returned pipeline = %+v, want ready-0001 IMAGE_READY", list[0])
	}
}
