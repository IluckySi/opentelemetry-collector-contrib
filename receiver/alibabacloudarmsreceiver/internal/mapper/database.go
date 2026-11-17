// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package mapper // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/alibabacloudarmsreceiver/internal/mapper"

// dbSystemFromRpcType maps ARMS product names to OTel DBMS identifiers.
// https://opentelemetry.io/docs/specs/semconv/registry/attributes/db/#db-system-name-values
func dbSystemFromRpcType(rpcType int) string {
	switch rpcType {
	case 5:
		return "tair"
	case 52:
		return "ibm.db2"
	case 53:
		return "ibm.informix"
	case 54:
		return "sequoiadb"
	case 55:
		return "gbase"
	case 56:
		return "gaussdb"
	case 57:
		return "kingbase"
	case 58:
		return "influxdb" // ARMS documents this as "infuxdb".
	case 59:
		return "clickhouse"
	case 60, 61:
		return "mysql"
	case 62:
		return "oracle.db"
	case 63:
		return "postgresql"
	case 64:
		return "mongodb"
	case 65:
		return "ppas"
	case 66:
		return "microsoft.sql_server"
	case 67:
		return "mariadb"
	case 68:
		return "dmdb"
	case 69:
		return "oceanbase"
	case 70:
		return "redis"
	case 71:
		return "memcached"
	case 72:
		return "elasticsearch"
	case 73:
		return "kudu"
	default:
		return ""
	}
}
