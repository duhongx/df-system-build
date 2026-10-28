package opsprobe

import (
	"context"
	"df-build-server/internal/inspection"
)

// InspectionDatabase executes the 16 SQL statements from the supplied guide.
// Successful SQL is recorded as output, without requiring an environment baseline.
func InspectionDatabase(ctx context.Context, c DatabaseConfig) []inspection.Observation {
	results, err := CollectManual(ctx, ManualOptions{Profiles: []string{"postgresql_sql"}}, &c, nil)
	if err != nil {
		return []inspection.Observation{{Code: "cloudhis-20", Status: "UNKNOWN", Reason: err.Error()}}
	}
	return results
}
