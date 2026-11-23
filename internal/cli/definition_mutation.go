package cli

func definitionMutation(invocation Invocation) bool {
	switch invocation.Command {
	case "add", "remove", "update", "lock":
		return true
	case "sync":
		return !boolFlag(invocation, "dry-run") && !boolFlag(invocation, "verify-only")
	case "mcp":
		return first(invocation.Args) == "add" || first(invocation.Args) == "remove"
	case "mode":
		switch first(invocation.Args) {
		case "create", "delete", "enable", "disable", "base":
			return true
		}
	case "env":
		return first(invocation.Args) == "restore"
	case "eject":
		return !boolFlag(invocation, "remove")
	}
	return false
}
