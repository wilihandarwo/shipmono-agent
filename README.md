# shipmono-agent

The ShipMono deploy agent — a single static Go binary that runs on a customer's
own Ubuntu LTS server and deploys their PHP app via FrankenPHP. It is the real
counterpart to the Ruby simulator `bin/dev-agent` in the control-plane repo,
which is the executable spec of the `/agent/v1` JSON contract this agent speaks.

This is a **separate codebase** from the ShipMono control plane (the Rails app),
by design: **no customer code runs on or passes through the control plane**, and
the agent is the only thing that touches the customer box.

## Security posture (read this first)

- **Outbound-only.** The agent dials the control plane over HTTPS and polls. It
  opens no inbound ports.
- **Fixed verb set, no arbitrary execution.** The agent only ever performs
  `deploy`, `rollback`, `reload`, `restore`, `add_domain`, `remove_domain`,
  `status`. The verb dispatch (`internal/verbs`) is a closed `switch` with no
  default execution path — an unknown verb is refused, never run. A compromised
  control plane cannot make the box run attacker-chosen commands.
- **Fully unprivileged — no sudo at all.** Runs as the `shipmono` system user
  with **zero** privilege escalation. It owns `/srv/app`, so the atomic `current`
  symlink swap is a plain rename; it reloads FrankenPHP through that server's
  localhost admin API (`frankenphp reload`), not `systemctl`. There is no sudoers
  entry and nothing setuid in its path, so the systemd sandbox keeps
  `NoNewPrivileges=true` with no escalation surface to defend.
- **Ephemeral git credentials.** The short-lived GitHub installation token is
  minted by the control plane per deploy and handed to the agent at poll time.
  It is passed to `git` through a `GIT_ASKPASS` shim (the agent re-invoking
  itself), so it never appears in any process's argv or on disk.
- **Repo allowlisting + path jail.** Git work is confined to a directory derived
  from constants and the numeric repo id — never from the attacker-influenceable
  `repo_full_name`. The server binds to the first repo id it deploys and refuses
  any other.
- **Instant revocation.** On `401 {revoked:true}` the agent deletes its
  credential and exits — the control-plane kill switch.

## CLI

```
shipmono-agent pair --token <token> --host <url>   # one-time registration
shipmono-agent run                                 # poll loop (started by systemd)
shipmono-agent version
```

Environment:

| Var | Default | Purpose |
|-----|---------|---------|
| `SHIPMONO_HOME` | `/var/lib/shipmono` | Credential + state dir. The credential is written `0600`, owned by the agent user. |
| `SHIPMONO_APP_ROOT` | `/srv/app` | Deploy root (`releases/`, `shared/`, `current`). |
| `SHIPMONO_SIMULATE` | unset | `1` forces the simulated executor (no host ops). Auto-on for any non-Linux host. |

## How it's installed

The control plane serves `public/install.sh`, run on the box as:

```
curl -fsSL https://app.shipmono.dev/install.sh | sudo bash -s -- --pair <token> --host https://app.shipmono.dev
```

That script lays out `/srv/app`, creates the `shipmono` user, installs FrankenPHP
+ Litestream + this agent (verifying the SHA256 checksum **before** executing),
and installs two sandboxed systemd units — one for the agent and one for the
FrankenPHP web server (`shipmono-frankenphp.service`, which loads
`/srv/app/shared/Caddyfile`) — then runs `shipmono-agent pair …` and starts them.

The agent's systemd unit runs under the sandbox from the security doc §6:
`NoNewPrivileges`, `ProtectSystem=strict`, `ProtectHome`, `PrivateTmp`,
`ReadWritePaths=/srv/app /var/lib/shipmono`, `CapabilityBoundingSet=` (empty),
`SystemCallFilter=@system-service`, `RestrictAddressFamilies=AF_INET AF_INET6`.

**No sudoers, no privilege escalation.** Because the agent owns `/srv/app` and
reloads FrankenPHP via its localhost admin API, it needs no elevated commands —
so there is no `/etc/sudoers.d/shipmono`, and `NoNewPrivileges=true` holds with
nothing to escalate. (Earlier sudo-based builds installed a sudoers drop-in;
re-running `install.sh` removes it.)

## The verbs

| Verb | Params | What the agent does |
|------|--------|---------------------|
| `deploy` | `repo_id, repo_full_name, git_ref, git_sha` (+ ephemeral `git_token`) | Fetch the repo (allowlisted by `repo_id`) at `git_sha`, `php -l`, build into `releases/<ts>`, symlink `shared/app.db`, atomic `current` swap, graceful reload. Reports `release_id`. |
| `rollback` | `release_id` | Verify the release dir exists, swap `current`, reload. |
| `reload` | — | Graceful FrankenPHP reload. |
| `restore` | `point_in_time` | Litestream point-in-time restore of `shared/app.db` (best-effort; full support is control-plane Tier 3.1). |
| `add_domain` | `domain` | Add host to `shared/Caddyfile`, reload (ACME issues the cert on first request). |
| `remove_domain` | `domain` | Remove host, reload. |
| `status` | — | Report host health (cpu/ram/disk %, load, disk free, FrankenPHP version/health). |

Release ids are UTC timestamps, `YYYYMMDDTHHMMSS` — the same format the control
plane stores.

## Architecture

```
cmd/shipmono-agent      thin entrypoint
internal/cli            subcommands (pair/run/version) + GIT_ASKPASS shim
internal/config         SHIPMONO_HOME / app root / simulate resolution
internal/controlplane   /agent/v1 HTTP client, wire DTOs, typed errors
internal/credstore      0600 atomic credential persistence
internal/daemon         poll → ack → dispatch → events loop; idle heartbeat; backoff
internal/executor       the host seam: Executor interface, sim + real linux impls
internal/verbs          verb → executor dispatch (the closed-set gate)
internal/gitops         confined git, repo_id allowlist, askpass token plumbing
internal/release        release ids + deploy layout paths
internal/health         /proc + statfs sampler (linux), stub elsewhere
```

The transport, poll loop, and verb dispatch are fully real and shared. Only the
host operations swap behind the `Executor` interface: a real Linux executor
(`//go:build linux`) and a simulated executor that logs the identical steps
without touching the host. The simulated executor is what makes a faithful local
end-to-end possible from macOS — the control plane can't tell it apart from a
real deploy.

## Local development & end-to-end (mirrors `bin/dev-agent`)

The agent runs against the **local control plane** on macOS using the simulated
executor — exercising the production transport code over the live contract.

1. In the control-plane repo: `bin/dev`, then create a server and copy its
   one-time pairing token from the Connect-server modal.
2. Here:
   ```sh
   make build
   export SHIPMONO_HOME=$(mktemp -d)
   ./dist/shipmono-agent pair --token <token> --host http://localhost:3000
   ./dist/shipmono-agent run
   ```
   (`SHIPMONO_SIMULATE` is auto-on off Linux, so `run` uses the simulator.)
3. In the control plane: `bin/simulate-push` to trigger a deploy. Watch the
   server flip to active, the heartbeat move the usage bars/sparklines, and the
   deploy log stream into the terminal panel — then revoke the server (kill
   switch) and watch the agent delete its credential and exit.

## Build & release

```sh
make test                 # go test -race ./...
make lint                 # gofmt check + go vet (host+linux) + staticcheck
make build-linux          # static linux/amd64 + linux/arm64
make release VERSION=0.1.0 # both arches + checksums.txt (what install.sh verifies)
make verify-reproducible  # build twice, assert byte-identical
```

Builds are reproducible: `CGO_ENABLED=0`, `-trimpath`, `-buildvcs=false`,
`-ldflags "-s -w -X …/version.Version=<v>"`, pinned Go toolchain (see
`.github/workflows/ci.yml`). The release artifacts are named exactly
`shipmono-agent-linux-{amd64,arm64}` with a `checksums.txt`, which is what
`install.sh` downloads and verifies.

## Mapping to production-readiness checklist §1.1

| Checklist item | Where |
|----------------|-------|
| 2s poll loop (register/poll/ack/events/heartbeat) | `internal/daemon`, `internal/controlplane` |
| Local enforcement of the fixed verb set | `internal/verbs` (closed switch) |
| deploy: repo allowlisted by numeric id, git confined to app tree | `internal/gitops`, `internal/executor/linux.go` |
| release into `releases/<ts>`, symlink `shared/app.db` | `internal/executor/linux.go`, `internal/release` |
| atomic symlink swap + graceful reload | `linuxExecutor.swapCurrent` / `reloadFrankenphp` |
| rollback/reload/restore/add_domain/remove_domain/status | `internal/executor/linux.go` |
| unprivileged user; elevated actions only via tight scoping | fully unprivileged — owns `/srv/app`, reloads via admin API; no sudo (`linux.go`) |
| systemd sandbox block | `install.sh` (control plane); documented above |
| token stored 0600, owned by agent user | `internal/credstore` |
| honors `401 {revoked:true}` | `internal/daemon` revoke branch |
| reproducible builds, cross-compile amd64/arm64 | `Makefile`, `.github/workflows/ci.yml` |

Deferred per their tiers: mTLS for the agent channel (Tier 2.1) and signed
releases / minisign verification (Tier 2.3). The bearer-token auth here is the
v1 model.
