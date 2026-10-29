package inspection

import "fmt"

func Attention(r *Report) Table {
	t := Table{Title: "本次需关注的项目", Columns: []string{"检查项目", "对象", "重点情况"}}
	for _, f := range r.Findings[:min(len(r.Findings), 10)] {
		value := f.Value
		if f.Status == "UNKNOWN" || value == "" {
			value = f.Reason
		}
		t.Rows = append(t.Rows, []string{f.Name, f.ObjectName, presentationText(value, 150)})
	}
	if len(r.Findings) == 0 {
		t.Rows = append(t.Rows, []string{"已取得结果范围", "", "本次没有识别出需关注的异常；未登记组件不代表已检查。"})
	}
	if len(r.Findings) > 10 {
		t.Note = fmt.Sprintf("共 %d 项需关注，按严重程度先列 10 项；各组件章节保留相应异常及采集缺口。", len(r.Findings))
	}
	return t
}
