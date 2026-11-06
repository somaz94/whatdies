# CLAUDE.md

Go CLI that runs as a Claude Code `PreToolUse` hook and decides ask or deny from what an infrastructure command would actually destroy. Public repository: nothing here may name real infrastructure.

<br/>

## Build & Test

```bash
make build           # Build binary to ./bin/whatdies
make test            # Unit tests with race detector and coverage
make cover           # Coverage report
make lint            # golangci-lint
make cover-check     # Fail below the coverage minimum
make demo            # End to end against a throwaway kind cluster
make e2e             # The demo, failing if the sequence of verdicts changes
```

Keep coverage at 90% or above, excluding `cmd/main.go`. `make cover-check` enforces it in CI.

<br/>

## Key Concepts

- **The verdict comes from state, not text.** That is the only reason this tool exists alongside text-matching guards. A rule that could be a regex over the command belongs in one of those, unless it is the static ask that covers what cannot be looked up
- **Never emit `allow`.** `verdict.Decision` has no such value on purpose: it would skip the permission prompt
- **A false "no decision" is the worst bug here.** `internal/engine/engine_test.go` `TestEverySpellingOfADestructiveCommandIsCaught` is the corpus. A new bypass goes there first. `TestOrdinaryCommandsAreLeftAlone` is its counterweight: a guard that prompts on `make test` gets uninstalled
- **Never run anything the agent has not been allowed to run.** So no `terraform plan` from the hook, no probe with a kubeconfig, server or binary the command chose (`noProbe`, `evaluateApply`), and `kubectl get` plus `config current-context` only. Every name passed to a probe goes through `validName`, including names from the cluster: `kubectl delete pv/--kubeconfig=x` once loaded that config and ran its exec plugin before approval (found in review, 2026-11-05). Saved plans are decoded in process (`planfile.go`) for the same reason: `terraform show` started provider binaries from the directory the plan is applied from (replaced 2026-11-06)
- **Fail closed at every layer** (`docs/verdicts.md`, "When a check fails"). Verified against Claude Code by observing a marker file: a hook that timed out, crashed or was missing let the command run, until `onFailure: block` or `|| exit 2` was added (2026-11-05)
- **Open messages by saying a safety hook stopped the command** (`verdict.Message`). Given only the reason, an agent took it for terraform's own odd output, re-ran the command and inspected the binary. With the opening sentence it followed the advice (2026-11-05)
- **A script's text comes from every write in the line and from the disk.** A write can be conditional (`[ -f x ] || echo ok > x`) or an append, so one that looks final must not hide the file (2026-11-05)
- **`TF_CLI_ARGS` from the user's environment is ignored, set by the command it is ask.** The first version treated both as ask and blocked every plan on a real shell that sets it (2026-11-05)
- **Advice must be something the tool can do.** `terraform plan -out=.terraform/x` fails when `.terraform/` does not exist, which is why `planPath` checks (2026-11-05)
- **`stateful.yaml` and `guards.yaml` are data.** A new resource type or guard attribute is a line there. A type missing from `stateful.yaml` still gets ask; a guard missing from `guards.yaml` is not seen, and is the only gap the file closes
- **Plan values are opened only for a type in `guards.yaml`, and only its guard keys are decoded** (`planvalues.go`). Every other value is stepped over by length, so a secret in the plan is never held as a string (2026-11-06)
- **A plan field, version, action or address the decoder does not know is ask.** Field numbers were observed in plans terraform wrote, not copied from its source, so a format change must surface as ask, never as no decision. That is also why `applyable` is not trusted: a plan with no changes has it false and still applies (2026-11-06)
- **Decode only the plan fields a verdict needs** (`planfile.go`, and `planvalues.go` for guard keys). Plans hold secrets in clear text, and what is never decoded cannot reach a message. `verdict.AskBecause` and `DenyBecause` clean every line, since text quoted from a plan, a cluster or the command reaches an agent. Calling `clean` at each quote missed some (2026-11-05)

<br/>

## Fixtures

Test data and the demo are synthetic. Do not copy a real plan, cluster name, address or account into them.

The saved plans in `internal/terraform/testdata/plans/` are written by real terraform through `generate.sh`, and `TestSavedPlansMatchTerraformShow` checks the decoder against the `terraform show -json` saved next to each. Regenerate them when a new Terraform release changes the format.

<br/>

## Local Runs

`examples/demo/demo.sh` must keep its own `KUBECONFIG`: `kind create cluster` otherwise switches the current context of the user's real kubeconfig.
