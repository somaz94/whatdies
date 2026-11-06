# Verdicts

How whatdies reaches each decision. There are three outcomes, and the strictest one across all the steps of a command line wins:

- **Deny**: the command does not run, and the agent is told what to do instead.
- **Ask**: Claude Code prompts you. In a headless run (`claude -p`) an ask is refused, with the reason passed to the agent, and that held under `bypassPermissions` too.
- **No decision**: your normal permission rules apply.

<br/>

## Reading the command

The command is parsed as bash, not searched as text, so these are all the same `terraform destroy`:

```bash
\terraform destroy
sudo env A=1 timeout 60 terraform destroy
bash -c 'cd envs && terraform destroy'
for x in 1; do terraform destroy; done
echo $(terraform destroy)
```

`git commit -m "terraform destroy"` and `grep "kubectl delete" docs` are not, because nothing runs those words.

| Shape | What happens |
|---|---|
| Wrappers such as `env`, `sudo`, `timeout`, `nohup`, `command` | Read through |
| `bash -c`, `eval`, here-documents fed to a shell | The text is parsed as a command, up to a depth limit |
| `xargs`, `parallel`, `find -exec` | The command is read, and its run-time arguments count as unknown |
| `parallel` with no command word | `parallel ::: 'x'` is read as the command `x`. Commands read from a file or a pipe are ask |
| An unknown wrapper, such as `aws-vault exec prod -- terraform destroy` or `ssh host "kubectl delete ns x"` | Ask, if its arguments contain an infrastructure tool and a destructive verb |
| `./deploy.sh`, `bash deploy.sh`, `~/deploy.sh`, `bash "$HOME/deploy.sh"`, `python3 run.py`, `python3 -c '...'`, `perl -e '...' -e '...'` | The program text is searched one level deep. A hit is ask |
| `bash -s x <<'EOF'`, `bash /dev/stdin <<< '...'`, `. /dev/stdin` | Read like the here-document it is. The words after `-s` are arguments, not a script |
| `ssh host <<'EOF'`, `docker exec -i c sh <<'EOF'` | The here-document is searched like the wrapper's arguments |
| `echo '...' > x.sh && bash x.sh`, or a here-document written to a file and then run | Every write to it in the line is searched, and so is the file on disk, since a write can be conditional or an append. Anything else that creates the script just before running it, such as `curl -o`, is ask |
| `git rebase -x`, `git bisect run`, `git submodule foreach`, a `git -c` setting that holds a command, `GIT_SSH_COMMAND` and similar variables | The command git would run is searched. Other git commands are left alone, so a commit message can say anything |
| `sed 's/x/y/e'`, `sed '1e ...'`, `rg --pre` | The command they would run is searched. Other uses are left alone, since search text is not a command |
| `make deploy` | The target's recipe and prerequisites are searched, with variables, `$@`, `%` patterns, `define` and includes filled in. A hit is ask, and `make test` stays silent. A computed include is not read, so the target may be missed |
| `eval "$X"`, `bash -c "$X"`, `curl ... \| bash`, `curl ... \| bash -s`, `python3 "$SCRIPT"`, `git rebase -x "$X"`, `$BIN delete ...` | Ask, since the command is not knowable. `source "$F"` is the exception: it stays silent, because `source "$HOME/.nvm/nvm.sh"` is too common to prompt on |
| `k`, `tf`, `tg`, `kubecolor` | Treated as `kubectl`, `terraform`, `terragrunt` and `kubectl` |

Steps earlier in the same line count. After a computed `cd`, a `cd` that may not have run (`false && cd x; ...`, or inside an `if`), a `kubectl config use-context`, a `terraform workspace select`, an `aws eks update-kubeconfig`, a `source` or an `env -u KUBECONFIG`, what could be looked up now would describe the wrong target, so the verdict is ask with no lookup.

Lines that only print or define text are skipped when searching a program, so `echo "usage: terraform destroy"` and `alias kd='kubectl delete'` do not ask. `--help` on a tool whatdies knows is no decision, because it prints and exits.

<br/>

## Terraform

Saving a plan is the only way to know what an apply does, so everything turns on it.

| Command | Verdict |
|---|---|
| `plan`, `init`, `validate`, `fmt`, `show`, `output`, `state list` and other read-only commands | No decision |
| `apply` or `destroy` with no saved plan, including `echo yes \| terraform apply` | Deny, with instructions |
| `terraform plan -out=x && terraform apply x`, or any earlier step that may write the plan file | Deny. The hook runs before the line does, so it would read the previous plan. The agent is told to split the calls |
| `apply <planfile>` | Decided from the plan, below |
| `state rm`, `state mv`, `taint`, `import`, `force-unlock`, `workspace delete`, `init -migrate-state`, `init -force-copy`, `test` | Ask |
| `apply` with an option whatdies does not know, or a computed argument | Ask |
| `TF_CLI_ARGS*`, `TF_WORKSPACE` or `TF_DATA_DIR` set by the command itself | Ask. The same variables in your own environment are ignored, since they cannot change what a saved plan does |
| `tofu`, `terragrunt` | Read-only commands pass, and anything else is ask. Their plans are not read yet |

whatdies reads the saved plan file itself rather than running `terraform show`, so nothing in the working directory runs before you approve. From its list of changes:

| `change.actions` | Resource type in [`stateful.yaml`](../internal/registry/stateful.yaml) | Any other type |
|---|---|---|
| `delete`, or `delete` with `create` | Deny | Ask |
| `forget` | Ask | Ask |
| `create`, `update`, `read`, `no-op` | No decision, unless an update turns off a guard (below) | No decision, unless an update turns off a guard (below) |

Also ask: an errored plan, a plan not marked complete (one saved with `-target` is not), provider actions, and provisioners when anything changes, since none of those are described by the change list. Provisioners are found by searching the configuration saved inside the plan, so one that is only mentioned in a comment counts too. Drift and output changes are ignored, because they describe the past and outputs.

Anything in the plan file that whatdies does not recognise is ask: a format version, a section such as deferred changes, an action, or a resource address it cannot read. So is a plan with more changes than it will read. A newer Terraform can therefore weaken a deny to ask, but never to no decision.

An in-place `update` that turns off a guard on a resource's data is ask: `force_destroy = true` on a bucket, or `deletion_protection = false` on a database, lets a later destroy lose data that it would otherwise have refused or kept. The guards and the value that turns each off are listed in [`guards.yaml`](../internal/registry/guards.yaml). A guard set to a value known only at apply is ask too, and so is unsetting a guard whose off value is `false`, since unset means `false`. A listed guard that is missing from the plan altogether is ask, because it means `guards.yaml` and the provider disagree. Creating a resource with a guard already off is no decision, since nothing is lost yet. Only top-level boolean attributes are read, and only for the listed types, because the values in a plan include secrets.

<br/>

## Kubernetes

Only named objects are looked up, with `kubectl get`, and each call has a short timeout.

| Target | Lookup | Verdict |
|---|---|---|
| Namespace | PVCs, StatefulSets, Deployments and Pods inside it, then the volumes behind the PVCs | Any volume with `reclaim=Delete` or `Recycle`: deny. Otherwise ask, with the contents listed |
| PVC | The volume behind it | Same |
| PV | Its reclaim policy | Same |
| StatefulSet | `persistentVolumeClaimRetentionPolicy.whenDeleted` | `Delete`, and the claims' volumes would be deleted: deny. Otherwise ask, since claims normally outlive the set |
| CRD | How many instances exist | Ask, with the count |
| Anything else | None | Ask |

Ask with no lookup: `-f`, `-k`, selectors, `--all`, `--raw`, several types at once, computed arguments, more names than it will look up, and any name that is not a valid object name. Names go to `kubectl get` as arguments, so `pv/--kubeconfig=x` must not reach it.

| Other verbs | Verdict |
|---|---|
| `get`, `describe`, `logs`, `apply`, `create`, `rollout` and similar | No decision |
| Anything with `--dry-run=client` or `--dry-run=server` | No decision |
| `drain`, `taint`, `exec`, `debug`, `attach`, `run ... -- <command>` | Ask |
| `apply --prune`, `apply --force`, `replace --force`, `scale --replicas=0`, a `patch` that touches finalizers or reads a `--patch-file` | Ask |
| A verb it does not know, such as a plugin | Ask |

`oc` follows the same rules without lookups, and its own read-only verbs such as `status` and `whoami` are no decision. `oc project` and `oc login` stop later `kubectl` lookups, since they share a kubeconfig. `helm upgrade` and helmfile are decided by verb unless `--dry-run` is set: `helm upgrade`, `helmfile sync` and the like are ask, because knowing what an upgrade removes means rendering the new chart, which can download it and run post-renderers and plugins.

| Helm command | Lookup | Verdict |
|---|---|---|
| `helm uninstall` | The release's latest revision, then each PVC, PV, namespace, StatefulSet and CRD in its manifest, judged as a `kubectl delete` of it | Any of those that would deny: deny. Otherwise ask, listing what goes, what `helm.sh/resource-policy: keep` keeps, and any delete hooks |
| `helm rollback` | The current and the target revision, then the same lookups for what the target lacks | Same, except that a keep in the manifest is not trusted, since helm reads it from the live object |

Helm keeps each revision in a Secret of the release's namespace, which `kubectl get secret` reads. Only the manifest, the revision, the status and the hook events are decoded, and from the manifest only each object's kind, name, namespace and resource policy, with lists read item by item as helm does: the same Secret holds the chart and its values, and the manifest holds rendered Secrets. Ask with no lookup: `--kubeconfig` and the other `--kube-*` connection options, any `HELM_DRIVER`, `HELM_NAMESPACE` or `HELM_KUBE*` set on the command, the same variables in the environment except `HELM_DRIVER=secret`, `KUBECONFIG`, `PATH` or `HOME` set on the command, a helm that is not the one on PATH, and the same earlier steps that stop a `kubectl` lookup.

<br/>

## When a check fails

Claude Code lets a command run when its hook times out, crashes or cannot start. whatdies closes each of those:

| Failure | Handling |
|---|---|
| A lookup hangs | Each probe has a timeout, the whole check has a deadline shorter than the hook's, and the verdict is ask |
| A lookup fails: no credentials, forbidden, unreachable | Ask, with the error |
| A bug in whatdies | The panic is recovered, and the verdict is ask |
| The process dies anyway, or the binary is gone | The installed command ends in `exit 2`, which blocks |
| The hook itself times out | `onFailure: block` in the installed entry |
