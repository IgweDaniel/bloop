package config

import (
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
)

func TestConfigFilesLoad(t *testing.T) {
	cwd, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("failed to resolve cwd: %v", err)
	}
	root := filepath.Clean(filepath.Join(cwd, "../.."))

	for _, path := range []string{
		filepath.Join(root, "config/config.yaml"),
		filepath.Join(root, "config/config.example.yaml"),
	} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			v := viper.New()
			v.SetConfigFile(path)

			if err := v.ReadInConfig(); err != nil {
				t.Fatalf("failed to read config file: %v", err)
			}

			var cfg Config
			if err := v.Unmarshal(&cfg); err != nil {
				t.Fatalf("failed to unmarshal config file: %v", err)
			}

			normalizeConfig(&cfg)
			if err := validateConfig(&cfg); err != nil {
				t.Fatalf("config validation failed: %v", err)
			}
		})
	}
}
