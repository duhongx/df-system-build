package model

import "time"

// Server represents a managed server (for WebSSH/WebSFTP/monitoring)
type Server struct {
	ID                  uint       `gorm:"primaryKey" json:"id"`
	Host                string     `gorm:"size:128;not null" json:"host"`
	Remark              string     `gorm:"size:128" json:"remark"`
	Port                int        `gorm:"default:22" json:"port"`
	Username            string     `gorm:"size:64;not null" json:"username"`
	AuthType            string     `gorm:"size:20;not null" json:"authType"`            // password / certificate
	CredentialEncrypted string     `gorm:"type:text" json:"-"`                          // Encrypted password or private key
	CertPassphrase      string     `gorm:"size:256" json:"-"`                           // Encrypted passphrase for private key
	ConnTimeout         int        `gorm:"default:10" json:"connTimeout"`               // Connection timeout in seconds
	ForbiddenCommands   string     `gorm:"size:512" json:"forbiddenCommands"`           // Comma-separated dangerous commands
	SortOrder           int        `gorm:"default:0" json:"sortOrder"`
	Status              string     `gorm:"size:20;default:unknown" json:"status"`        // online / offline / unknown
	LastConnTime        *time.Time `json:"lastConnTime"`
	CreatedBy           string     `gorm:"size:64" json:"createdBy"`
	CreatedAt           time.Time  `json:"createdAt"`
	UpdatedAt           time.Time  `json:"updatedAt"`
}

// ServerLog records WebSSH and WebSFTP operations
type ServerLog struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	ServerID  uint      `gorm:"index;not null" json:"serverId"`
	Type      string    `gorm:"size:20;not null" json:"type"` // ssh / sftp
	Operator  string    `gorm:"size:64" json:"operator"`
	Content   string    `gorm:"type:text" json:"content"` // Command or file operation description
	ClientIP  string    `gorm:"size:64" json:"clientIp"`
	CreatedAt time.Time `json:"createdAt"`
}
