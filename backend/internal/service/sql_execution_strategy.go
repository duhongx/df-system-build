package service

type SQLExecutionStrategy struct {
	Name                string
	CanRunInTransaction bool
}

func DetermineExecutionStrategy(analysis RiskAnalysis) SQLExecutionStrategy {
	if analysis.RiskLevel == "BLOCKED" {
		return SQLExecutionStrategy{Name: "MANUAL_EXPORT", CanRunInTransaction: false}
	}
	switch analysis.SQLType {
	case "CREATE_INDEX_CONCURRENTLY", "DROP_INDEX_CONCURRENTLY", "VACUUM", "VACUUM_FULL", "REINDEX", "REINDEX_DATABASE":
		return SQLExecutionStrategy{Name: "DIRECT_NO_TRANSACTION", CanRunInTransaction: false}
	default:
		return SQLExecutionStrategy{Name: "DIRECT", CanRunInTransaction: true}
	}
}
