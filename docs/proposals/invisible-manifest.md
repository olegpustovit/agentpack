# Invisible project configuration

Status: proposal. This describes a CLI change; the current CLI still requires its existing initialization flow.

Agentpack should launch in an uninitialized workspace without creating `agentpack.toml` or `pack.lock`. Installing the first package creates a private definition under the existing Agentpack home. `agentpack init` promotes that definition into the repository. No new home directory, daemon, database, or configuration format is needed.

## User behavior

| Action | Result |
| --- | --- |
| Launch with no project configuration | Run with native user/project configuration; create no manifest, lock, or empty private definition. Normal session/auth/staging state remains separate. |
| Install the first package through CLI or browser | Resolve and validate the package, then store `agentpack.toml` and `pack.lock` under `$AGENTPACK_HOME/projects/<workspace-hash>/definition/`. Leave the workspace unchanged. |
| Install more packages, edit modes or MCP | Update that same private definition through the normal services and integrity checks. |
| Launch again | Reuse the private definition, selected mode, and ordinary launch synchronization. |
| Run `agentpack init` | Publish the private manifest and lock in the repository root, preserving all pins, modes, MCP definitions, and relative-path meaning. With no private definition, create the existing empty project configuration. |
| Clone on another machine | Only checked-in configuration travels. Private definitions stay on their originating machine until promoted or exported. |

“Create nothing” applies to project configuration, not credentials or session history required to run a harness. There is no promise that an uninitialized launch leaves the whole user profile untouched.

## Resolution and ownership

Reuse `paths.Context` and the existing definition/workspace split. Resolve explicit `--env` or `--definition-root` first, then explicit workspace bindings, then a repository manifest discovered through the existing ancestor search, then the workspace's private definition. Invalid explicit selectors remain errors. An existing repository manifest wins over private state; never merge two definitions silently.

Use the existing canonical workspace identity and project hash rather than its basename. Subdirectories in one repository share the repository root; independent repositories and worktrees keep separate private state. Outside a repository, the canonical launch directory is the workspace. Keep workspace discovery separate from manifest discovery so a private definition never becomes the harness working directory.

The first mutation creates the definition lazily under the existing mutation lock. Failed resolution must not leave an empty definition that later shadows native configuration. CLI and browser installs must use the same context resolver. Package integrity, pinned launch behavior, registry capture, effective-mode checks, and restart decisions keep their existing service boundaries.

## Promotion safety

`init` takes the private-definition and destination mutation locks in a stable order, validates the source lock, and refuses to overwrite either destination file. Prepare both files in the destination filesystem before publication. If publication fails, preserve the private source and remove only files created by that invocation. Crash recovery must detect an incomplete pair and explain recovery; two file renames alone are not an atomic pair.

Private relative filesystem dependencies must be rewritten relative to the repository destination, preserving the same source directory. Preserve locked cache identities and content hashes. Verify the promoted pair using the normal readers before marking promotion complete. Keep the private source as recovery data until publication succeeds; subsequent resolution uses the repository files. Never delete credentials, cached packages, running generations, or session history.

Explicit external environments are not implicitly promoted: `init` must explain that the workspace is bound to an external definition and direct the user to the existing export workflow. Promotion of an implicit definition does not restart agents or replace their live generation paths.

## Implementation scope and acceptance

Touch workspace/context resolution in `internal/paths` and `internal/cli/run.go`; reuse `internal/environment` binding and identity conventions, manifest/lock writers, definition mutation locking, and existing launch/install services. Audit all callers of `resolveInvocationContext`, including network package installation, registry save/restore, mode and MCP commands, sync, and launch. Update command help and initialization documentation with the changed default.

Required tests: uninitialized launch creates neither workspace nor private project files; first install stays outside the repository; repeated launch/install reuses pins; repository discovery and explicit selectors retain precedence; independent worktrees do not share implicit definitions; promotion preserves modes/MCP/pins and path dependencies; existing destination files are never overwritten; failed install, concurrent promotion/install, interrupted publication, and malformed locks preserve recoverable state. Include a browser-install integration check and a real harness launch from an uninitialized repository.

Do not implement a second package store or auto-initialize every launch. The existing split-root model already supplies most of the mechanism; the new work is lazy selection and safe promotion.
