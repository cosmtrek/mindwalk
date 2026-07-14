package integration

import "testing"

func TestCatalogIsDisabledAndActionContractsCannotExecute(t *testing.T) {
	contracts := Catalog()
	if len(contracts) != 7 {
		t.Fatalf("contracts = %#v", contracts)
	}
	want := map[string]string{"discord": "DRY_RUN", "github": "READ_ONLY", "llama-cpp": "READ_ONLY", "ollama": "READ_ONLY", "openai-export": "READ_ONLY", "openclaw": "DRY_RUN", "otlp-loopback": "RECEIVE_ONLY"}
	for _, contract := range contracts {
		if contract.Enabled || contract.Mode != want[contract.ID] {
			t.Fatalf("unsafe contract = %#v", contract)
		}
		result := Preview(contract.ID)
		if result.Status != "DISABLED" {
			t.Fatalf("preview %s = %#v", contract.ID, result)
		}
	}
}

func TestUnknownIntegrationFailsClosed(t *testing.T) {
	if result := Preview("not-real"); result.Status != "UNKNOWN" {
		t.Fatalf("result = %#v", result)
	}
}
