# Linux task isolation deployment contract

This mode separates the long-lived daemon identity from every agent task. It
is intended for dedicated Linux workers that execute untrusted repositories.
It does not configure a production host or provision credentials.

## Trust boundaries

- The daemon runs as a permanent, unprivileged user. Its home, profile,
  configuration, token cache, logs, SSH keys, and workspace root are mode
  `0700` (files containing credentials are `0600`). Its Multica credential is
  a scoped runtime/service credential; a member PAT is not the target model.
- Each claimed task gets a new `mct-<task-id>` system user. The user owns only
  that managed task root. `HOME` and every XDG base directory point inside it.
  The task receives the server-issued `mat_` task token and any explicitly
  task-scoped provider credential; it never receives the daemon credential.
- Agent executables and their shared libraries must be installed in
  system-readable locations such as `/usr/local/bin`. Do not put an executable
  or required library below the daemon home. Codex's task-local `CODEX_HOME`
  is handed over with the task root.
- Set `MULTICA_WORKSPACES_ROOT` to a dedicated path outside the daemon home,
  for example `/var/lib/multica-tasks`. Its ancestor directories and the
  per-workspace directory need execute-only traversal for task users (`0711`);
  do not grant directory listing. Each task root itself is reset to `0770`
  with no world access, so traversal does not grant another task's contents.
- `local_directory` project resources are rejected while isolation is
  required. Managed repository checkouts are the only supported workdirs.
- A completed workdir may remain for same-issue resume, but it is re-owned by
  the daemon after every run. A later unrelated task has a different UID and
  cannot read it. Garbage collection remains daemon-owned.

The protected assets are the daemon/runtime credential, config and profile,
daemon logs, audit/deploy SSH keys, other tasks' workdirs, and permanent host
state. The attacker is an arbitrary command executed by an agent task,
including a command that forks, changes process group, or tries to persist.
Host root, the daemon user itself, and a malicious replacement of the root-owned
Multica binary are outside this boundary.

## Host prerequisites

Install `sudo`, `useradd`, `userdel`, and `pkill`. Install the exact Multica
binary at a root-owned, non-writable path. For a daemon user named
`multica-daemon` and `/usr/local/bin/multica`, add a root-owned sudoers fragment:

```sudoers
Cmnd_Alias MULTICA_TASK_SANDBOX = /usr/local/bin/multica __multica_task_sandbox_root *
multica-daemon ALL=(root) NOPASSWD:SETENV: MULTICA_TASK_SANDBOX
```

Validate it with `visudo -cf`. `SETENV` is needed because provider and `mat_`
credentials cross the daemon-to-root supervisor boundary; the helper removes
all internal sandbox control variables before it execs the task. The task UID
has no sudo rule, so it cannot invoke the helper itself.

Configure the service with:

```ini
Environment=MULTICA_LINUX_TASK_ISOLATION=required
Environment=MULTICA_DAEMON_MAX_CONCURRENT_TASKS=1
```

The implementation supports unique identities at higher concurrency, but a
worker rollout may independently choose `1` as its admission limit. Restarting
with `disabled` weakens the boundary and must be treated as a security-relevant
configuration change.

## Lifecycle and fail-closed behavior

Preparation happens before the server task enters `running`. It validates that
the workdir is inside the managed task root, creates the user, removes all
world permissions from the tree, and hands the tree to the task UID while
retaining daemon group access. Any error stops the task before agent launch.

The root supervisor drops supplementary groups, GID, and UID before exec. It
watches the daemon PID and kills every process with the task UID on daemon
crash. Normal completion, agent failure, cancellation, and timeout converge on
the same cleanup: kill the UID, restore the tree to the daemon, then delete the
system user. Cleanup failure changes an otherwise successful run to failure;
there is no fallback launch under the daemon UID.

## Verification

Normal Go tests cover validation, environment stripping, daemon integration,
and fail-closed configuration. The privileged runtime test creates a real
ephemeral user and checks the positive execution path plus denial of daemon
credential/config/log/profile, audit SSH key, and another task workdir. It also
leaves a background process intentionally and proves process and identity
cleanup:

```bash
cd server
MULTICA_RUN_LINUX_TASK_SANDBOX_TESTS=1 \
  go test ./internal/daemon/tasksandbox -run TestLinuxTaskSandboxRuntime -count=1 -v
```

Run this only on a disposable Linux CI/worker host whose operator permits
temporary system-user creation. The test uses passwordless sudo and removes
the generated identity before returning.
