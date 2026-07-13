// Package integration declares optional integration capabilities. These are
// contracts, not active clients: every integration is disabled by default and
// action-capable surfaces are permanently dry-run in this release candidate.
package integration

import "sort"

const SchemaVersion = 1

type Contract struct {
	SchemaVersion int      `json:"schemaVersion"`
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Enabled       bool     `json:"enabled"`
	Mode          string   `json:"mode"`
	Transport     string   `json:"transport"`
	Capabilities  []string `json:"capabilities"`
	Requirements  []string `json:"requirements,omitempty"`
	Risk          string   `json:"risk"`
}

type Result struct {
	IntegrationID string `json:"integrationId"`
	Status        string `json:"status"`
	Detail        string `json:"detail"`
}

func Catalog() []Contract {
	contracts := []Contract{
		{SchemaVersion: 1, ID: "otlp-loopback", Name: "OTLP loopback receiver", Mode: "RECEIVE_ONLY", Transport: "127.0.0.1 only", Capabilities: []string{"telemetry.receive"}, Risk: "disabled; loopback validation required before bind"},
		{SchemaVersion: 1, ID: "openai-export", Name: "ChatGPT/OpenAI local export", Mode: "READ_ONLY", Transport: "owner-selected local file", Capabilities: []string{"export.read"}, Risk: "disabled; no API or credential access"},
		{SchemaVersion: 1, ID: "ollama", Name: "Ollama", Mode: "READ_ONLY", Transport: "loopback HTTP", Capabilities: []string{"model.metadata.read"}, Risk: "disabled; no prompt submission"},
		{SchemaVersion: 1, ID: "llama-cpp", Name: "llama.cpp", Mode: "READ_ONLY", Transport: "loopback HTTP", Capabilities: []string{"model.metadata.read"}, Risk: "disabled; no prompt submission"},
		{SchemaVersion: 1, ID: "discord", Name: "Discord", Mode: "DRY_RUN", Transport: "none", Capabilities: []string{"message.preview"}, Requirements: []string{"owner approval", "credential supplied outside Observatory"}, Risk: "sending is not implemented"},
		{SchemaVersion: 1, ID: "openclaw", Name: "OpenClaw", Mode: "DRY_RUN", Transport: "none", Capabilities: []string{"action.preview"}, Requirements: []string{"owner approval"}, Risk: "execution and shell access are not implemented"},
		{SchemaVersion: 1, ID: "github", Name: "GitHub", Mode: "READ_ONLY", Transport: "disabled network client", Capabilities: []string{"repository.metadata.read"}, Requirements: []string{"owner approval", "read-only credential"}, Risk: "mutations are not implemented"},
	}
	for i := range contracts {
		contracts[i].Enabled = false
		sort.Strings(contracts[i].Capabilities)
		sort.Strings(contracts[i].Requirements)
	}
	sort.Slice(contracts, func(i, j int) bool { return contracts[i].ID < contracts[j].ID })
	return contracts
}

// Preview returns a truthful no-side-effect result. This package deliberately
// has no Send, Execute, Shell, or network method.
func Preview(id string) Result {
	for _, contract := range Catalog() {
		if contract.ID != id {
			continue
		}
		if !contract.Enabled {
			return Result{IntegrationID: id, Status: "DISABLED", Detail: contract.Risk}
		}
		return Result{IntegrationID: id, Status: "DRY_RUN", Detail: "preview only; no external action occurred"}
	}
	return Result{IntegrationID: id, Status: "UNKNOWN", Detail: "integration is not registered"}
}
