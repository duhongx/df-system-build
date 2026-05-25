package model

import "time"

type Executor struct {
	ID            uint       `gorm:"primaryKey" json:"id"`
	Name          string     `gorm:"uniqueIndex;size:64;not null" json:"name"`
	DockerImage   string     `gorm:"size:256" json:"dockerImage"`                   // docker 模式必填
	Type          string     `gorm:"size:20;not null" json:"type"`                  // docker / local
	CPULimit      string     `gorm:"size:10" json:"cpuLimit"`
	MemoryLimit   string     `gorm:"size:10" json:"memoryLimit"`
	CacheMounts   string     `gorm:"type:text" json:"cacheMounts"`                  // JSON: [{"hostPath":"...","containerPath":"..."}]
	WorkDir       string     `gorm:"size:512" json:"workDir"`                       // local 模式工作目录 (可选)
	EnvVars       string     `gorm:"type:text" json:"envVars"`                      // local 模式额外环境变量 (可选, KEY=VALUE 每行一个)
	Status        string     `gorm:"size:20;default:online" json:"status"`          // online / offline
	LastCheckTime *time.Time `json:"lastCheckTime"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}
