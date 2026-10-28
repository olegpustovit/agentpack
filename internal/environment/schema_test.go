package environment

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

func publishedSchema(t *testing.T, name string) *jsonschema.Resolved {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "schemas", name+".v1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func TestPublishedContractAndReceiptExamples(t *testing.T) {
	for _, test := range []struct {
		name          string
		kind          string
		schemaValid   bool
		semanticValid bool
	}{
		{"contract.valid.json", "contract", true, true},
		{"contract.invalid-duplicate.json", "contract", true, false},
		{"receipt.valid.json", "receipt", true, true},
		{"receipt.invalid-future.json", "receipt", false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("..", "..", "schemas", "examples", test.name))
			if err != nil {
				t.Fatal(err)
			}
			var document any
			if err := json.Unmarshal(data, &document); err != nil {
				t.Fatal(err)
			}
			if err := publishedSchema(t, test.kind).Validate(document); (err == nil) != test.schemaValid {
				t.Fatalf("schema validation: %v (want valid=%v)", err, test.schemaValid)
			}
			if test.kind == "contract" {
				var contract Contract
				err = decodeContract(data, &contract)
				if err == nil {
					err = ValidateContract(contract)
				}
			} else {
				var receipt Receipt
				err = json.Unmarshal(data, &receipt)
				if err == nil {
					err = ValidateReceipt(receipt)
				}
			}
			if (err == nil) != test.semanticValid {
				t.Fatalf("Go validation: %v (want valid=%v)", err, test.semanticValid)
			}
		})
	}
}

func TestPublishedPreflightReportsMatchSchema(t *testing.T) {
	schema := publishedSchema(t, "report")
	failed := Report{SchemaVersion: 1, Phase: "preflight", OverallStatus: "error", Findings: []Finding{{Code: "PREFLIGHT_ERROR", Severity: SeverityError, Message: "invalid policy"}}}
	data, err := json.Marshal(failed)
	if err != nil {
		t.Fatal(err)
	}
	var failure any
	if err := json.Unmarshal(data, &failure); err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(failure); err != nil {
		t.Fatalf("report before inputs can be loaded: %v", err)
	}
	for _, name := range []string{"clean", "violation", "unknown", "stale-receipt"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("..", "..", "evaluation", "icse", "reports", name+".json"))
			if err != nil {
				t.Fatal(err)
			}
			var document map[string]any
			if err := json.Unmarshal(data, &document); err != nil {
				t.Fatal(err)
			}
			if err := schema.Validate(document); err != nil {
				t.Fatal(err)
			}
			document["schema_version"] = 2
			if err := schema.Validate(document); err == nil {
				t.Fatal("future report major version accepted")
			}
		})
	}
}
