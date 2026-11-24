// Package event is the published copy of the canonical event vocabulary (ADR
// 0065) for Connector authors. Core's driverkit package owns the vocabulary;
// the lib/connector-protocol description renders this copy (protocol_gen.go)
// and core's agreement tests hold driverkit to the same description. Only
// declarations are copied: the readers that parse Driver output stay in core,
// and Event.HostOp (a core-internal payload) is not published.
package event
