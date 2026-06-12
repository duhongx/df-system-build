package engine

import (
	"bytes"
	"os"
	"text/template"
)

func (e *ActionExecutor) renderTemplate(ctx TaskContext, spec ActionSpec, actionName string) error {
	if spec.Target == "" {
		return pathRequiredError(ctx, actionName, "render_template.target")
	}
	templateText := spec.Content
	if spec.Source != "" {
		sourcePath, err := e.resourcePath(spec.Source)
		if err != nil {
			return &DeployError{
				Context:    ctx,
				Action:     actionName,
				Position:   "模板文件 " + spec.Source,
				Reason:     "模板路径非法",
				Detail:     err.Error(),
				Suggestion: "render_template.source 必须是离线资源目录下的相对路径。",
			}
		}
		content, err := os.ReadFile(sourcePath)
		if err != nil {
			return &DeployError{
				Context:    ctx,
				Action:     actionName,
				Position:   "模板文件 " + spec.Source,
				Reason:     "读取模板失败",
				Detail:     err.Error(),
				Suggestion: "检查离线模板文件是否存在以及权限是否正确。",
			}
		}
		templateText = string(content)
	}
	tmpl, err := template.New("dfctl-action").Option("missingkey=error").Parse(templateText)
	if err != nil {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "模板内容",
			Reason:     "解析模板失败",
			Detail:     err.Error(),
			Suggestion: "检查 Go template 语法和变量名。",
		}
	}
	var rendered bytes.Buffer
	if err := tmpl.Execute(&rendered, spec.TemplateVars); err != nil {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "模板变量",
			Reason:     "渲染模板失败",
			Detail:     err.Error(),
			Suggestion: "检查 template_vars 是否包含模板所需变量。",
		}
	}
	next := spec
	next.Content = rendered.String()
	return e.writeFile(ctx, next, actionName)
}
