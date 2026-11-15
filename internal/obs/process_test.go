package obs

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProcessTraceInheritsOnlyValidIdentifiers(t *testing.T) {
	const id = "0123456789abcdef0123456789abcdef"
	t.Setenv("AGENTPACK_TRACE", id)
	if got := ParseTraceParent(TraceParent(ProcessTrace(context.Background()))).ID; got != id {
		t.Fatalf("inherited trace=%s", got)
	}
	parent := WithTrace(context.Background(), "00-abcdefabcdefabcdefabcdefabcdefab-abcdefabcdefabcd-01")
	if TraceParent(ProcessTrace(parent)) != TraceParent(parent) {
		t.Fatal("environment replaced request trace")
	}
	t.Setenv("AGENTPACK_TRACE", strings.Repeat("0", 32))
	if got := ParseTraceParent(TraceParent(ProcessTrace(context.Background()))).ID; got == strings.Repeat("0", 32) {
		t.Fatal("accepted invalid process trace")
	}
}

func TestGoRecordsAndRethrowsPanic(t *testing.T) {
	if os.Getenv("AGENTPACK_OBS_PANIC_CHILD") == "1" {
		spool, err := OpenSpool(os.Getenv("AGENTPACK_OBS_PANIC_HOME"), SpoolOptions{Proc: "gateway"})
		if err != nil {
			panic(err)
		}
		SetDefault(spool, BuildMetadata("test", "node", "gateway"))
		Go("fixture.loop", func() { panic("fixture failure") })
		select {}
	}
	home := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestGoRecordsAndRethrowsPanic$")
	command.Env = append(os.Environ(), "AGENTPACK_OBS_PANIC_CHILD=1", "AGENTPACK_OBS_PANIC_HOME="+home)
	output, err := command.CombinedOutput()
	if err == nil || ctx.Err() != nil || !strings.Contains(string(output), "fixture failure") {
		t.Fatalf("panic was not rethrown: %v", err)
	}
	files, err := filepath.Glob(filepath.Join(home, "logs", "spool", "*.ndjson"))
	if err != nil || len(files) != 1 {
		t.Fatalf("panic spools=%v err=%v", files, err)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	var event Event
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatal(err)
	}
	if event.Kind != "panic" || event.Name != "fixture.loop" || event.Stack == "" || event.Fingerprint == "" {
		t.Fatalf("panic event %+v", event)
	}
}
