package service

import (
	"bytes"
	"context"
	"df-build-server/internal/inspection"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestConfiguredReportLayoutWithArchivedEvidence(t *testing.T) {
	source := os.Getenv("DFOPS_CONFIGURED_REPORT_SOURCE")
	dest := os.Getenv("DFOPS_CONFIGURED_REPORT_FIXTURE")
	if source == "" || dest == "" {
		t.Skip("optional historical-evidence layout fixture")
	}
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	var doc inspection.Report
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	doc.Mode = "configured"
	doc.Name = "系统巡检报告 · 历史证据版式验证"
	doc.GeneratedAt = time.Now()
	for i := range doc.Objects {
		o := &doc.Objects[i]
		o.CheckCodes = nil
		for _, obs := range o.Observations {
			o.CheckCodes = append(o.CheckCodes, obs.Code)
		}
		o.Supplement = nil
		if len(o.Tables) > 2 {
			o.Tables = o.Tables[:2]
		}
	}
	doc = inspection.Build(doc)
	r := inspectionReport{Snapshot: &doc, Generated: time.Now().Format("2006-01-02 15:04:05")}
	var html bytes.Buffer
	if err := inspectionHTML.Execute(&html, r); err != nil {
		t.Fatal(err)
	}
	pdf, err := renderInspectionPDF(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dest, 0700); err != nil {
		t.Fatal(err)
	}
	for name, b := range map[string][]byte{"layout-preview.html": html.Bytes(), "layout-preview.pdf": pdf} {
		if err := os.WriteFile(filepath.Join(dest, name), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if bytes.Contains(html.Bytes(), []byte("39 项检查目录")) {
		t.Fatal("legacy unrelated checklist rendered")
	}
}
