package opsprobe

import (
	"context"
	"df-build-server/internal/inspection"
)

// InspectionDatabase executes the 16 SQL statements from the supplied guide.
func InspectionPostgreSQLDeployment(ctx context.Context, name string, c DatabaseConfig) []inspection.Observation {
	f, e, err := inspectNativePostgreSQL(ctx, InspectionDatabaseBinding{Name: name, Config: &c})
	m := nativePostgreSQLObservations(name, f, e, err)
	var out []inspection.Observation
	for _, code := range []string{"cloudhis-2", "cloudhis-16", "cloudhis-19"} {
		o := m[code]
		o.Code = code
		if code == "cloudhis-19" {
			o.Code = "postgresql-archive"
			o.Name = "PostgreSQL 归档配置与执行统计"
		}
		for _, d := range inspection.Catalogue() {
			if d.Code == o.Code {
				o.Name = d.Name
			}
		}
		out = append(out, o)
	}
	return out
}

// Successful SQL is recorded as output, without requiring an environment baseline.
func InspectionDatabase(ctx context.Context, c DatabaseConfig) []inspection.Observation {
	results, err := CollectManual(ctx, ManualOptions{Profiles: []string{"postgresql_sql"}}, &c, nil)
	if err != nil {
		return []inspection.Observation{{Code: "cloudhis-20", Status: "UNKNOWN", Reason: err.Error()}}
	}
	return results
}
