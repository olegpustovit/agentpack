# macOS privacy requests

## Attribution

I queried seven days of `com.apple.TCC` logs on the Mac mini and MacBook Air on November 24, 2026. The retained request records used here start on November 23. The counts join `AUTHREQ_CTX`, `AUTHREQ_ATTRIBUTION`, `AUTHREQ_SUBJECT`, and `AUTHREQ_PROMPTING` by daemon PID and message ID. A sandbox or framework daemon can request access on behalf of the accessing binary. When there is no separate accessing field, the table uses the requesting binary from the attribution chain. Counts include preflights and repeated requests. Parentheses show how many of those records logged prompting.

The logs show both direct filesystem requests from `ap` and requests from children whose responsible path is `~/.local/bin/ap`. They usually redact the file within the protected root. The Mini also logged a mismatch between the ad-hoc binary's current and previously stored code hashes. Apple documents consent for Desktop, Documents, Downloads, iCloud Drive, and external volumes. [Apple Platform Security](https://support.apple.com/en-mide/guide/security-pdf/secddd1d86a6/1/web/1)

| Service | Accessing binary | Responsible | Mini | Air | Path or operation |
| --- | --- | --- | ---: | ---: | --- |
| SystemPolicyDesktopFolder | `ap` | `ap` | — | 4 (3) | Desktop |
| SystemPolicyDesktopFolder | `Claude Code 2.1.330` | `ap` | — | 2 | Desktop |
| SystemPolicyDocumentsFolder | `Claude Code 2.1.330` | `ap` | — | 5 | Documents |
| SystemPolicyDocumentsFolder | `ap` | `ap` | — | 4 (3) | Documents |
| SystemPolicyDownloadsFolder | `Claude Code 2.1.330` | `ap` | — | 7 (1) | Downloads |
| SystemPolicyDownloadsFolder | `ap` | `ap` | — | 3 (2) | Downloads |
| SystemPolicyNetworkVolumes | `Claude Code 2.1.330` | `ap` | — | 1 (1) | Network volume. The mount path was private. |
| Photos | `Claude Code 2.1.330` | `ap` | — | 35 | Photos library access check |
| Photos | `ap` | `ap` | — | 4 (3) | Photos library access check |
| MediaLibrary | `ap` | `ap` | 3 (1) | 17 (4) | Media Library access check |
| MediaLibrary | `Claude Code 2.1.330` | `ap` | — | 14 | Media Library access check |
| SystemPolicyAllFiles | `ap` | `ap` | 2 | 16 | Full Disk Access check. The path was private. |
| SystemPolicyAllFiles | `Claude Code 2.1.330` | `ap` | — | 12 | Full Disk Access check. The path was private. |
| SystemPolicyAllFiles | `pxui_gallery` | `ap` | 8 | — | Full Disk Access check. The path was private. |
| SystemPolicyAllFiles | `xctrace` | `ap` | 7 | — | Full Disk Access check. The path was private. |
| SystemPolicyAllFiles | `chrome-headless-shell` | `ap` | — | 6 | Full Disk Access check. The path was private. |
| SystemPolicyAllFiles | `window_input-7f8f0a6c79137a6c` | `ap` | 5 | — | Full Disk Access check. The path was private. |
| SystemPolicyAllFiles | `window_pxui_sdl-5cfb86f8f8f57cf4` | `ap` | 3 | — | Full Disk Access check. The path was private. |
| SystemPolicyAllFiles | `bash` | `ap` | 2 | — | Full Disk Access check. The path was private. |
| SystemPolicyAllFiles | `pxui_host-9399e98906274fa7` | `ap` | 2 | — | Full Disk Access check. The path was private. |
| SystemPolicyAllFiles | `window_pxui_sdl-a4ccd99f2a5d1a30` | `ap` | 1 | — | Full Disk Access check. The path was private. |
| SystemPolicyAppBundles | `xctrace` | `ap` | 7 | — | App bundle access check |
| SystemPolicyAppBundles | `bash` | `ap` | 2 | — | App bundle access check |
| SystemPolicyAppDataDetailed | `Claude Code 2.1.330` | `ap` | — | 406 | App data access check |
| AddressBook / Calendar / Reminders | `xctrace` | `ap` | 107 | — | One count per service. Xcode and Instruments checks. |
| AddressBook / Calendar / Reminders | `com.apple.dt.instruments.dtsecurity` | `ap` | 104 | — | One count per service. Xcode and Instruments checks. |
| AddressBook / Calendar / Reminders | `sample` | `ap` | 5 | — | One count per service. Xcode and Instruments checks. |
| AddressBook / Calendar / Reminders | `xcodebuild` | `ap` | 3 | — | One count per service. Xcode and Instruments checks. |
| AddressBook / Calendar / Reminders | `lldb` | `ap` | 1 | — | One count per service. Xcode and Instruments checks. |
| ScreenCapture | `localwave` | `ap` | 975 | 1 | Screen Capture check |
| ScreenCapture | `localwave-playback` | `ap` | 290 | — | Screen Capture check |
| ScreenCapture | `window_input-7f8f0a6c79137a6c` | `ap` | 191 | — | Screen Capture check |
| ScreenCapture | `lw_bin-b1d401332f56b990` | `ap` | 68 | — | Screen Capture check |
| ScreenCapture | `lw_apple_sys-391dd182ed5b88d0` | `ap` | 67 | — | Screen Capture check |
| ScreenCapture | `splice_mcp-5f4ec5231960d4fc` | `ap` | — | 16 | Screen Capture check |
| ScreenCapture | `window_input-e0eb74fed00d872d` | `ap` | 15 | — | Screen Capture check |
| ScreenCapture | `present-spike` | `ap` | 8 | — | Screen Capture check |
| ScreenCapture | `looks-2f54f82c31d71f81` | `ap` | — | 6 | Screen Capture check |
| ScreenCapture | `lw_apple_sys-467df4a17c09d442` | `ap` | 6 | — | Screen Capture check |
| ScreenCapture | `native-1936983b8c59d33f` | `ap` | — | 6 | Screen Capture check |
| ScreenCapture | `lw_bin-ef93bd9460e2172d` | `ap` | 5 | — | Screen Capture check |
| ScreenCapture | `motion-d73f9bb8a3b8aea4` | `ap` | — | 3 | Screen Capture check |
| ScreenCapture | `r7b-display-state` | `ap` | 1 | — | Screen Capture check |
| ListenEvent | `gpu_pxui_render-23d375554b61a664` | `ap` | 10517 | — | Input Monitoring check |
| ListenEvent | `gpu_pxui_render-b2846057db0b5c6a` | `ap` | 3059 | — | Input Monitoring check |
| ListenEvent | `pxui_gallery` | `ap` | 1747 | — | Input Monitoring check |
| ListenEvent | `localwave` | `ap` | 1699 | 1 | Input Monitoring check |
| ListenEvent | `window_pxui_sdl-a4ccd99f2a5d1a30` | `ap` | 1665 | — | Input Monitoring check |
| ListenEvent | `pxui_host-b032741e22c78665` | `ap` | 1633 | — | Input Monitoring check |
| ListenEvent | `pxui_host-9399e98906274fa7` | `ap` | 1265 | — | Input Monitoring check |
| ListenEvent | `gpu_pxui_parity-8a50e8cc74061131` | `ap` | 1019 | — | Input Monitoring check |
| ListenEvent | `abi_bench_static_gallery` | `ap` | 945 | — | Input Monitoring check |
| ListenEvent | `pxui_host-a695a8810a3c1cbf` | `ap` | 889 | — | Input Monitoring check |
| ListenEvent | `window_pxui_sdl-5cfb86f8f8f57cf4` | `ap` | 887 | — | Input Monitoring check |
| ListenEvent | `xctrace` | `ap` | 738 | — | Input Monitoring check |
| ListenEvent | `pxui_host-514da5ecf95fac62` | `ap` | 725 | — | Input Monitoring check |
| ListenEvent | `window_input-7f8f0a6c79137a6c` | `ap` | 713 | — | Input Monitoring check |
| ListenEvent | `chrome-headless-shell` | `ap` | — | 501 | Input Monitoring check |
| ListenEvent | `localwave-playback` | `ap` | 410 | — | Input Monitoring check |
| ListenEvent | `gpu_gallery-d909247662b51549` | `ap` | 317 | — | Input Monitoring check |
| ListenEvent | `gpu_pxui_render-049a6b803aa3ee90` | `ap` | 284 | — | Input Monitoring check |
| ListenEvent | `DTServiceHub` | `ap` | 206 | — | Input Monitoring check |
| ListenEvent | `looks-2f54f82c31d71f81` | `ap` | — | 135 | Input Monitoring check |
| ListenEvent | `gpu_pxui_parity-3c74ffb2b04bde5d` | `ap` | 130 | — | Input Monitoring check |
| ListenEvent | `pxui_host-fccc11b291813dd0` | `ap` | 122 | — | Input Monitoring check |
| ListenEvent | `VTDecoderXPCService` | `ap` | — | 90 | Input Monitoring check |
| ListenEvent | `gpu_gallery-dcd448d98f6df111` | `ap` | 86 | — | Input Monitoring check |
| ListenEvent | `gpu_all-ff0d7d65db50b30a` | `ap` | 85 | — | Input Monitoring check |
| ListenEvent | `gpu_pxui_parity-c88ee9ddf274d4cf` | `ap` | 78 | — | Input Monitoring check |
| ListenEvent | `window_input-e0eb74fed00d872d` | `ap` | 58 | — | Input Monitoring check |
| ListenEvent | `native-1936983b8c59d33f` | `ap` | — | 54 | Input Monitoring check |
| ListenEvent | `gpu_gallery-6c605176f3439f05` | `ap` | 50 | — | Input Monitoring check |
| ListenEvent | `motion-d73f9bb8a3b8aea4` | `ap` | — | 45 | Input Monitoring check |
| ListenEvent | `gpu_gallery-cc7654db943553ad` | `ap` | 37 | — | Input Monitoring check |
| ListenEvent | `window_pxui_sdl-28c0839546ba398d` | `ap` | 35 | — | Input Monitoring check |
| ListenEvent | `gpu_gallery-12c4b5d36e96ddb6` | `ap` | 30 | — | Input Monitoring check |
| ListenEvent | `window_sdl3-463d7c2af11908bf` | `ap` | 26 | — | Input Monitoring check |
| ListenEvent | `gpu_all-9ce72987bd036324` | `ap` | 20 | — | Input Monitoring check |
| ListenEvent | `abi_bench-c9fa0b308dc6595c` | `ap` | 17 | — | Input Monitoring check |
| ListenEvent | `abi_bench-7f4d14fed86e9c7b` | `ap` | 15 | — | Input Monitoring check |
| ListenEvent | `window_sdl3-fe27133dee373693` | `ap` | 14 | — | Input Monitoring check |
| ListenEvent | `ibtoold` | `ap` | 13 | — | Input Monitoring check |
| ListenEvent | `present-spike` | `ap` | 9 | — | Input Monitoring check |
| ListenEvent | `AssetCatalogAgent-AssetRuntime` | `ap` | 6 | — | Input Monitoring check |
| ListenEvent | `system_profiler` | `ap` | 6 | — | Input Monitoring check |
| ListenEvent | `gpu_all-e6fc7fb05141f90e` | `ap` | 4 | — | Input Monitoring check |
| ListenEvent | `gpu_pxui_render-9719ab26071d9b8b` | `ap` | 4 | — | Input Monitoring check |
| ListenEvent | `pxui_host-5ccd77e196d6e5a6` | `ap` | 4 | — | Input Monitoring check |
| ListenEvent | `pxui_host-8317f23845ced4c3` | `ap` | 4 | — | Input Monitoring check |
| ListenEvent | `r7b-activate` | `ap` | 4 | — | Input Monitoring check |
| ListenEvent | `abi_bench-4eab25107ccbe8df` | `ap` | 2 | — | Input Monitoring check |
| ListenEvent | `abi_bench-a6d84ff99ee4f9c0` | `ap` | 2 | — | Input Monitoring check |
| ListenEvent | `osascript` | `ap` | 2 | — | Input Monitoring check |
| ListenEvent | `r7b-display-state` | `ap` | 2 | — | Input Monitoring check |
| ListenEvent | `window_sdl3-e556ed62ce90c25f` | `ap` | 2 | — | Input Monitoring check |
| FileProviderDomain | `sandboxd` broker, during Claude Code filesystem requests | `ap` | — | 1 prompt | iCloud Drive. The originating binary for the indirect request was private. |
| AppleEvents | System Events broker, near an `osascript` request | `ap` | 1 prompt | — | Automation request to System Events. The prompt timed out after 120 seconds. |

The Mini had 29,585 Input Monitoring requests and 1,626 Screen Capture requests in the joined records. The Screen Capture requests came from LocalWave and its development binaries. The Contacts, Calendar, and Reminders checks came from Xcode, Instruments, and related tools. The Air's Claude Code requests include one prompt each for Downloads and a network volume. The logs show neither the invocation arguments nor the particular MCP server behind a child request.

There were no matching sandbox or kernel deny lines for `ap` on either host. The logs did not identify a removable-volume access by `ap` or distinguish individual Music and Movies paths. Both Macs required a password for sudo, so I skipped `fs_usage`.

## Access policy

Home discovery used to descend into visible children of the home directory. The folder picker also statted each child's manifest and Git marker and resolved symlink targets to decide which entries were folders. The browser's project, worktree, and Library views could stat recents, read their definitions or run Git. A subscribed browser started a 10-second projects watcher that checked folder identity and statted every recent `pack.lock`. Staging cleanup similarly statted former project paths to find orphaned directories. These are automatic access paths established in source. The TCC logs identify the binary and service without Go call stacks.

On macOS the shared path check stops before inspecting a protected component, including a link target, a case variant, or a path through the APFS Data volume. The rule covers Desktop, Documents, Downloads, Pictures, Movies, Music, iCloud and File Provider storage, protected app data under Library, and mounts under `/Volumes` and `/Network`. It also covers media library bundles outside their usual folders. The parent folder picker can display protected child names. A request for a specific folder can read that folder. Project and recent lists keep saved rows selectable and defer live details. The projects watcher always uses this rule, so a saved recent or a selection made before a gateway restart cannot cause a lockfile probe. Choosing a project, opening Project Settings, or asking for Git details does a scoped read. Session starts still validate the folder identity.

The worktree checks also cover Git's `.git` and `commondir` pointers. Protected workspaces are excluded from orphan cleanup and automatic recovery that would reopen them. Existing session history stays visible and the person can resume the session. Browser startup and passive reads use saved state for account limits, account status and native sessions. On macOS, **Refresh** under Limits and **Check agents** under Machines run the corresponding native probes. The gateway continues to resolve a directly selected project or agent on demand.

Cursor's Application Support links and Claude, Codex, and Grok's native state are part of an explicitly requested launch. Gateway usage totals come from stored events. The launch fingerprint already reads configuration and authored resources, so it did not need a new scan or a new input. The path check covers Agentpack's automatic reads of the listed roots. Third-party programs run with their own behavior, including commands, extensions and configuration the person selected.

I left process responsibility unchanged. The macOS SDK exposes the responsibility symbol in its libraries but omits it from public `spawn.h`. Go's Darwin `os/exec` path uses fork and exec, and its `SysProcAttr` has no place to supply a spawn attribute. Replacing worker startup with a private spawn API would risk process-group and terminal behavior and would still leave native tool access itself in place. macOS may attribute a deliberate child action to `ap`. [Apple's public spawn attributes](https://developer.apple.com/library/archive/documentation/System/Conceptual/ManPages_iPhoneOS/man3/posix_spawnattr_init.3.html)

## Verification

The native build used the Mac mini's Go 1.27.3 toolchain, Apple SDK and pinned Darwin arm64 cr-sqlite archive, with the staged UI embedded. It produced an ad-hoc-signed arm64 binary. An isolated launchd gateway used a fresh data directory, disabled sharing, a distinct loopback port, and synthetic recents under the real Documents, Desktop, Music, iCloud and volume roots. The test kept SSE open across seven rounds of discovery and API reads and changed a safe lockfile to verify that the projects watcher was active. The production gateways and TCC settings were untouched.

The final run lasted 183 seconds, from 12:41:51 to 12:44:54 CET on November 24. The event stream had two project events, including the safe lockfile change. The APIs deferred protected recents, the symlink into Documents, a worktree with Git metadata under Documents, and passive session Git status. A stub native agent recorded no invocations. `log show --info --debug` for `com.apple.TCC`, filtered on the isolated executable path and PID 89704 over that interval, returned the header and zero records. The isolated launchd job and test data were removed.

`make ci`, native Darwin `go vet ./...` and the focused Darwin tests passed. So did the UI build, lint, 360 unit tests and 45 Chromium tests for sessions, project settings, machines and limits.

Linux x86-64 startup checks ran five interleaved baseline and branch samples, with `GOMAXPROCS=4`, a separate temporary directory and the existing 32 MiB fixture. Run `go test ./internal/sync -run '^$' -bench '^(BenchmarkMaterializationHome|BenchmarkCodexSetup)$' -benchmem -count=5 -benchtime=250ms` on each revision.

| Benchmark | 76fc0751 median | Branch median |
| --- | ---: | ---: |
| MaterializationHome | 0.484 ms | 0.479 ms |
| CodexSetup/cold | 12.476 ms | 10.330 ms |
| CodexSetup/warm | 0.166 ms | 0.165 ms |

The cold timings varied between samples. These are development-host measurements. A 33-second loopback CPU profile of the Linux baseline with an SSE subscriber collected 10 ms of samples in SQLite. A 35-second profile during the final Mac mini run collected 240 ms of CPU samples, or 0.68% of one core, while the API checks were active.
