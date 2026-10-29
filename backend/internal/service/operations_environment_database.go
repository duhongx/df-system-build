package service

import (
	"context"
	"crypto/sha256"
	"df-build-server/internal/model"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strconv"
)

func environmentDatabaseDigest(c PostgreSQLConfig) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(mustJSON([]string{c.Host, c.Port, c.Database, c.User, c.Password}))))
}

func (s *OperationsService) SyncEnvironmentDatabase(ctx context.Context, create bool, name string) (*model.OperationsDatabase, error) {
	cfg, err := NewPostgreSQLService().loadConfig()
	if err != nil {
		return nil, err
	}
	complete := cfg.Host != "" && cfg.Database != "" && cfg.User != "" && cfg.Password != ""
	if create && !complete {
		return nil, fmt.Errorf("环境 PostgreSQL 连接不完整，请先在环境配置维护")
	}
	var saved *model.OperationsDatabase
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(70291030)").Error; err != nil {
			return err
		}
		var row model.OperationsDatabase
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("source='environment'").Order("id").First(&row).Error
		if err != nil && err != gorm.ErrRecordNotFound {
			return err
		}
		if err == gorm.ErrRecordNotFound {
			if !create {
				return nil
			}
			// Adopt only the unchanged default record created by the old importer.
			// Independently named/workbench connections remain independent.
			defaultName := cfg.Host + " / " + cfg.Database
			var legacy model.OperationsDatabase
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("COALESCE(source,'')='' AND name=? AND host=? AND port=? AND database=? AND username=?", defaultName, cfg.Host, cfg.Port, cfg.Database, cfg.User).Order("id").First(&legacy).Error; err == nil {
				if current, e := s.databaseConfig(legacy); e == nil && current.Password == cfg.Password {
					row = legacy
				}
			}
			if row.ID == 0 {
				row = model.OperationsDatabase{Name: name, SSLMode: "require", LogFormat: "csv", LogTimezone: "Asia/Shanghai", SlowThresholdMs: 5000, LookbackHours: 24}
				if row.Name == "" {
					row.Name = "环境 PostgreSQL"
				}
			}
		}
		digest := environmentDatabaseDigest(cfg)
		if row.Source != "environment" || row.EnvironmentDigest != digest {
			port, _ := strconv.Atoi(cfg.Port)
			if port == 0 {
				port = 5432
			}
			changedEndpoint := row.Host != cfg.Host || row.Port != port || row.Database != cfg.Database || row.Username != cfg.User
			row.Source, row.EnvironmentDigest, row.Host, row.Port, row.Database, row.Username = "environment", digest, cfg.Host, port, cfg.Database, cfg.User
			row.PasswordEncrypted = ""
			row.Revision++
			if changedEndpoint {
				var host model.Server
				tx.Where("host=?", cfg.Host).Order("id").First(&host)
				row.ServerID = host.ID
				row.LogEnabled = false
				row.LogCapability = ""
				row.LogError = "连接目标已变化，请重新核对日志和备份主机"
			}
			if err := tx.Save(&row).Error; err != nil {
				return err
			}
		}
		saved = &row
		return nil
	})
	return saved, err
}
