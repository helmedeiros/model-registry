// Package main is the model-registry service entry point. Boots the
// HTTP server, the Versioned Config Store, the Registry state machine,
// the deployer (markup-svc HTTP client), and the observability stack
// (OTel + jsonlog + Prometheus /metrics).
//
// Wiring lands per-iteration; ADR-0001 frames the architecture.
package main

func main() {
}
