//nolint:testpackage // Match the rest of the model test files (agent_schema_test, schema_test, etc).
package model

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTraceSchemaParityWithTypesScript walks the trace JSON schema
// and asserts that every required event property has a counterpart
// in the frontend's TraceEvent declaration. The two evolve
// independently — Go changes via go.mod + regen, TS changes via
// web/package.json — so a parity test catches drift before a
// backend change ships without the frontend matching.
//
// The check is deliberately one-way: schema properties must exist
// in TS. TS-only properties are allowed (the frontend can carry
// view-layer fields the backend never emits) but the schema is the
// source of truth for what the API returns.
func TestTraceSchemaParityWithTypesScript(t *testing.T) {
	t.Parallel()

	repoRoot := filepath.Join("..", "..")
	schemaPath := filepath.Join(repoRoot, "schema", "trace.schema.json")
	typesPath := filepath.Join(repoRoot, "web", "src", "types.ts")

	schemaBytes, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("read %s: %v", schemaPath, err)
	}

	typesBytes, err := os.ReadFile(typesPath)
	if err != nil {
		t.Fatalf("read %s: %v", typesPath, err)
	}

	var schema struct {
		Properties map[string]struct {
			Properties map[string]struct{} `json:"properties"`
			Items      struct {
				Properties map[string]struct{} `json:"properties"`
				Required   []string            `json:"required"`
			} `json:"items"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(schemaBytes, &schema); err != nil {
		t.Fatalf("parse schema: %v", err)
	}

	events := schema.Properties["events"].Items

	var missing []string

	for _, name := range events.Required {
		if _, ok := lookupTSField(typesBytes, "TraceEvent", name); !ok {
			missing = append(missing, name)
		}
	}

	if len(missing) > 0 {
		t.Fatalf("trace.schema.json event fields missing from web/src/types.ts TraceEvent: %v", missing)
	}
}

// lookupTSField scans a TypeScript file for an interface field, matching
// the camelCase name inside `interface <ownerName> { ... }` (with an
// optional `export ` prefix). The scan is line-based: it returns true
// when the field name appears at the start of a line that has been
// recognised as a property declaration.
func lookupTSField(source []byte, interfaceName, field string) (struct{}, bool) {
	var (
		depth   int
		inBlock bool
		lines   = strings.Split(string(source), "\n")
	)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inBlock {
			if strings.HasPrefix(trimmed, "interface "+interfaceName) ||
				strings.HasPrefix(trimmed, "export interface "+interfaceName) {
				inBlock = true
			}

			continue
		}

		// crude brace tracking: each '{' adds, each '}' subtracts
		for _, r := range trimmed {
			switch r {
			case '{':
				depth++
			case '}':
				depth--
			}
		}

		if depth <= 0 && strings.Contains(trimmed, "}") {
			return struct{}{}, false
		}

		if strings.HasPrefix(trimmed, field+":") || strings.HasPrefix(trimmed, field+"?:") {
			return struct{}{}, true
		}
	}

	return struct{}{}, false
}
