# Internal fork: quota-aware local CLI continuation

Base: upstream **v0.14.9**, commit `f9dc472702` (2026-11-03 release).
Fork: https://github.com/fsaldivar-dev/agent-orchestrator
Branch: `fix/quota-fallback`.

## Behavior

For explicitly enabled projects, the daemon observes a settled, recognizable subscription-quota refusal. It ranks alternatives using the session role's configured candidates, skips already-exhausted choices and fresh known-exhausted quota pools, and checks the destination's Chat capability before switching. It uses the installed CLI drivers and their existing authentication. It does not install agents, add API credentials, spend reset credits, enable paid extra usage, or change accounts.

Default pilot preference: Codex GPT-6.1 Sol with high reasoning for the director; then Claude Opus 5.5, Sonnet 5.5 and Codex Terra. Worker preference puts Sonnet ahead of Opus and uses medium reasoning where configured. This is an explicit operating policy, not a claim that a benchmark universally ranks these models. Availability and observed quota can change which candidate is chosen. Kimi K3.1 Pro is also configured as an alternative: its CLI now reports authorized. High-speed variants are excluded. The policy is preference-ranked and capacity-aware, not an LLM judging its own replacement.

The original AO session and registered workspace are retained. Before switching, the daemon saves the full active durable conversation, failed request, partial response, plan, activities and session metadata in `AO_DATA_DIR/quota-fallback/checkpoints/` (private local files). The existing provider-switch code supplies its normal handoff, and the continuation asks the next CLI to read the complete checkpoint. A different model cannot inherit another provider's private KV cache; continuity is through the saved conversation, files and instructions. The switch never stashes, resets, commits, deletes or recreates the workspace.

Each operation is an idempotent, durable system activity in the existing conversation, visible as `Quota fallback`. A committed switch can recover before its continuation is sent. Simultaneous sweeps cannot launch duplicates. Exhausted models follow the continuation lineage, including aliases. Pending approvals, new work and manual provider changes prevent automatic takeover. An ambiguous failure or failed target startup produces a visible hold rather than repeated restarts.

## Scope

- Native **Chat** sessions (workers and director); TUI and the separate automatic-review controller are not included.
- Explicit quota refusal required. Ordinary HTTP 429, network failure, stale meter and empty output alone do not trigger a handoff.
- No configuration file, or `enabled: false`, means off. An already armed reset-time retry keeps priority.
- Unknown target capacity permits one CLI attempt; it is not represented as confirmed available capacity. If that attempt hits quota, selection advances.
- The first entry of each chain describes the starting/default route and is not a fallback candidate. Remaining entries are preference-ranked candidates. The selector excludes the failed source and exhausted candidates.
- Holds are visible and require a manual new request/model choice after the underlying issue is fixed. No automatic loop or return to a recovered earlier model.

## Configure

Copy `scripts/local-fork/quota-fallback.example.json` to `~/.ao/data/quota-fallback.json`. The default enables only `mexquiapp-pilot`. `roleChains` overrides the default `chain` for a worker or director. Model IDs must exist in the authenticated CLI's catalog. `excludedSessionIds` can keep explicitly paused sessions outside automatic recovery. Config is reread on the normal daemon recovery sweep; no restart is needed for policy edits.

## Build and run

From this checkout:

```sh
go build -ldflags '-X github.com/aoagents/agent-orchestrator/backend/internal/cli.Version=0.14.9-fsaldivar.3' -o frontend/daemon/ao ./backend/cmd/ao
./scripts/local-fork/start.command
```

The installed internal build is `~/Applications/Orchestrator Fork.app`; its bundled Go daemon includes this change. The original `Agent Orchestrator.app` remains intact. `scripts/local-fork/package-macos.cjs` builds the copy from the exact verified v0.14.9 desktop archive and the compiled daemon. It also applies the equivalent compiled one-token form of the tested `daemon-launch.ts` fix (use the real home directory rather than `app.asar` as cwd for a configured daemon). The script validates archive offsets, updates per-file and Electron archive integrity hashes, retains Electron's helper naming, and verifies the local ad-hoc signature. It requires `@electron/asar` 3.4.1 as its third argument.

This is a locally signed development build, not a newly notarized vendor release. macOS may ask again for the app's own “Orchestrator.inc Safe Storage” key because the fork has a different signing identity. This is not permission to read all Keychain items. No passwords or keychain ACLs are changed by the build scripts; encryption is not disabled.

The launcher opens the fork copy. Quit the previous desktop completely first; a second launch only focuses an existing process. The daemon's native provider hosts survive its normal shutdown and reconnect; explicit session termination remains destructive to a live agent. Opening the official app normally runs its original daemon and does not activate the fork feature. Do not use the vendor updater to overwrite the internal copy; rebuild it explicitly from the pinned base.

## Verification

Focused regression suite: `go test -race ./backend/internal/service/chat -run '^TestQuotaFallback' -count=1`.
Full backend: `cd backend && go test ./...`.
Static checks: `go vet ./backend/internal/service/chat ./backend/internal/daemon`.
See `FORK-VALIDATION.md` for actual results and limitations. No test deliberately exhausts a real subscription.

## Reviewed artifact completion

The fork supports `ao report list --project <id>` and
`ao report accept <report-id> --note <findings> --evidence <references>`.
The latter records durable director acceptance of the latest settled worker
`done` report, scoped to the same project. Workers cannot approve themselves.
Artifact-only cards then enter Ready (the board's done lane), with Approved
status. New worker activity or any new report invalidates the old acceptance;
PR-based cards still use PR review facts. Nothing is deleted or terminated.

The director prompt and each done-report delivery require review and acceptance
without waiting for a human reminder. The model must inspect evidence and route
unresolved findings back; the daemon does not equate a worker's claim with proof.
The loopback API's caller attribution is not an authentication boundary.
