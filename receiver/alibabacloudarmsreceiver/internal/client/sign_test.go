// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPopSignStable(t *testing.T) {
	params := map[string]string{
		"AccessKeyId":      "testid",
		"Action":           "GetTrace",
		"Format":           "JSON",
		"RegionId":         "test-region",
		"SignatureMethod":  "HMAC-SHA1",
		"SignatureNonce":   "nonce",
		"SignatureVersion": "1.0",
		"Timestamp":        "2024-01-02T03:04:05Z",
		"Version":          "2019-08-08",
		"TraceID":          "abc",
	}
	sig1 := popSign("testsecret", params)
	sig2 := popSign("testsecret", params)
	require.Equal(t, sig1, sig2)
	require.NotEmpty(t, sig1)

	params["TraceID"] = "abcd"
	sig3 := popSign("testsecret", params)
	require.NotEqual(t, sig1, sig3)
}

func TestPercentEncode(t *testing.T) {
	require.Equal(t, "%20", percentEncode(" "))
	require.Equal(t, "%2A", percentEncode("*"))
	require.Equal(t, "~", percentEncode("~"))
}
