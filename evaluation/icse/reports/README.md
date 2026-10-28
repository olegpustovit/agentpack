# Recorded offline reports

Generate these with the built CLI:

```sh
python3 evaluation/icse/run.py --binary /absolute/path/to/agentpack --trace --output evaluation/icse/reports
```

`clean.json`, `violation.json`, and `unknown.json` are actual static CLI reports on
disposable fixtures. `stale-receipt.json` is the CLI's rejection of an explicitly
synthetic receipt with obsolete identities and no observed properties.
`support.json` is the CLI's sanitized export of that synthetic receipt. It is a
format and redaction example, not recorded native evidence. Temporary paths are
replaced by `<fixture>`, `<fixtures>`, and `<agentpack>` after assertions pass.

`summary.json` records the exits and filesystem/process/network checks from the
run. Real-native versioned run summaries are the separate `native-evidence-*.json`
files in the parent directory.
