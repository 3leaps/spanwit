package config

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fulmenhq/gofulmen/appidentity"
	gofulmenconfig "github.com/fulmenhq/gofulmen/config"
	"github.com/fulmenhq/gofulmen/logging"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"

	spanwitschema "github.com/3leaps/spanwit/config/schema"
	"github.com/3leaps/spanwit/internal/contract"
)

// ResolvePath returns the config file path using CLI, environment, then default precedence.
func ResolvePath(identity *appidentity.Identity, explicitPath string) string {
	if explicitPath != "" {
		return expandHome(explicitPath)
	}
	if identity != nil {
		if envPath := os.Getenv(identity.EnvVar("CONFIG_PATH")); envPath != "" {
			return expandHome(envPath)
		}
	}
	return DefaultPath(identity)
}

// LoadConfig loads and strictly decodes a spanwit config file.
func LoadConfig(ctx context.Context, identity *appidentity.Identity, logger *logging.Logger, explicitPath string) (*Config, string, error) {
	_ = ctx
	configPath := ResolvePath(identity, explicitPath)
	data, err := os.ReadFile(configPath) // #nosec G304 -- Config path is user-selected by CLI/env/default convention.
	if err != nil {
		return nil, configPath, fmt.Errorf("failed to read config file: %w", err)
	}
	if err := contract.ValidateYAML(spanwitschema.SpanwitConfigV1, data); err != nil {
		return nil, configPath, fmt.Errorf("config does not match spanwit schema: %w", err)
	}

	// Decode into an empty shell so omitted defaults fields stay unset.
	// Seeding min_size/min_age/max_age then decoding over them used to inject
	// silent gates when a partial defaults: block was present.
	cfg := EmptyConfig()
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(cfg); err != nil {
		return nil, configPath, fmt.Errorf("failed to parse config file: %w", err)
	}
	if err := validateConfig(cfg); err != nil {
		return nil, configPath, err
	}

	logger.Info("loaded config from file", zap.String("path", configPath))
	return cfg, configPath, nil
}

// DefaultPath returns the default config path. It delegates to gofulmen/config
// for XDG-aware directory resolution (honoring XDG_CONFIG_HOME, falling back to
// ~/.config), then uses the standard config.yaml file name.
func DefaultPath(identity *appidentity.Identity) string {
	configName := "spanwit"
	if identity != nil && identity.ConfigName != "" {
		configName = identity.ConfigName
	}
	return filepath.Join(gofulmenconfig.GetAppConfigDir(configName), "config.yaml")
}

func validateConfig(cfg *Config) error {
	if cfg.Version != 1 {
		return fmt.Errorf("unsupported config version %d", cfg.Version)
	}
	signatures := MergeSignatureCatalogs(BuiltInSignatures(), cfg.Signatures)
	if err := validateDomains(cfg); err != nil {
		return err
	}
	// Resolve + validate the domain catalog (overlay escalation, dangling links,
	// unknown enabled domains) so every config-consuming command fails the same way.
	if _, err := ResolveCatalog(cfg); err != nil {
		return err
	}
	if len(cfg.Paths) == 0 {
		return fmt.Errorf("config must include at least one path")
	}
	for i, path := range cfg.Paths {
		if path.Path == "" {
			return fmt.Errorf("paths[%d].path is required", i)
		}
		for j, target := range path.Targets {
			if target.Pattern == "" && target.Signature == "" {
				return fmt.Errorf("paths[%d].targets[%d] must set pattern or signature", i, j)
			}
			if target.Pattern != "" && target.Signature != "" {
				return fmt.Errorf("paths[%d].targets[%d] cannot set both pattern and signature", i, j)
			}
			if target.Signature != "" {
				if err := ValidateSignatureID(target.Signature); err != nil {
					return fmt.Errorf("paths[%d].targets[%d]: %w", i, j, err)
				}
				if _, ok := signatures.Lookup(target.Signature); !ok {
					return fmt.Errorf("paths[%d].targets[%d] references unknown signature %q", i, j, target.Signature)
				}
			}
		}
	}
	return nil
}

// validateDomains checks the optional domains policy block. Deep overlay
// validation (trust-ceiling escalation, dangling links) happens where the
// catalog is resolved (space); here we reject only malformed enabled names and
// overlay ids so usage errors surface at config load.
func validateDomains(cfg *Config) error {
	if cfg.Domains == nil {
		return nil
	}
	if cfg.Domains.Enabled != nil {
		for i, d := range *cfg.Domains.Enabled {
			if err := validateDomainName(d); err != nil {
				return fmt.Errorf("domains.enabled[%d]: %w", i, err)
			}
		}
	}
	seen := map[string]bool{}
	for i, e := range cfg.Domains.Catalog {
		if err := ValidateSignatureID(e.ID); err != nil {
			return fmt.Errorf("domains.catalog[%d].id: %w", i, err)
		}
		if seen[e.ID] {
			return fmt.Errorf("domains.catalog[%d]: duplicate overlay entry id %q", i, e.ID)
		}
		seen[e.ID] = true
	}
	return nil
}

// validateDomainName enforces a single lowercase taxonomy segment.
func validateDomainName(d string) error {
	if d == "" {
		return fmt.Errorf("empty domain name")
	}
	for _, r := range d {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return fmt.Errorf("domain %q must be a lowercase segment (a-z0-9_-)", d)
		}
	}
	if d[0] == '-' || d[0] == '_' || d[len(d)-1] == '-' || d[len(d)-1] == '_' {
		return fmt.Errorf("domain %q must start and end alphanumeric", d)
	}
	return nil
}

func expandHome(path string) string {
	if path == "~" {
		homeDir, _ := os.UserHomeDir()
		return homeDir
	}
	if len(path) > 2 && path[:2] == "~/" {
		homeDir, _ := os.UserHomeDir()
		return filepath.Join(homeDir, path[2:])
	}
	return path
}
