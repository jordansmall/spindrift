// Package ecosystemkit holds the helpers no single ecosystem owns (ADR 0048,
// amendment of 2026-10-22), so ecosystem packages share them without
// depending on each other. It imports neither the ecosystem package nor any
// ecosystem's code, keeping it free of import cycles; a helper that knows an
// ecosystem's file format stays with that ecosystem.
package ecosystemkit
