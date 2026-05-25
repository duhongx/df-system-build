package service

import (
	"fmt"
	"strings"
)

type ViewDependency struct {
	Schema     string
	View       string
	Kind       string
	Definition string
}

type ViewRebuildPlan struct {
	DropSQL   string
	CreateSQL string
}

func BuildViewRebuildPlan(dep ViewDependency) ViewRebuildPlan {
	qualifiedName := quoteQualifiedName(dep.Schema, dep.View)
	definition := strings.TrimSpace(dep.Definition)
	createSQL := fmt.Sprintf("CREATE OR REPLACE VIEW %s AS\n%s;", qualifiedName, strings.TrimSuffix(definition, ";"))
	dropObject := "VIEW"
	if dep.Kind == "m" {
		dropObject = "MATERIALIZED VIEW"
		createSQL = fmt.Sprintf("CREATE MATERIALIZED VIEW %s AS\n%s;", qualifiedName, strings.TrimSuffix(definition, ";"))
	}
	return ViewRebuildPlan{
		DropSQL:   fmt.Sprintf("DROP %s IF EXISTS %s;", dropObject, qualifiedName),
		CreateSQL: createSQL,
	}
}

func quoteQualifiedName(schemaName, objectName string) string {
	schemaName = strings.TrimSpace(schemaName)
	objectName = strings.TrimSpace(objectName)
	if schemaName == "" {
		return quoteIdent(objectName)
	}
	return quoteIdent(schemaName) + "." + quoteIdent(objectName)
}

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(strings.TrimSpace(name), `"`, `""`) + `"`
}
