package service

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"df-build-server/internal/inspection"
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
	Snapshot  *inspection.Report
	Run       model.InspectionRun
	Targets   []inspectionReportTarget
	Generated string
}

func (r inspectionReport) Presentation() *inspection.Report {
	return inspection.Presentation(r.Document())
}

//go:embed inspection_report.gohtml
var inspectionHTMLSource string

var inspectionHTML = template.Must(template.New("report").Funcs(template.FuncMap{
	"status":     inspectionStatusLabel,
	"sections":   inspection.Sections,
	"components": inspection.Components,
	"time": func(t time.Time) string {
		if t.IsZero() {
			return "未记录"
		}
		return t.In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04:05")
	},
	"percent": func(v float64) string { return fmt.Sprintf("%.1f%%", v) },
	"fallback": func(s string) string {
		if s == "" {
			return "未填写"
		}
		return s
	},
}).Parse(inspectionHTMLSource))

func inspectionStatusLabel(s string) string {
	if v := map[string]string{"NOT_RUN": "未执行", "RECORDED": "已记录待核对", "NORMAL": "正常", "NOT_APPLICABLE": "不适用", "WARNING": "需关注", "CRITICAL": "严重", "UNKNOWN": "未取得完整结果", "QUEUED": "等待中", "RUNNING": "执行中", "COMPLETED": "已完成", "PARTIAL": "检查已结束，存在检查缺口", "FAILED": "失败", "CANCELED": "已取消"}[s]; v != "" {
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
	if err := s.freezeInspectionReport(ctx, &report); err != nil {
		return s.reportFailure(id, "HTML", err)
	}
	if err := s.db.Model(&run).Updates(map[string]any{"html_status": "GENERATING", "pdf_status": "PENDING", "report_error": "", "message": "生成在线报告"}).Error; err != nil {
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
	if err := s.db.Model(&run).Updates(map[string]any{"html_status": "READY", "pdf_status": "GENERATING", "message": "在线报告已生成，正在生成 PDF"}).Error; err != nil {
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
	return s.db.Model(&run).Updates(map[string]any{"pdf_status": "READY", "report_error": "", "message": "在线报告和 PDF 已生成"}).Error
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
		if size >= 11 && y+lineHeight*3 > bottom {
			newPage()
		}
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
	// Tables repeat their header after a page break. Very wide SQL evidence is
	// transposed to field/value rows so text remains readable on A4.
	var table func(inspection.Table) error
	table = func(t inspection.Table) error {
		if page == 0 {
			newPage()
		}
		if y+130 > bottom {
			newPage()
		}
		if t.Title != "" {
			if err := write(t.Title, 11); err != nil {
				return err
			}
		}
		if len(t.Columns) == 0 {
			return nil
		}
		if len(t.Columns) > 6 {
			for i, row := range t.Rows {
				if i >= 5 {
					break
				}
				converted := inspection.Table{Title: fmt.Sprintf("记录 %d", i+1), Columns: []string{"字段", "实测值"}, Rows: [][]string{}}
				for j, c := range t.Columns {
					v := ""
					if j < len(row) {
						v = row[j]
					}
					converted.Rows = append(converted.Rows, []string{c, shortReportText(v, 180)})
				}
				if err := table(converted); err != nil {
					return err
				}
			}
			if len(t.Rows) > 5 {
				if err := write(fmt.Sprintf("共 %d 行，PDF 展示前 5 行；完整记录见任务检查详情。", len(t.Rows)), 9); err != nil {
					return err
				}
			}
			if t.Note != "" {
				return write(t.Note, 9)
			}
			return nil
		}
		n := len(t.Columns)
		widths := make([]float64, n)
		for i := range widths {
			widths[i] = (width - 76) / float64(n)
		}
		if n == 2 {
			widths[0] = (width - 76) * 0.28
			widths[1] = (width - 76) * 0.72
		}
		if n >= 5 && (t.Columns[0] == "设备 / 挂载点" || t.Columns[0] == "主机") {
			widths[0] = (width - 76) * 0.30
			for i := 1; i < n; i++ {
				widths[i] = (width - 76) * 0.70 / float64(n-1)
			}
		}
		if n == 4 {
			ratios := []float64{0.22, 0.26, 0.26, 0.26}
			if t.Columns[0] == "级别" {
				ratios = []float64{0.09, 0.24, 0.32, 0.35}
			}
			if t.Columns[0] == "对象" {
				ratios = []float64{0.25, 0.13, 0.42, 0.20}
			}
			if t.Columns[0] == "编号" {
				ratios = []float64{0.16, 0.40, 0.14, 0.30}
			}
			for i, ratio := range ratios {
				widths[i] = (width - 76) * ratio
			}
		}
		if n >= 4 && t.Columns[0] == "Pod" {
			widths[0] = (width - 76) * 0.45
			for i := 1; i < n; i++ {
				widths[i] = (width - 76) * 0.55 / float64(n-1)
			}
		}
		wrap := func(text string, w float64) ([]string, error) {
			var lines []string
			line := ""
			used := 0.0
			for _, c := range strings.ReplaceAll(text, "\t", " ") {
				if c == '\r' {
					continue
				}
				if c == '\n' {
					lines = append(lines, line)
					line = ""
					used = 0
					continue
				}
				if unicode.IsControl(c) {
					continue
				}
				if err := pdf.SetFont(fontFor(c), "", 8.5); err != nil {
					return nil, err
				}
				cw, e := pdf.MeasureTextWidth(string(c))
				if e != nil {
					return nil, e
				}
				if used+cw > w && line != "" {
					lines = append(lines, line)
					line = ""
					used = 0
				}
				line += string(c)
				used += cw
			}
			lines = append(lines, line)
			return lines, nil
		}
		drawRow := func(row []string, header bool) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			cells := make([][]string, n)
			count := 1
			for i := 0; i < n; i++ {
				v := ""
				if i < len(row) {
					v = shortReportText(row[i], 180)
				}
				lines, e := wrap(v, widths[i]-12)
				if e != nil {
					return e
				}
				cells[i] = lines
				count = max(count, len(lines))
			}
			h := float64(count)*13 + 12
			if page == 0 {
				newPage()
			}
			if y+h > bottom {
				return fmt.Errorf("table row needs new page")
			}
			x := 38.0
			for i, lines := range cells {
				pdf.SetStrokeColor(216, 224, 234)
				pdf.SetLineWidth(0.5)
				if header {
					pdf.SetFillColor(237, 243, 250)
					pdf.RectFromUpperLeftWithStyle(x, y, widths[i], h, "FD")
				} else {
					pdf.RectFromUpperLeftWithStyle(x, y, widths[i], h, "D")
				}
				pdf.SetTextColor(35, 52, 73)
				for j, line := range lines {
					if e := draw(line, x+6, y+6+float64(j)*13, 8.5); e != nil {
						return e
					}
				}
				x += widths[i]
			}
			y += h
			return nil
		}
		if page == 0 {
			newPage()
		}
		if y+65 > bottom {
			newPage()
		}
		if err := drawRow(t.Columns, true); err != nil {
			return err
		}
		rows := t.Rows
		if len(rows) == 0 {
			rows = [][]string{{"本次未返回行，结论参见本项说明。"}}
		}
		for i, row := range rows {
			if i >= 10 {
				break
			}
			if err := drawRow(row, false); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				newPage()
				if e := drawRow(t.Columns, true); e != nil {
					return e
				}
				if e := drawRow(row, false); e != nil {
					return e
				}
			}
		}
		y += 8
		if len(t.Rows) > 10 {
			if err := write(fmt.Sprintf("共 %d 行，PDF 展示前 10 行，完整明细见任务检查详情。", len(t.Rows)), 9); err != nil {
				return err
			}
		}
		if t.Note != "" {
			return write(t.Note, 9)
		}
		return nil
	}
	doc := r.Presentation()
	r.Generated = reportTime(doc.GeneratedAt)
	for _, line := range []struct {
		text string
		size float64
	}{
		{doc.Name, 20},
		{fmt.Sprintf("任务 #%d / 发起人 %s / 报告版本 %s", doc.RunID, doc.Operator, doc.Version), 10},
		{"采集时间：" + reportTime(doc.StartedAt) + " 至 " + reportTime(doc.FinishedAt), 9},
		{"结果冻结时间：" + reportTime(doc.GeneratedAt), 9}, {"整体结论", 15}, {doc.Conclusion, 11},
		{fmt.Sprintf("本次执行 %d 项 / 指导书共 39 项 / 未纳入本次 %d 项", doc.Summary.Planned, doc.Summary.NotRun), 10},

		{"正文列出关键检查结果；原始命令与输出保存在任务检查详情。“已记录”表示执行成功，不表示自动判定业务正常。", 9},
	} {
		if err := write(line.text, line.size); err != nil {
			return nil, err
		}
	}
	if err := table(inspection.Components(doc)); err != nil {
		return nil, err
	}
	if err := write("执行对象", 15); err != nil {
		return nil, err
	}
	inventory := inspection.Table{Columns: []string{"对象", "类型", "地址", "执行服务器", "执行状态"}, Rows: [][]string{}}
	for _, o := range doc.Objects {
		inventory.Rows = append(inventory.Rows, []string{o.Name, o.Type, o.Endpoint, o.ExecutorEndpoint, inspectionStatusLabel(o.Status)})
	}
	for start := 0; start < len(inventory.Rows); start += 10 {
		batch := inventory
		batch.Rows = inventory.Rows[start:min(start+10, len(inventory.Rows))]
		if err := table(batch); err != nil {
			return nil, err
		}
	}
	for _, section := range inspection.Sections(doc) {
		y += 14
		if y+180 > bottom {
			newPage()
		}
		if err := write(section.Name, 15); err != nil {
			return nil, err
		}
		if err := write(section.Intro, 9); err != nil {
			return nil, err
		}
		if section.ID == "resources" {
			for _, o := range doc.Objects {
				if len(o.Tables) == 0 {
					continue
				}
				if y+150 > bottom {
					newPage()
				}
				if err := write(o.Name, 12); err != nil {
					return nil, err
				}
				for _, t := range o.Tables {
					if err := table(t); err != nil {
						return nil, err
					}
				}
			}
		}
		var absent []string
		for _, item := range section.Items {
			hasContent := false
			for _, e := range item.Evidence {
				if len(e.Tables) > 0 || e.Value != "" {
					hasContent = true
				}
			}
			if !hasContent {
				absent = append(absent, item.Code+" "+item.Name)
				continue
			}
			y += 8
			if y+95 > bottom {
				newPage()
			}
			if err := write(item.Name+" ["+inspectionStatusLabel(item.Status)+"] / "+item.Code, 11); err != nil {
				return nil, err
			}
			for _, e := range item.Evidence {
				if e.Value != "" {
					if err := write(e.Value, 10); err != nil {
						return nil, err
					}
				}
				if e.Reason != "" {
					if err := write(e.Reason, 9); err != nil {
						return nil, err
					}
				}
				if e.Threshold != "" {
					if err := write("判定口径："+e.Threshold, 9); err != nil {
						return nil, err
					}
				}
				for _, gap := range e.Gaps {
					if err := write("证据缺口："+gap, 9); err != nil {
						return nil, err
					}
				}
				if len(e.Executions) == 0 || e.Executions[0].Kind != "sql" {
					for _, t := range e.Tables {
						if err := table(t); err != nil {
							return nil, err
						}
					}
				}
				for _, record := range e.Executions {
					if err := write(fmt.Sprintf("执行记录 / %s / 退出码 %d / %s 至 %s", record.Kind, record.ExitCode, reportTime(record.StartedAt), reportTime(record.FinishedAt)), 9); err != nil {
						return nil, err
					}
					if err := write(record.Command, 8.5); err != nil {
						return nil, err
					}
					if record.Stdout != "" {
						if err := write("标准输出：\n"+record.Stdout, 8.5); err != nil {
							return nil, err
						}
					}

					if record.Stderr != "" {
						if err := write("标准错误：\n"+record.Stderr, 8.5); err != nil {
							return nil, err
						}
					}
					if record.Error != "" {
						if err := write("执行错误："+record.Error, 9); err != nil {
							return nil, err
						}
					}
					if record.Truncated {
						if err := write("输出已截断，不能视为完整结果。", 9); err != nil {
							return nil, err
						}
					}
				}
				if e.Advice != "" && e.Status != "NORMAL" {
					if err := write("处理建议："+e.Advice, 9); err != nil {
						return nil, err
					}
				}
				if err := write("证据："+e.ID+" / "+e.ObjectName+" / "+e.Source, 8.5); err != nil {
					return nil, err
				}
			}
		}
		if len(absent) > 0 {
			if err := write("本章未取得专项证据："+strings.Join(absent, "、")+"。具体原因见任务检查详情。", 9); err != nil {
				return nil, err
			}
		}
	}
	if err := write("附录 39 项检查结论", 15); err != nil {
		return nil, err
	}
	catalogue := inspection.Table{Columns: []string{"编号", "检查项目", "结论", "证据状态"}, Rows: [][]string{}}
	for _, item := range doc.Items {
		detail := "已取得"
		if !item.Complete {
			detail = fmt.Sprintf("缺口 %d 处", item.Missing)
		}
		if item.Status == "NOT_RUN" {
			detail = "本次未执行"
		}
		if item.Status == "NOT_APPLICABLE" {
			detail = "已记录不适用原因"
		}
		catalogue.Rows = append(catalogue.Rows, []string{item.Code, item.Name, inspectionStatusLabel(item.Status), detail})
	}
	for start := 0; start < len(catalogue.Rows); start += 10 {
		batch := catalogue
		batch.Rows = catalogue.Rows[start:min(start+10, len(catalogue.Rows))]
		if err := table(batch); err != nil {
			return nil, err
		}
	}

	if err := write("报告范围与限制", 15); err != nil {
		return nil, err
	}
	for _, line := range doc.Limitations {
		if err := write(line, 9); err != nil {
			return nil, err
		}
	}

	if len(missing) > 0 {
		return nil, fmt.Errorf("PDF 字体缺少 %d 个字形，请配置覆盖报告字符的 TrueType 字体", len(missing))
	}
	return pdf.GetBytesPdfReturnErr()
}

func shortReportText(s string, limit int) string {
	r := []rune(s)
	if len(r) > limit {
		return string(r[:limit]) + "…（完整内容见任务检查详情）"
	}
	return s
}
func displayReportValue(s string) string {
	if s == "" {
		return "未填写"
	}
	return s
}
func reportTime(t time.Time) string {
	if t.IsZero() {
		return "未记录"
	}
	return t.In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04:05")
}

func displayReportAdvice(s string) string {
	if s == "" {
		return "结合对象原始结果与业务用途核实。"
	}
	return s
}
