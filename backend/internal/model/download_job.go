package model

import "time"

// DownloadJob persists batch artifact download progress so a page refresh can
// recover the currently running task.
type DownloadJob struct {
	ID             string    `gorm:"primaryKey;size:64" json:"id"`
	Status         string    `gorm:"size:20;index" json:"status"`
	RemotePath     string    `gorm:"size:1024" json:"remotePath"`
	LocalDir       string    `gorm:"size:1024" json:"localDir"`
	TargetPath     string    `gorm:"size:1024" json:"targetPath"`
	BatchID        string    `gorm:"size:64;index" json:"batchId"`
	FilesJSON      string    `gorm:"type:text" json:"-"`
	Count          int       `json:"count"`
	TotalFiles     int       `json:"totalFiles"`
	CompletedFiles int       `json:"completedFiles"`
	CurrentPath    string    `gorm:"size:1024" json:"currentPath"`
	Error          string    `gorm:"type:text" json:"error"`
	StartedAt      time.Time `json:"startedAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}
