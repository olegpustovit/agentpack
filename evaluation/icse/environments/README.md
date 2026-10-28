# External environment examples

`minimal` is an empty frozen environment with a no-checkout-write contract.
`review` adds the adjacent benign skill through a local path dependency. Copy the
whole `evaluation/icse` directory before experimenting so generated locks stay
outside this repository. Neither example needs a model key or network acquisition.

From this repository, using a built `agentpack` on PATH:

```sh
example_root=$(mktemp -d)
cp -R evaluation/icse "$example_root/examples"
mkdir "$example_root/checkout"
agentpack --env "$example_root/examples/environments/minimal" --workspace "$example_root/checkout" preflight --agent claude --strict-external --json
agentpack --env "$example_root/examples/environments/review" --workspace "$example_root/checkout" lock
agentpack --env "$example_root/examples/environments/review" --workspace "$example_root/checkout" env restore
agentpack --env "$example_root/examples/environments/review" --workspace "$example_root/checkout" preflight --agent claude --strict-external --json
agentpack --env "$example_root/examples/environments/review" --workspace "$example_root/checkout" --mode quiet preflight --agent claude --json
```

The final command fails the generated-skill contract: `quiet` intentionally omits
the skill. This demonstrates mode selection and a concrete recovery: select the
default mode again. The checkout remains empty. The second example is a local
development input; its first `lock` records the copied skill's exact content hash.
Its source folder must remain available for a cold restore. Native discovery is a
separate explicit `probe --agent claude|codex` after restore, with the effects and
version limits described in the evaluation guide.
