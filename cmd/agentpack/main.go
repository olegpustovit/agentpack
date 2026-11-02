package main

import (
	"context"
	"os"

	"github.com/olegpustovit/agentpack/internal/cli"
	"github.com/olegpustovit/agentpack/internal/paths"
)

func main() {
	paths.RestoreUserEnvironment()
	code, _ := cli.NewRunner().Execute(context.Background(), os.Args[1:])
	os.Exit(code)
}
