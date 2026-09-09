package stages

import "df-build-server/internal/repository"

type imageSettingsReader interface {
	GetByKey(string) (string, error)
}

// Missing optional settings retain their existing empty-value behavior. Actual
// storage failures must stop the stage, without printing credential values.
func readImageSetting(reader imageSettingsReader, key string, required bool) (string, error) {
	return repository.ReadSetting(reader, key, required)
}

type imageRegistry struct{ url, user, password string }

func readImageRegistry(reader imageSettingsReader) (imageRegistry, error) {
	var result imageRegistry
	for _, item := range []struct {
		key      string
		required bool
		dest     *string
	}{
		{"docker_registry_url", true, &result.url},
		{"docker_registry_user", false, &result.user},
		{"docker_registry_password", false, &result.password},
	} {
		value, err := readImageSetting(reader, item.key, item.required)
		if err != nil {
			return imageRegistry{}, err
		}
		*item.dest = value
	}
	return result, nil
}
