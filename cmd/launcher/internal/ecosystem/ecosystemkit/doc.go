// Package ecosystemkit holds the helpers no single ecosystem owns (ADR 0048,
// amendment of 2026-10-22), so ecosystem packages can share them without
// depending on each other. It names no ecosystem and imports neither the
// ecosystem package nor any ecosystem's code, which keeps it free of import
// cycles. A helper that knows an ecosystem's file format stays with that
// ecosystem.
package ecosystemkit
