package service

import (
	"crypto/sha256"
	"encoding/binary"
	"strings"

	"df-build-server/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Serialize imports and metadata changes by filename, including schema changes
// and operations on deleted/overwritten revisions.
func lockSQLFileIdentity(tx *gorm.DB, version, name, schema string) error {
	sum := sha256.Sum256([]byte(strings.Join([]string{version, name}, "\x00")))
	return tx.Exec("SELECT pg_advisory_xact_lock(?)", int64(binary.BigEndian.Uint64(sum[:8]))).Error
}

func lockSQLFile(tx *gorm.DB, id uint) (*model.SQLChangeFile, error) {
	var file model.SQLChangeFile
	if err := tx.Where("is_deleted = false").First(&file, id).Error; err != nil {
		return nil, err
	}
	if err := lockSQLFileIdentity(tx, file.Version, file.FileName, file.SchemaName); err != nil {
		return nil, err
	}
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("is_deleted = false").First(&file, id).Error; err != nil {
		return nil, err
	}
	return &file, nil
}

func validateReleaseSQLTransactions(statements []model.SQLChangeStatement) error {
	_, err := releaseSQLTransactionGroups(statements)
	return err
}
