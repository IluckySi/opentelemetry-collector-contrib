// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package alibabacloudarmsreceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/alibabacloudarmsreceiver"

import (
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/config/configopaque"
	"go.uber.org/multierr"
)

const (
	defaultAPIVersion         = "2019-08-08"
	defaultScheme             = "http"
	defaultCollectionInterval = 30 * time.Second
	defaultInitialDelay       = time.Second
	initialLookback           = 5 * time.Minute
	maxQueryWindow            = 30 * time.Minute
	defaultQueryDelay         = 3 * time.Minute
	defaultPageSize           = 100
	defaultMaxPage            = 20
	defaultHTTPTimeout        = 30 * time.Second
	defaultOverlapSlack       = 0 * time.Second
	defaultMaxCallsPerTrace   = 3
	// Larger in-memory span cache reduces duplicate re-exports after FIFO eviction.
	defaultMaxSeenSpans = 50000
)

var (
	errMissingEndpoint           = errors.New("endpoint is required")
	errMissingRegionID           = errors.New("region_id is required")
	errMissingAccessKeyID        = errors.New("access_key_id is required")
	errMissingAccessKeySecret    = errors.New("access_key_secret is required")
	errMissingOrganizationID     = errors.New("organization_id is required")
	errMissingResourceGroupID    = errors.New("resource_group_id is required")
	errMissingServices           = errors.New("services must be non-empty (production allowlist)")
	errInvalidPageSize           = errors.New("page.page_size must be between 1 and 100")
	errInvalidMaxPage            = errors.New("page.max_page must be >= 1")
	errInvalidCollectionInterval = errors.New("collection_interval must be positive")
	errInvalidQueryDelay         = errors.New("query_delay must be non-negative")
	errInvalidOverlapSlack       = errors.New("overlap_slack must be non-negative")
)

// Config configures the alibabacloudarms receiver.
type Config struct {
	Endpoint        string              `mapstructure:"endpoint"`
	RegionID        string              `mapstructure:"region_id"`
	Scheme          string              `mapstructure:"scheme"`
	APIVersion      string              `mapstructure:"api_version"`
	AccessKeyID     string              `mapstructure:"access_key_id"`
	AccessKeySecret configopaque.String `mapstructure:"access_key_secret"`
	OrganizationID  string              `mapstructure:"organization_id"`
	ResourceGroupID string              `mapstructure:"resource_group_id"`

	CollectionInterval time.Duration `mapstructure:"collection_interval"`
	InitialDelay       time.Duration `mapstructure:"initial_delay"`
	QueryDelay         time.Duration `mapstructure:"query_delay"`
	OverlapSlack       time.Duration `mapstructure:"overlap_slack"`

	Services []string `mapstructure:"services"`

	Page  PageConfig  `mapstructure:"page"`
	Stack StackConfig `mapstructure:"stack"`
	HTTP  HTTPConfig  `mapstructure:"http"`
}

// PageConfig controls SearchTracesByPage pagination.
type PageConfig struct {
	PageSize int `mapstructure:"page_size"`
	MaxPage  int `mapstructure:"max_page"`
}

// StackConfig controls optional GetStack enrichment.
type StackConfig struct {
	Enabled          bool `mapstructure:"enabled"`
	OnlyOnError      bool `mapstructure:"only_on_error"`
	MaxCallsPerTrace int  `mapstructure:"max_calls_per_trace"`
}

// HTTPConfig holds HTTP client options.
type HTTPConfig struct {
	Timeout time.Duration `mapstructure:"timeout"`
}

var _ component.Config = (*Config)(nil)

// Validate checks the receiver configuration.
func (cfg *Config) Validate() error {
	var err error
	if cfg.Endpoint == "" {
		err = multierr.Append(err, errMissingEndpoint)
	}
	if cfg.RegionID == "" {
		err = multierr.Append(err, errMissingRegionID)
	}
	if cfg.AccessKeyID == "" {
		err = multierr.Append(err, errMissingAccessKeyID)
	}
	if string(cfg.AccessKeySecret) == "" {
		err = multierr.Append(err, errMissingAccessKeySecret)
	}
	if cfg.OrganizationID == "" {
		err = multierr.Append(err, errMissingOrganizationID)
	}
	if cfg.ResourceGroupID == "" {
		err = multierr.Append(err, errMissingResourceGroupID)
	}
	if len(cfg.Services) == 0 {
		err = multierr.Append(err, errMissingServices)
	}
	if cfg.CollectionInterval <= 0 {
		err = multierr.Append(err, errInvalidCollectionInterval)
	}
	if cfg.QueryDelay < 0 {
		err = multierr.Append(err, errInvalidQueryDelay)
	}
	if cfg.OverlapSlack < 0 {
		err = multierr.Append(err, errInvalidOverlapSlack)
	}
	if cfg.Page.PageSize < 1 || cfg.Page.PageSize > 100 {
		err = multierr.Append(err, errInvalidPageSize)
	}
	if cfg.Page.MaxPage < 1 {
		err = multierr.Append(err, errInvalidMaxPage)
	}
	if cfg.Stack.Enabled && cfg.Stack.MaxCallsPerTrace < 1 {
		err = multierr.Append(err, fmt.Errorf("stack.max_calls_per_trace must be >= 1 when stack.enabled"))
	}
	switch cfg.Scheme {
	case "", "http", "https":
	default:
		err = multierr.Append(err, fmt.Errorf("scheme must be http or https, got %q", cfg.Scheme))
	}
	return err
}
