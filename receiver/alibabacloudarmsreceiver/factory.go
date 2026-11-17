// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package alibabacloudarmsreceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/alibabacloudarmsreceiver"

import (
	"context"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/receiver"

	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/alibabacloudarmsreceiver/internal/metadata"
)

func NewFactory() receiver.Factory {
	return receiver.NewFactory(
		metadata.Type,
		createDefaultConfig,
		receiver.WithTraces(createTracesReceiver, metadata.TracesStability),
	)
}

func createDefaultConfig() component.Config {
	return &Config{
		Scheme:             defaultScheme,
		APIVersion:         defaultAPIVersion,
		CollectionInterval: defaultCollectionInterval,
		InitialDelay:       defaultInitialDelay,
		QueryDelay:         defaultQueryDelay,
		OverlapSlack:       defaultOverlapSlack,
		Page: PageConfig{
			PageSize: defaultPageSize,
			MaxPage:  defaultMaxPage,
		},
		Stack: StackConfig{
			Enabled:          false,
			OnlyOnError:      true,
			MaxCallsPerTrace: defaultMaxCallsPerTrace,
		},
		HTTP: HTTPConfig{
			Timeout: defaultHTTPTimeout,
		},
	}
}

func createTracesReceiver(
	_ context.Context,
	settings receiver.Settings,
	cfg component.Config,
	next consumer.Traces,
) (receiver.Traces, error) {
	return newReceiver(settings, cfg.(*Config), next), nil
}
