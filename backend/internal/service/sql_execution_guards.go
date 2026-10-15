package service

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"

	"df-build-server/internal/model"
	pg_query "github.com/pganalyze/pg_query_go/v6"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Serialize first imports as well as operations on existing/deleted revisions.
func lockSQLFileIdentity(tx *gorm.DB, version, name, schema string) error {
	sum := sha256.Sum256([]byte(strings.Join([]string{version, name, schema}, "\x00")))
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
	for _, stmt := range statements {
		parsed, err := pg_query.Parse(stmt.SQLContent)
		// Keep ordinary syntax errors on the database execution path. The parser
		// is used here to distinguish top-level transaction control from bodies.
		if err != nil {
			continue
		}
		for _, raw := range parsed.Stmts {
			variable := raw.Stmt.GetVariableSetStmt()
			if raw.Stmt.GetTransactionStmt() != nil || (variable != nil && variable.Name == "TRANSACTION") {
				return fmt.Errorf("第 %d 行包含事务控制语句；普通 SQL 文件逐句提交，请移除 BEGIN/COMMIT/ROLLBACK/SAVEPOINT 等事务控制语句", stmt.LineNumber)
			}
		}
	}
	return nil
}
