package mcpauth

import _ "embed"

// PostgreSQLSchema installs the MCP credential, admission and usage tables in the connection search path.
//
//go:embed schema.sql
var PostgreSQLSchema string
