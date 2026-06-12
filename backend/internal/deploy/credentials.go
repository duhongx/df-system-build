package deploy

import (
	"strings"

	"df-build-server/internal/deploy/engine/runtime"
	"df-build-server/internal/model"
	"df-build-server/pkg/crypto"

	"gorm.io/gorm"
)

// NewServerCredentialResolver builds a runtime.CredentialResolver that reads
// SSH credentials live from the Server Management registry (model.Server).
//
// Credentials are looked up by host address at dial time, so rotating a
// Server's password/key takes effect on the next run without re-binding any
// deployment target (Requirement 2.5/2.6, CP-8). No credential copy is kept in
// any deployment-management table.
func NewServerCredentialResolver(db *gorm.DB) runtime.CredentialResolver {
	return func(addr string) (runtime.HostCredentials, bool) {
		host := addr
		if i := strings.LastIndex(host, ":"); i >= 0 {
			host = host[:i]
		}
		var srv model.Server
		if err := db.Where("host = ?", host).Order("id asc").First(&srv).Error; err != nil {
			return runtime.HostCredentials{}, false
		}
		creds := runtime.HostCredentials{
			User: srv.Username,
			Port: srv.Port,
		}
		secret, err := crypto.Decrypt(srv.CredentialEncrypted)
		if err != nil {
			// Credential cannot be decrypted; let the global fallback handle it.
			return runtime.HostCredentials{}, false
		}
		switch srv.AuthType {
		case "certificate":
			creds.PrivateKey = []byte(secret)
		default: // "password"
			creds.Password = secret
		}
		return creds, true
	}
}
