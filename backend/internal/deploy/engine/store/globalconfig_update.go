package store

// GlobalConfigUpdate carries the optional sections of a global-config PUT.
// Any field left nil is skipped (the corresponding table is not touched).
// EnvReplace=true means "Env is the new full snapshot, replace the env_settings
// table" (so an empty slice clears it); EnvReplace=false means "leave env alone".
//
// Defined here because df-build-system uses the GORM/PostgreSQL-backed store.
type GlobalConfigUpdate struct {
	Deployment *DeploymentSettings
	Network    *NetworkSettings
	Env        []*EnvEntry
	EnvReplace bool
}
