# Agentpack gateway

The gateway lets a developer use their existing Agentpack terminal sessions and manage pinned package configurations from a browser. It serves local assets from the Go executable and can be installed as a PWA. Node is a build/test dependency, not a production server.

Primary tasks are selecting a session, taking or releasing control, starting/stopping/restarting sessions in trusted folders, inspecting packages and saved agentpack revisions, and confirming installs or restores across known machines. Tailscale identity and the existing sharing allowlist govern peer access. Registry replication requires explicit enrollment by both peers.

Saved revisions exclude credential values; machine-local bindings supply missing configuration. Offline installs remain drafts until the user reviews a fresh destination check and confirms. A successful replica receipt concerns registry objects, not arbitrary project files or local-only package bytes.

The interface follows the supplied version-9 reference. Preserve its compact desktop controls, light and dark themes, left source list, fixed host terminal grid, mobile pane switching, and destination previews. See `SURFACE.md` for the direction contract and `README.md` for the API contract and verification commands.
