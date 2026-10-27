package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"df-build-server/internal/model"
	"df-build-server/internal/opsprobe"
	"github.com/signintech/gopdf"
	"golang.org/x/image/font/gofont/goregular"
)

type inspectionReportTarget struct {
	Target model.InspectionTargetRun
	Result opsprobe.JobResult
}
type inspectionReport struct {
	Run       model.InspectionRun
	Targets   []inspectionReportTarget
	Generated string
}

var inspectionHTML = template.Must(template.New("report").Funcs(template.FuncMap{"status": inspectionStatusLabel, "time": func(t time.Time) string { return t.In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04:05") }}).Parse(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>{{.Run.Name}} - DFOPS 巡检报告</title><style>
*{box-sizing:border-box}body{font-family:Arial,"Noto Sans CJK SC","Droid Sans Fallback","Microsoft YaHei",sans-serif;color:#17233d;background:#f4f6fa;margin:0;padding:32px}main{max-width:1100px;margin:auto;background:white;padding:36px;border:1px solid #e3e8ef}h1{font-size:27px;margin:12px 0}h2{font-size:20px;margin:28px 0 12px}h3{font-size:15px;margin:0 0 8px}.brand{color:#52657e;letter-spacing:2px;font-size:12px}.meta{color:#52657e;line-height:1.8}.summary{display:flex;gap:12px;margin:22px 0}.summary>div{flex:1;background:#f5f7fb;padding:14px;border:1px solid #e2e8f0}.summary strong{display:block;font-size:23px;margin-top:8px}.target{border-top:2px solid #d8e2ee;padding-top:14px;margin-top:26px}.check{padding:14px 0;border-bottom:1px solid #e2e8f0;break-inside:avoid}.status{display:inline-block;padding:3px 8px;border-radius:4px;font-size:12px;background:#eaf2fd;color:#315a8d}.WARNING{background:#fff5dd;color:#805400}.CRITICAL,.FAILED{background:#ffebeb;color:#a32121}.UNKNOWN,.CANCELED{background:#eef0f3;color:#515e70}.NORMAL,.COMPLETED{background:#e8f6ee;color:#206644}pre{white-space:pre-wrap;overflow-wrap:anywhere;word-break:break-word;background:#f6f8fb;border:1px solid #e3e8ef;padding:12px;font-size:12px;line-height:1.6}p{line-height:1.7;margin:7px 0}.foot{margin-top:28px;color:#65758b;font-size:12px}@page{size:A4;margin:16mm}@media print{body{background:white;padding:0}main{border:0;padding:0;max-width:none}.summary{display:flex}h2{break-after:avoid}}
</style></head><body><main><div class="brand">DFOPS · 运维巡检</div><h1>{{.Run.Name}}</h1><div class="meta">任务 #{{.Run.ID}} · {{status .Run.Status}} · 发起人 {{.Run.Operator}}<br>生成时间 {{.Generated}} · 执行器版本 {{range .Targets}}{{if .Result.Version}}{{.Result.Version}} {{end}}{{end}}</div><div class="summary"><div>巡检对象<strong>{{.Run.TotalTargets}}</strong></div><div>告警检查项<strong>{{.Run.WarningCount}}</strong></div><div>严重检查项<strong>{{.Run.CriticalCount}}</strong></div><div>采集异常<strong>{{.Run.ErrorCount}}</strong></div></div><p>本报告保留本次检查快照。CPU 等指标为执行时采样；历史日志统计仅涵盖平台已采集记录。采集失败、权限不足和未配置项目以未知状态列出。</p>{{range .Targets}}<section class="target"><h2>{{.Target.Name}} <span class="status {{.Target.Status}}">{{status .Target.Status}}</span></h2><div class="meta">{{.Target.Type}} · {{.Target.Endpoint}} · 执行主机 {{.Target.ServerEndpoint}}</div>{{if .Target.Message}}<p>{{.Target.Message}}</p>{{end}}{{if .Result.Error}}<pre>{{.Result.Error}}</pre>{{end}}{{range .Result.Checks}}<div class="check"><h3><span class="status {{.Status}}">{{status .Status}}</span> {{.Name}}</h3>{{if .Value}}<p>{{.Value}}</p>{{end}}{{if .Evidence}}<pre>{{.Evidence}}</pre>{{end}}{{if .Error}}<pre>{{.Error}}</pre>{{end}}{{if .Advice}}<p>建议：{{.Advice}}</p>{{end}}</div>{{else}}<p>本目标未取得可用检查结果，请结合执行状态和错误信息核实。</p>{{end}}</section>{{end}}<div class="foot">DFOPS 运维巡检报告 · 任务 #{{.Run.ID}} · HTML 与 PDF 使用同一份已持久化检查结果</div></main></body></html>`))

func inspectionStatusLabel(s string) string {
	if v := map[string]string{"NORMAL": "正常", "WARNING": "告警", "CRITICAL": "严重", "UNKNOWN": "未知／待核实", "QUEUED": "等待中", "RUNNING": "执行中", "COMPLETED": "已完成", "PARTIAL": "部分完成", "FAILED": "失败", "CANCELED": "已取消"}[s]; v != "" {
		return v
	}
	return s
}
func (s *OperationsService) ReportPath(id uint, format string) (string, error) {
	if format != "html" && format != "pdf" {
		return "", fmt.Errorf("不支持的报告格式")
	}
	return filepath.Join(s.workspace, "reports", fmt.Sprintf("run-%d", id), "report."+format), nil
}
func atomicReportFile(name string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(name), ".report-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), name)
}
func (s *OperationsService) GenerateInspectionReports(ctx context.Context, id uint) error {
	select {
	case s.reportSlots <- struct{}{}:
		defer func() { <-s.reportSlots }()
	case <-ctx.Done():
		return s.reportFailure(id, "HTML", ctx.Err())
	}
	var run model.InspectionRun
	if err := s.db.First(&run, id).Error; err != nil {
		return err
	}
	var targets []model.InspectionTargetRun
	if err := s.db.Where("run_id=?", id).Order("id").Find(&targets).Error; err != nil {
		return err
	}
	report := inspectionReport{Run: run, Generated: time.Now().In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04:05")}
	for _, t := range targets {
		var result opsprobe.JobResult
		if t.ResultJSON != "" {
			if err := json.Unmarshal([]byte(t.ResultJSON), &result); err != nil {
				return err
			}
		}
		report.Targets = append(report.Targets, inspectionReportTarget{t, result})
	}
	// The report should describe the collected result even while the owning job
	// is still in its report-generation stage.
	report.Run.Status = "COMPLETED"
	if run.ErrorCount > 0 {
		report.Run.Status = "PARTIAL"
	}
	if run.CancelRequested {
		report.Run.Status = "CANCELED"
	}
	if err := s.db.Model(&run).Updates(map[string]any{"html_status": "GENERATING", "pdf_status": "PENDING", "report_error": "", "message": "生成 HTML 报告"}).Error; err != nil {
		return err
	}
	var html bytes.Buffer
	if err := inspectionHTML.Execute(&html, report); err != nil {
		return s.reportFailure(id, "HTML", err)
	}
	htmlPath, _ := s.ReportPath(id, "html")
	if err := atomicReportFile(htmlPath, html.Bytes()); err != nil {
		return s.reportFailure(id, "HTML", err)
	}
	if err := s.db.Model(&run).Updates(map[string]any{"html_status": "READY", "pdf_status": "GENERATING", "message": "HTML 已生成，正在生成 PDF"}).Error; err != nil {
		return err
	}
	pdf, err := renderInspectionPDF(ctx, report)
	if err != nil {
		return s.reportFailure(id, "PDF", err)
	}
	pdfPath, _ := s.ReportPath(id, "pdf")
	if err := atomicReportFile(pdfPath, pdf); err != nil {
		return s.reportFailure(id, "PDF", err)
	}
	return s.db.Model(&run).Updates(map[string]any{"pdf_status": "READY", "report_error": "", "message": "巡检及 HTML／PDF 报告生成完成"}).Error
}
func (s *OperationsService) reportFailure(id uint, format string, err error) error {
	message := format + " 报告生成失败: " + err.Error() + "；已采集结果保留，可单独重新生成报告"
	field := "pdf_status"
	if format == "HTML" {
		field = "html_status"
	}
	s.db.Model(&model.InspectionRun{}).Where("id=?", id).Updates(map[string]any{field: "FAILED", "report_error": message, "message": message})
	return fmt.Errorf("%s", message)
}
func reportFont() string {
	candidates := []string{os.Getenv("DFOPS_REPORT_FONT"), "/usr/share/fonts/google-droid-fonts/DroidSansFallback.ttf", "/usr/share/fonts/truetype/droid/DroidSansFallbackFull.ttf", "/usr/share/fonts/truetype/wqy/wqy-microhei.ttf"}
	for _, name := range candidates {
		if name != "" {
			if st, err := os.Stat(name); err == nil && !st.IsDir() {
				return name
			}
		}
	}
	return ""
}
func renderInspectionPDF(ctx context.Context, r inspectionReport) ([]byte, error) {
	font := reportFont()
	if font == "" {
		return nil, fmt.Errorf("未找到中文 TrueType 字体，请安装 Droid Sans Fallback 或通过 DFOPS_REPORT_FONT 指定字体路径")
	}
	pdf := &gopdf.GoPdf{}
	pdf.Start(gopdf.Config{PageSize: *gopdf.PageSizeA4})
	missing := map[rune]bool{}
	fontOptions := gopdf.TtfOption{OnGlyphNotFound: func(r rune) { missing[r] = true }}
	if err := pdf.AddTTFFontWithOption("report", font, fontOptions); err != nil {
		return nil, fmt.Errorf("加载中文字体失败: %w", err)
	}
	if err := pdf.AddTTFFontDataWithOption("latin", goregular.TTF, fontOptions); err != nil {
		return nil, err
	}
	fontFor := func(r rune) string {
		if r < 0x3000 {
			return "latin"
		}
		return "report"
	}
	draw := func(text string, x, y, size float64) error {
		var segment strings.Builder
		family := ""
		flush := func() error {
			if segment.Len() == 0 {
				return nil
			}
			if err := pdf.SetFont(family, "", size); err != nil {
				return err
			}
			s := segment.String()
			w, err := pdf.MeasureTextWidth(s)
			if err != nil {
				return err
			}
			pdf.SetXY(x, y+size)
			if err := pdf.Text(s); err != nil {
				return err
			}
			x += w
			segment.Reset()
			return nil
		}
		for _, r := range text {
			f := fontFor(r)
			if family != "" && family != f {
				if err := flush(); err != nil {
					return err
				}
			}
			family = f
			segment.WriteRune(r)
		}
		return flush()
	}
	width := gopdf.PageSizeA4.W
	bottom := gopdf.PageSizeA4.H - 48
	y := 0.0
	page := 0
	fontSize := 10.0
	cache := map[rune]float64{}
	newPage := func() {
		pdf.AddPage()
		page++
		pdf.SetFont("report", "", 9)
		pdf.SetTextColor(99, 115, 137)
		pdf.SetXY(38, 22)
		draw(fmt.Sprintf("DFOPS / 运维巡检 / #%d", r.Run.ID), 38, 22, 9)
		pdf.SetXY(38, gopdf.PageSizeA4.H-27)
		draw(fmt.Sprintf("第 %d 页   %s", page, r.Generated), 38, gopdf.PageSizeA4.H-32, 9)
		y = 55
		pdf.SetFont("report", "", fontSize)
		pdf.SetTextColor(25, 39, 61)
	}
	write := func(text string, size float64) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if fontSize != size {
			fontSize = size
			cache = map[rune]float64{}
		}
		if page == 0 {
			newPage()
		}
		if err := pdf.SetFont("report", "", size); err != nil {
			return err
		}
		lineHeight := size * 1.65
		var line strings.Builder
		lineWidth := 0.0
		flush := func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if y+lineHeight > bottom {
				newPage()
			}
			pdf.SetXY(38, y)
			if err := draw(line.String(), 38, y, size); err != nil {
				return err
			}
			y += lineHeight
			line.Reset()
			lineWidth = 0
			return nil
		}
		for _, char := range strings.ReplaceAll(text, "\t", "    ") {
			if char == '\r' {
				continue
			}
			if char == '\n' {
				if err := flush(); err != nil {
					return err
				}
				continue
			}
			if unicode.IsControl(char) {
				continue
			}
			cw, ok := cache[char]
			if !ok {
				var err error
				_ = pdf.SetFont(fontFor(char), "", size)
				cw, err = pdf.MeasureTextWidth(string(char))
				if err != nil {
					return err
				}
				cache[char] = cw
			}
			if lineWidth+cw > width-76 && line.Len() > 0 {
				if err := flush(); err != nil {
					return err
				}
			}
			line.WriteRune(char)
			lineWidth += cw
		}
		return flush()
	}
	if err := write(r.Run.Name, 20); err != nil {
		return nil, err
	}
	if err := write(fmt.Sprintf("任务 #%d / %s / 发起人 %s", r.Run.ID, inspectionStatusLabel(r.Run.Status), r.Run.Operator), 10); err != nil {
		return nil, err
	}
	if err := write(fmt.Sprintf("对象 %d   告警 %d   严重 %d   采集异常 %d", r.Run.TotalTargets, r.Run.WarningCount, r.Run.CriticalCount, r.Run.ErrorCount), 11); err != nil {
		return nil, err
	}
	write("CPU 为本次短时间采样；日志统计仅代表已采集记录。未知项目不等同于正常。", 9)
	for _, target := range r.Targets {
		y += 16
		if err := write(target.Target.Name+" / "+inspectionStatusLabel(target.Target.Status), 14); err != nil {
			return nil, err
		}
		write(target.Target.Endpoint+" / 执行主机 "+target.Target.ServerEndpoint, 9)
		if target.Target.Message != "" {
			write(target.Target.Message, 9)
		}
		for _, check := range target.Result.Checks {
			y += 7
			if err := write("["+inspectionStatusLabel(check.Status)+"] "+check.Name, 11); err != nil {
				return nil, err
			}
			for _, text := range []string{check.Value, check.Evidence, check.Error} {
				if text != "" {
					if err := write(text, 9); err != nil {
						return nil, err
					}
				}
			}
			if check.Advice != "" {
				if err := write("建议："+check.Advice, 9); err != nil {
					return nil, err
				}
			}
		}
		if len(target.Result.Checks) == 0 {
			write("未取得可用检查结果，请核实执行状态及错误信息。", 10)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("PDF 字体缺少 %d 个字形，请配置覆盖报告字符的 TrueType 字体", len(missing))
	}
	return pdf.GetBytesPdfReturnErr()
}
