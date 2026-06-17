package repository

import (
	"df-build-server/pkg/logger"
)

// Migration represents a versioned database migration
type Migration struct {
	Version     int
	Description string
	SQL         string
}

// migrations is the ordered list of all schema migrations
// Add new migrations at the end with incrementing version numbers
var migrations = []Migration{
	{
		Version:     1,
		Description: "Remove legacy executor_id column from tasks table",
		SQL:         `ALTER TABLE tasks DROP COLUMN IF EXISTS executor_id;`,
	},
	{
		Version:     2,
		Description: "Remove legacy dockerfile_code and k8s_template_code from tasks",
		SQL: `
ALTER TABLE tasks DROP COLUMN IF EXISTS dockerfile_code;
ALTER TABLE tasks DROP COLUMN IF EXISTS k8s_template_code;
`,
	},
	{
		Version:     3,
		Description: "Update config items to new naming scheme",
		SQL: `
UPDATE config_items SET code = 'deployment-java', name = 'Java Deployment' WHERE code = 'k8s-deploy-jar';
UPDATE config_items SET code = 'deployment-web', name = 'Web Deployment' WHERE code = 'k8s-deploy-web';
UPDATE config_items SET code = 'service-java', name = 'Java Service' WHERE code = 'k8s-svc-jar';
UPDATE config_items SET code = 'service-web', name = 'Web Service' WHERE code = 'k8s-svc-web';
UPDATE config_items SET code = 'ingress', name = 'Ingress' WHERE code = 'k8s-ingress';
UPDATE config_items SET code = 'app-sh-java', name = 'Java App' WHERE code = 'script-app-sh';
UPDATE config_items SET code = 'delete-app-java', name = 'Java Offline' WHERE code = 'script-delete-app-sh';
UPDATE config_items SET code = 'configmap-web-main', name = 'ConfigMap (web-main)' WHERE code = 'k8s-cm-web-main';
UPDATE config_items SET code = 'configmap-web-cdr', name = 'ConfigMap (web-cdr)' WHERE code = 'k8s-cm-web-cdr';
UPDATE config_items SET code = 'configmap-web-opm', name = 'ConfigMap (web-opm)' WHERE code = 'k8s-cm-web-opm';
`,
	},
	{
		Version:     4,
		Description: "Add new settings keys for environment config",
		SQL: `
INSERT INTO settings (key, value, description) VALUES ('build_mode', 'docker', '编译模式') ON CONFLICT (key) DO NOTHING;
INSERT INTO settings (key, value, description) VALUES ('deploy_mode', 'deploy', '部署模式') ON CONFLICT (key) DO NOTHING;
INSERT INTO settings (key, value, description) VALUES ('customer_env', '本地', '客户环境') ON CONFLICT (key) DO NOTHING;
INSERT INTO settings (key, value, description) VALUES ('nacos_url', '', 'Nacos 地址') ON CONFLICT (key) DO NOTHING;
INSERT INTO settings (key, value, description) VALUES ('nacos_user', 'nacos', 'Nacos 用户名') ON CONFLICT (key) DO NOTHING;
INSERT INTO settings (key, value, description) VALUES ('nacos_password', '', 'Nacos 密码') ON CONFLICT (key) DO NOTHING;
INSERT INTO settings (key, value, description) VALUES ('skywalking_oap_url', '', 'SkyWalking OAP 地址') ON CONFLICT (key) DO NOTHING;
INSERT INTO settings (key, value, description) VALUES ('skywalking_graphql_url', '', 'SkyWalking GraphQL 地址') ON CONFLICT (key) DO NOTHING;
INSERT INTO settings (key, value, description) VALUES ('postgresql_host', '', 'PostgreSQL 主机') ON CONFLICT (key) DO NOTHING;
INSERT INTO settings (key, value, description) VALUES ('postgresql_port', '5432', 'PostgreSQL 端口') ON CONFLICT (key) DO NOTHING;
INSERT INTO settings (key, value, description) VALUES ('postgresql_user', '', 'PostgreSQL 用户名') ON CONFLICT (key) DO NOTHING;
INSERT INTO settings (key, value, description) VALUES ('postgresql_password', '', 'PostgreSQL 密码') ON CONFLICT (key) DO NOTHING;
INSERT INTO settings (key, value, description) VALUES ('postgresql_admin_password', '', 'PostgreSQL 管理员密码') ON CONFLICT (key) DO NOTHING;
INSERT INTO settings (key, value, description) VALUES ('postgresql_database', '', 'PostgreSQL 数据库') ON CONFLICT (key) DO NOTHING;
`,
	},
	{
		Version:     5,
		Description: "Classify independent Vue apps as standalone",
		SQL: `
UPDATE applications
SET vue_role = 'standalone',
    app_code = '',
    artifact_name = app_name || '.zip'
WHERE app_name IN ('web-cdr', 'web-opm');

UPDATE applications
SET vue_role = 'main',
    app_code = '',
    artifact_name = 'web-main.zip'
WHERE app_name = 'web-main';
`,
	},
}

// RunMigrations executes pending migrations based on the current schema version
func RunMigrations() {
	// Create migration tracking table
	DB.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, description TEXT, applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP)`)

	// Get current version
	var currentVersion int
	DB.Raw("SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&currentVersion)

	// Run pending migrations
	for _, m := range migrations {
		if m.Version <= currentVersion {
			continue
		}
		if m.SQL != "" {
			if err := DB.Exec(m.SQL).Error; err != nil {
				logger.Log.Warnf("Migration v%d (%s) partial failure: %v", m.Version, m.Description, err)
				// Don't block startup — some statements may fail if already applied
			}
		}
		DB.Exec("INSERT INTO schema_migrations (version, description) VALUES (?, ?)", m.Version, m.Description)
		logger.Log.Infof("Migration v%d applied: %s", m.Version, m.Description)
	}
}
