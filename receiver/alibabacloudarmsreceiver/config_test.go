// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package alibabacloudarmsreceiver

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/confmap"
	"go.yaml.in/yaml/v3"
)

func validConfig() *Config {
	cfg := createDefaultConfig().(*Config)
	cfg.Endpoint = "arms-api.example.com"
	cfg.RegionID = "test-region"
	cfg.AccessKeyID = "ak"
	cfg.AccessKeySecret = "sk"
	cfg.OrganizationID = "org"
	cfg.ResourceGroupID = "rs"
	cfg.Services = []string{"svc-a"}
	cfg.QueryDelay = 0
	return cfg
}

func TestConfigValidate(t *testing.T) {
	require.NoError(t, validConfig().Validate())

	cfg := validConfig()
	cfg.Services = nil
	require.ErrorIs(t, cfg.Validate(), errMissingServices)

	cfg = validConfig()
	cfg.Page.PageSize = 101
	require.ErrorIs(t, cfg.Validate(), errInvalidPageSize)

	cfg = validConfig()
	cfg.Page.MaxPage = 0
	require.ErrorIs(t, cfg.Validate(), errInvalidMaxPage)

	cfg = validConfig()
	cfg.OverlapSlack = -time.Second
	require.ErrorIs(t, cfg.Validate(), errInvalidOverlapSlack)

	cfg = validConfig()
	cfg.QueryDelay = -time.Second
	require.ErrorIs(t, cfg.Validate(), errInvalidQueryDelay)
}

func TestDefaultQueryTiming(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	require.Equal(t, 3*time.Minute, cfg.QueryDelay)
	require.Zero(t, cfg.OverlapSlack)
}

func TestQueryTimingConfiguration(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	conf := confmap.NewFromStringMap(map[string]any{
		"query_delay":   "0s",
		"overlap_slack": "30s",
	})
	require.NoError(t, conf.Unmarshal(cfg))
	require.Zero(t, cfg.QueryDelay)
	require.Equal(t, 30*time.Second, cfg.OverlapSlack)
}

func TestLoadExampleConfig(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "config.yaml"))
	require.NoError(t, err)
	var file struct {
		Receivers map[string]map[string]any `yaml:"receivers"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &file))
	values, ok := file.Receivers["alibabacloudarms"]
	require.True(t, ok)

	cfg := createDefaultConfig().(*Config)
	require.NoError(t, confmap.NewFromStringMap(values).Unmarshal(cfg))
	require.NoError(t, cfg.Validate())
	require.Equal(t, 60*time.Second, cfg.CollectionInterval)
	require.Equal(t, 3*time.Minute, cfg.QueryDelay)
	require.Equal(t, 30*time.Second, cfg.OverlapSlack)
	require.Equal(t, 50, cfg.Page.PageSize)
	require.Equal(t, 5, cfg.Page.MaxPage)
}

func TestRemovedLookbackOptionsAreRejected(t *testing.T) {
	for _, field := range []string{"lookback", "max_lookback"} {
		t.Run(field, func(t *testing.T) {
			cfg := createDefaultConfig().(*Config)
			err := confmap.NewFromStringMap(map[string]any{field: "5m"}).Unmarshal(cfg)
			require.ErrorContains(t, err, field)
		})
	}
}
