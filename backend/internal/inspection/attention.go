package inspection

import "fmt"

func Attention(r *Report) Table {
	if r.Mode == "configured" {
		t := Table{Title: "空间与数据可用情况", Columns: []string{"项目", "对象", "实际情况"}}
		spaces, gaps := 0, 0
		for _, f := range r.Findings {
			if f.Status == "CRITICAL" {
				spaces++
				if spaces <= 10 {
					t.Rows = append(t.Rows, []string{f.Name, f.ObjectName, presentationText(f.Reason, 180)})
				}
			} else if f.Status == "UNKNOWN" || f.Status == "NOT_RUN" {
				gaps++
			}
		}
		if spaces == 0 {
			t.Rows = append(t.Rows, []string{"空间使用", "", "已有数据未发现容量或 inode 超过 75% 的项目"})
		}
		if gaps > 0 {
			t.Rows = append(t.Rows, []string{"数据可用情况", "", fmt.Sprintf("%d 项数据未完整取得，项目、对象和具体原因见各组件章节", gaps)})
		}
		if spaces > 10 {
			t.Note = fmt.Sprintf("共 %d 项空间异常，先列 10 项，其余见组件章节。", spaces)
		}
		return t
	}
	t := Table{Title: "本次需关注的项目", Columns: []string{"检查项目", "对象", "重点情况"}}
	if r.Mode == "configured" {
		t.Title = "空间与数据可用情况"
	}
	for _, f := range r.Findings[:min(len(r.Findings), 10)] {
		value := f.Value
		if f.Status == "UNKNOWN" || value == "" {
			value = f.Reason
		}
		t.Rows = append(t.Rows, []string{f.Name, f.ObjectName, presentationText(value, 150)})
	}
	if len(r.Findings) == 0 {
		t.Rows = append(t.Rows, []string{"已取得结果范围", "", "本次已有数据未发现超过 75% 的空间使用项目或数据缺口。其他运行指标见各组件实际数据。"})
	}
	if len(r.Findings) > 10 {
		t.Note = fmt.Sprintf("共 %d 项需关注，按严重程度先列 10 项；各组件章节保留相应异常及采集缺口。", len(r.Findings))
	}
	return t
}
