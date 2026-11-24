package connectorprotocol

import _ "embed"

// Schema is protocol.schema.json, the JSON Schema (draft 2020-12) of every
// method's params and result. It is rendered with protocol_gen.go from
// lib/connector-protocol; the schematest package validates against it.
//
//go:embed protocol.schema.json
var Schema []byte
