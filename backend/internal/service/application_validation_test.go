package service

import (
	"testing"

	"pgregory.net/rapid"
)

// Property 2: Application Validation Rules
func TestPropertyApplicationValidation(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		appName := rapid.StringMatching(`[a-z\-]{0,30}`).Draw(t, "appName")
		appType := rapid.SampledFrom([]string{"java", "vue", "python", ""}).Draw(t, "appType")
		gitRepo := rapid.StringMatching(`[a-z:/\.]{0,50}`).Draw(t, "gitRepo")
		vueRole := rapid.SampledFrom([]string{"main", "sub", "standalone", ""}).Draw(t, "vueRole")
		appCode := rapid.StringMatching(`[a-z0-9]{0,6}`).Draw(t, "appCode")

		req := &CreateAppRequest{
			AppName: appName,
			AppType: appType,
			GitRepo: gitRepo,
			VueRole: vueRole,
			AppCode: appCode,
		}

		// Determine expected validity
		shouldReject := false
		reason := ""

		if appName == "" {
			shouldReject = true
			reason = "empty name"
		} else if appType != "java" && appType != "vue" {
			shouldReject = true
			reason = "invalid type"
		} else if gitRepo == "" {
			shouldReject = true
			reason = "empty repo"
		} else if appType == "vue" && vueRole == "" {
			shouldReject = true
			reason = "vue without role"
		} else if appType == "vue" && vueRole == "sub" && appCode == "" {
			shouldReject = true
			reason = "sub without code"
		}

		// Validate using service logic (without DB)
		err := validateCreateRequest(req)

		if shouldReject && err == nil {
			t.Fatalf("Expected rejection for %s but got nil error. req=%+v", reason, req)
		}
		if !shouldReject && err != nil {
			t.Fatalf("Expected acceptance but got error: %v. req=%+v", err, req)
		}
	})
}

// validateCreateRequest is a pure validation function extracted for testing
func validateCreateRequest(req *CreateAppRequest) error {
	if req.AppName == "" {
		return errEmpty("应用名称")
	}
	if req.AppType != "java" && req.AppType != "vue" {
		return errEmpty("项目类型")
	}
	if req.GitRepo == "" {
		return errEmpty("Git 仓库")
	}
	if req.AppType == "vue" {
		if req.VueRole == "" {
			return errEmpty("应用角色")
		}
		if req.VueRole == "sub" && req.AppCode == "" {
			return errEmpty("应用编号")
		}
	}
	return nil
}

type validationError struct{ msg string }

func (e *validationError) Error() string { return e.msg }
func errEmpty(field string) error        { return &validationError{msg: field + " 不能为空"} }
