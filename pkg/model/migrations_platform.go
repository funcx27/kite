package model

// platformMigrations lists the platform module models that ride the host's
// AutoMigrate. Kept here so InitDB stays byte-aligned with upstream and new
// module tables never touch it.
func platformMigrations() []any {
	return []any{
		KcrossSetting{},
		KcrossBinding{},
		BuildProvider{},
		BuildRuntimeSetting{},
		DefaultRegistrySetting{},
		Certificate{},
	}
}
