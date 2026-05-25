package model

import (
	"encoding/json"
	"time"
)

type Application struct {
	ID                uint       `gorm:"primaryKey" json:"id"`
	AppName           string     `gorm:"uniqueIndex;size:128;not null" json:"appName"`
	AppType           string     `gorm:"size:20;not null" json:"appType"`             // java / vue
	VueRole           string     `gorm:"size:20" json:"vueRole"`                      // main / sub / standalone (Vue only)
	IsGateway         bool       `gorm:"default:false" json:"isGateway"`              // Java 网关标识
	AppCode           string     `gorm:"size:20" json:"appCode"`                      // 子应用编号 (sub only)
	AppDesc           string     `gorm:"size:255" json:"appDesc"`
	GitRepo           string     `gorm:"size:512;not null" json:"gitRepo"`
	DefaultBranch     string     `gorm:"size:128;default:master" json:"defaultBranch"`
	NodePort          int        `gorm:"default:0" json:"nodePort"`                   // K8s Service NodePort (0=不暴露)
	IngressHost       string     `gorm:"size:256" json:"ingressHost"`                 // Deprecated: 单 Ingress 域名 (向后兼容)
	Ingresses         string     `gorm:"type:text" json:"ingresses"`                  // JSON array of IngressConfig
	ConfigMapContent  string     `gorm:"type:text" json:"configMapContent"`           // K8s ConfigMap 内容 (空=不创建)
	EnvTags           string     `gorm:"size:512" json:"envTags"`                     // 客户环境标签 (空=所有环境部署, 逗号分隔如"四川,重庆")
	TemplateCode      string     `gorm:"size:32" json:"templateCode"`
	ArtifactName      string     `gorm:"size:128" json:"artifactName"`
	BuildCommand      string     `gorm:"size:512" json:"buildCommand"`
	InstallCommand    string     `gorm:"size:512" json:"installCommand"`
	ArtifactDir       string     `gorm:"size:256" json:"artifactDir"`
	ArtifactCheckFile string     `gorm:"size:256" json:"artifactCheckFile"`
	BuilderImage      string     `gorm:"size:256" json:"builderImage"`
	Enabled           bool       `gorm:"default:true" json:"enabled"`
	LastBuildStatus   string     `gorm:"size:20" json:"lastBuildStatus"`
	LastBuildTime     *time.Time `json:"lastBuildTime"`
	CreatedAt         time.Time  `json:"createdAt"`
	UpdatedAt         time.Time  `json:"updatedAt"`
}

// IngressConfig describes a single Ingress entry attached to an application.
type IngressConfig struct {
	Name string `json:"name"` // 如 his-gateway-internal
	Host string `json:"host"` // 如 api.his.com
}

// GetIngresses parses the Ingresses JSON column into a slice of IngressConfig.
// Falls back to a single entry built from the deprecated IngressHost field
// when the new column is empty.
func (a *Application) GetIngresses() []IngressConfig {
	var result []IngressConfig
	if a.Ingresses != "" {
		_ = json.Unmarshal([]byte(a.Ingresses), &result)
	}
	if len(result) == 0 && a.IngressHost != "" {
		result = append(result, IngressConfig{Name: a.AppName, Host: a.IngressHost})
	}
	return result
}

// DeriveArtifactName returns the artifact filename based on app type and role
func (a *Application) DeriveArtifactName() string {
	switch a.AppType {
	case "java":
		return a.AppName + ".jar"
	case "vue":
		switch a.VueRole {
		case "main":
			return "web-main.zip"
		case "sub":
			if a.AppCode != "" {
				return a.AppCode + ".zip"
			}
			return a.AppName + ".zip"
		case "standalone":
			return a.AppName + ".zip"
		default:
			return a.AppName + ".zip"
		}
	default:
		return a.AppName
	}
}
