package handler

import (
	"df-build-server/internal/repository"
	"df-build-server/internal/service"
	"df-build-server/pkg/response"
	"github.com/gin-gonic/gin"
)

func (h *SettingsHandler) SavePostgreSQLAccountPasswords(c *gin.Context) {
	var req struct {
		Items []struct {
			Username string `json:"username" binding:"required"`
			Password string `json:"password" binding:"required"`
		} `json:"items" binding:"max=500,dive"`
		Config map[string]string `json:"config"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || (len(req.Items) == 0 && len(req.Config) == 0) {
		response.Fail(c, 10907, "请选择账号并填写密码，或提交连接设置")
		return
	}
	passwords := make(map[string]string, len(req.Items))
	for _, item := range req.Items {
		if err := service.ValidatePostgreSQLBusinessUsername(item.Username); err != nil {
			response.Fail(c, 10907, err.Error())
			return
		}
		if _, exists := passwords[item.Username]; exists {
			response.Fail(c, 10907, "业务账号不能重复")
			return
		}
		passwords[item.Username] = item.Password
	}
	allowed := map[string]bool{"postgresql_host": true, "postgresql_port": true, "postgresql_database": true, "postgresql_admin_password": true, "postgresql_password": true, "postgresql_password_mode": true}
	for key := range req.Config {
		if !allowed[key] {
			response.Fail(c, 10907, "仅允许保存 PostgreSQL 连接设置")
			return
		}
	}
	if mode, ok := req.Config["postgresql_password_mode"]; ok && mode != "shared" && mode != "individual" {
		response.Fail(c, 10907, "请选择有效的业务密码方式")
		return
	}
	if err := repository.NewPostgreSQLAccountRepo().SavePasswords(passwords, req.Config); err != nil {
		response.Fail(c, 10907, "保存失败，所有本次修改均未生效")
		return
	}
	response.OKWithMessage(c, "密码设置已保存", gin.H{"updatedCount": len(passwords)})
}

func (h *SettingsHandler) ListPostgreSQLPasswordOverrides(c *gin.Context) {
	list, err := repository.NewPostgreSQLAccountRepo().List()
	if err != nil {
		response.Fail(c, 10907, "读取账号专用密码配置失败")
		return
	}
	response.OK(c, list)
}

func (h *SettingsHandler) DiscoverPostgreSQLAccounts(c *gin.Context) {
	var req struct {
		Config map[string]string `json:"config" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 10907, "参数错误")
		return
	}
	list, err := service.DiscoverPostgreSQLAccounts(c.Request.Context(), req.Config)
	if err != nil {
		response.Fail(c, 10907, err.Error())
		return
	}
	response.OK(c, list)
}

func (h *SettingsHandler) SetPostgreSQLAccountPassword(c *gin.Context) {
	username := c.Param("username")
	if err := service.ValidatePostgreSQLBusinessUsername(username); err != nil {
		response.Fail(c, 10907, err.Error())
		return
	}
	var req struct {
		Password string `json:"password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 10907, "请输入专用密码")
		return
	}
	if err := repository.NewPostgreSQLAccountRepo().SetPassword(username, req.Password); err != nil {
		response.Fail(c, 10907, "保存专用密码失败")
		return
	}
	response.OKWithMessage(c, "专用密码已保存", nil)
}

func (h *SettingsHandler) DeletePostgreSQLAccountPassword(c *gin.Context) {
	username := c.Param("username")
	if err := service.ValidatePostgreSQLBusinessUsername(username); err != nil {
		response.Fail(c, 10907, err.Error())
		return
	}
	if err := repository.NewPostgreSQLAccountRepo().Delete(username); err != nil {
		response.Fail(c, 10907, "清除专用密码失败")
		return
	}
	response.OKWithMessage(c, "账号密码已清除", nil)
}

func (h *SettingsHandler) TestPostgreSQLAccount(c *gin.Context) {
	var req struct {
		Config map[string]string `json:"config" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 10907, "参数错误")
		return
	}
	response.OK(c, service.TestPostgreSQLBusinessAccount(c.Request.Context(), req.Config, c.Param("username")))
}
