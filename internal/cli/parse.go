package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/pflag"
)

type Global struct {
	ProjectRoot    string
	DefinitionRoot string
	WorkspaceRoot  string
	Env            string // environment name/path; resolved before binding fallthrough
	Quiet          bool
	NoProgress     bool
	Yolo           bool
	Mode           string
	Debug          bool
	Proxy          bool
	StrictExternal bool
	Version        bool
}

// Invocation is one command: Command is the top-level name and Args the rest
// of the command path plus positional arguments. Flags holds what Cobra parsed
// for a non-launcher command and is nil for launchers.
type Invocation struct {
	Global  Global
	Command string
	Args    []string
	Flags   *pflag.FlagSet
}

// Parse reads agentpack's global options out of a launcher command line
// wherever they appear before --, except --debug after the harness name,
// which belongs to the harness (Claude has its own). --yolo stays agentpack's
// anywhere: each harness gets its own bypass flag from it. Everything else is
// left for the harness unchanged.
func Parse(arguments []string) (Invocation, error) {
	var invocation Invocation
	var rest []string
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		if argument == "--" {
			rest = append(rest, arguments[index:]...)
			break
		}
		switch argument {
		case "-q", "--quiet":
			invocation.Global.Quiet = true
		case "--no-progress":
			invocation.Global.NoProgress = true
		case "--yolo":
			invocation.Global.Yolo = true
		case "--debug":
			if len(rest) != 0 {
				rest = append(rest, argument)
			} else {
				invocation.Global.Debug = true
			}
		case "--proxy":
			invocation.Global.Proxy = true
		case "--strict-external":
			invocation.Global.StrictExternal = true
		case "--project-root", "--definition-root", "--workspace", "--mode":
			if index+1 >= len(arguments) {
				return Invocation{}, fmt.Errorf("%s requires a value", argument)
			}
			index++
			switch argument {
			case "--project-root":
				invocation.Global.ProjectRoot = arguments[index]
			case "--definition-root":
				invocation.Global.DefinitionRoot = arguments[index]
			case "--workspace":
				invocation.Global.WorkspaceRoot = arguments[index]
			default:
				invocation.Global.Mode = arguments[index]
			}
		case "--env":
			// Environment selector. A KEY=VALUE value belongs to the harness and passes through.
			if index+1 >= len(arguments) {
				return Invocation{}, fmt.Errorf("%s requires a value", argument)
			}
			if strings.Contains(arguments[index+1], "=") {
				rest = append(rest, argument)
				continue
			}
			index++
			invocation.Global.Env = arguments[index]
		default:
			if value, found := strings.CutPrefix(argument, "--project-root="); found {
				invocation.Global.ProjectRoot = value
			} else if value, found := strings.CutPrefix(argument, "--definition-root="); found {
				invocation.Global.DefinitionRoot = value
			} else if value, found := strings.CutPrefix(argument, "--workspace="); found {
				invocation.Global.WorkspaceRoot = value
			} else if value, found := strings.CutPrefix(argument, "--env="); found {
				if strings.Contains(value, "=") {
					rest = append(rest, argument)
				} else {
					invocation.Global.Env = value
				}
			} else if value, found := strings.CutPrefix(argument, "--mode="); found {
				invocation.Global.Mode = value
			} else {
				rest = append(rest, argument)
			}
		}
	}
	if len(rest) == 0 {
		return Invocation{}, fmt.Errorf("a command is required")
	}
	invocation.Command, invocation.Args = rest[0], rest[1:]
	if invocation.Command == "cursor-agent" {
		invocation.Command = "agent"
	}
	return invocation, nil
}

// stringFlag, stringsFlag and boolFlag read a flag Cobra parsed for this command.
func stringFlag(invocation Invocation, name string) string {
	value, _ := invocation.Flags.GetString(name)
	return value
}

func stringsFlag(invocation Invocation, name string) []string {
	values, _ := invocation.Flags.GetStringArray(name)
	return values
}

func boolFlag(invocation Invocation, name string) bool {
	value, _ := invocation.Flags.GetBool(name)
	return value
}
