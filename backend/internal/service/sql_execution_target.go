package service

import (
	"encoding/json"
	"fmt"
	"strings"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"gorm.io/gorm"
)

// Credentials do not identify a database. Password rotation must not reset progress.
func releaseSQLTarget(cfg PostgreSQLConfig) string {
	if strings.TrimSpace(cfg.Host) == "" || strings.TrimSpace(cfg.Database) == "" {
		return ""
	}
	port := strings.TrimSpace(cfg.Port)
	if port == "" {
		port = "5432"
	}
	value, _ := json.Marshal(struct{ Host, Port, Database string }{
		strings.ToLower(strings.TrimSpace(cfg.Host)), port, cfg.Database,
	})
	return string(value)
}

func currentReleaseSQLTarget() (string, error) {
	return readReleaseSQLTarget(repository.DB)
}

// Read on the caller's transaction connection, including pools of size one.
func readReleaseSQLTarget(tx *gorm.DB) (string, error) {
	var settings []model.Settings
	if err := tx.Where("key IN ?", []string{"postgresql_host", "postgresql_port", "postgresql_database"}).Find(&settings).Error; err != nil {
		return "", err
	}
	cfg := PostgreSQLConfig{}
	for _, setting := range settings {
		switch setting.Key {
		case "postgresql_host":
			cfg.Host = setting.Value
		case "postgresql_port":
			cfg.Port = setting.Value
		case "postgresql_database":
			cfg.Database = setting.Value
		}
	}
	return releaseSQLTarget(cfg), nil
}

func requireReleaseSQLTarget(file *model.SQLChangeFile, target string) error {
	if target == "" {
		return fmt.Errorf("请先配置 PostgreSQL 数据库连接")
	}
	if file.DatabaseTarget != "" && file.DatabaseTarget != target {
		return fmt.Errorf("数据库连接目标已变化，本文件的执行进度属于其他数据库；请在当前数据库重新上传文件，不能沿用原库进度")
	}
	return nil
}

// Before serving requests, bind pre-upgrade progress once. Prefer the recorded
// database; legacy results without a recorded target use the upgrade-time
// connection as their baseline. This does not invent targets in audit records.
func BackfillSQLExecutionTargets() error {
	target, err := currentReleaseSQLTarget()
	if err != nil {
		return err
	}
	return repository.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", int64(681294032)).Error; err != nil {
			return err
		}
		var files []model.SQLChangeFile
		if err := tx.Omit("file_content").Where("database_target = ''").Find(&files).Error; err != nil {
			return err
		}
		for _, file := range files {
			var records []model.SQLStatementRecord
			if err := tx.Raw(`WITH RECURSIVE lineage(id) AS (
			 SELECT id FROM sql_change_statements WHERE file_id = ?
			 UNION SELECT r.source_statement_id FROM sql_statement_records r JOIN lineage l ON r.statement_id = l.id WHERE r.source_statement_id <> 0
			) SELECT DISTINCT r.database_host, r.database_port, r.database_name
			 FROM sql_statement_records r JOIN lineage l ON r.statement_id = l.id
			 WHERE r.database_host <> '' AND r.database_name <> ''`, file.ID).Scan(&records).Error; err != nil {
				return err
			}
			known := map[string]bool{}
			for _, r := range records {
				known[releaseSQLTarget(PostgreSQLConfig{Host: r.DatabaseHost, Port: r.DatabasePort, Database: r.DatabaseName})] = true
			}
			bound := target
			for key := range known {
				bound = key
			}
			if len(known) > 1 {
				bound = fmt.Sprintf("legacy-ambiguous:%d", file.ID)
			}
			if bound != "" {
				if err := tx.Model(&model.SQLChangeFile{}).Where("id = ? AND database_target = ''", file.ID).Update("database_target", bound).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
}
