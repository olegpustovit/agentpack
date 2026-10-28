# Configuration JSON formats

- `contract.v1.schema.json` describes the input contract. Go validation also rejects semantic conflicts, duplicate requirement IDs, unsupported category/property pairs, and unscoped allowances.
- `report.v1.schema.json` describes preflight stdout. `source_identity` maps to the receipt's `source_id`; `expected_target` holds the static target identity while `target` is the selected adapter name. Missing inputs can prevent freshness identities from being populated, so those fields are optional in an error report.
- `receipt.v1.schema.json` describes saved native probe receipts. Go validation also checks duplicate coverage/property identities and freshness prerequisites. A structurally valid receipt does not establish a trustworthy or current observation.

Report and receipt schema v1 allow additive fields. Future major versions are rejected. Contract inputs reject unknown fields to catch policy typos.

Findings retain the existing `message`, `source`, and `remedy` fields. `consequence`, `artifact_id`, `property`, and `related_refs` carry known impact and identities when available. `remediation.message` mirrors the plain-text remedy; optional `remediation.argv` is an argument vector for a concrete implemented command, never a shell expression or a parsed copy of free-form prose. Missing structured identities mean the diagnostic did not establish them.

`examples/receipt.valid.json` is an illustrative shape with unknown evidence, not a native run. `examples/receipt.invalid-future.json` must be rejected by the schema and loader. The duplicate-ID contract is structurally valid JSON but fails semantic validation. Real generated preflight samples are in `evaluation/icse/reports/`; their README states the measured scope and redaction.

Run `go test ./internal/environment -run 'TestPublished'` to validate the shipped examples and reports using the existing JSON Schema library and the production Go validators.
