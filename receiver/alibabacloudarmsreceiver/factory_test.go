// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package alibabacloudarmsreceiver

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/receiver/receivertest"

	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/alibabacloudarmsreceiver/internal/metadata"
)

func TestFactoryDefaults(t *testing.T) {
	f := NewFactory()
	require.Equal(t, metadata.Type, f.Type())
	cfg := f.CreateDefaultConfig()
	require.NoError(t, componenttest.CheckConfigStruct(cfg))

	c := cfg.(*Config)
	c.Endpoint = "arms-api.example.com"
	c.RegionID = "test-region"
	c.AccessKeyID = "ak"
	c.AccessKeySecret = "sk"
	c.OrganizationID = "org"
	c.ResourceGroupID = "rs"
	c.Services = []string{"svc"}
	require.NoError(t, c.Validate())

	r, err := f.CreateTraces(t.Context(), receivertest.NewNopSettings(metadata.Type), c, consumertest.NewNop())
	require.NoError(t, err)
	require.NotNil(t, r)
}
