// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

//go:generate make mdatagen

// Package alibabacloudarmsreceiver implements a traces receiver that polls
// Alibaba Cloud ARMS OpenAPI (EagleEye call chains) and converts them to OTLP.
package alibabacloudarmsreceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/alibabacloudarmsreceiver"
