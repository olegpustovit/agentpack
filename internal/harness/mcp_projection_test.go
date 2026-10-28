package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateMCPProjectionInputsDetectsMutationAndAddition(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.json")
	if err := os.WriteFile(path, []byte("seed"), 0o644); err != nil {
		t.Fatal(err)
	}
	input, err := ReadMCPInput(path)
	if err != nil {
		t.Fatal(err)
	}
	projection := MCPProjection{Inputs: []MCPProjectedInput{input}}
	if err := ValidateMCPProjectionInputs([]MCPProjection{projection}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateMCPProjectionInputs([]MCPProjection{projection}); err == nil || !strings.Contains(err.Error(), "changed after planning") {
		t.Fatalf("changed seed passed validation: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := ValidateMCPProjectionInputs([]MCPProjection{projection}); err == nil {
		t.Fatal("removed seed passed validation")
	}
	if err := ValidateMCPProjectionInputs([]MCPProjection{{Inputs: []MCPProjectedInput{{Path: path}}}}); err != nil {
		t.Fatalf("input that remains absent failed validation: %v", err)
	}
	if err := os.WriteFile(path, []byte("appeared"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateMCPProjectionInputs([]MCPProjection{{Inputs: []MCPProjectedInput{{Path: path}}}}); err == nil || !strings.Contains(err.Error(), "appeared after planning") {
		t.Fatalf("appearing seed passed validation: %v", err)
	}
}
