# Validation record — 2026-11-05

Base: upstream tag v0.14.9. Change: opt-in quota fallback using local CLI drivers.

- Focused quota fallback tests pass, including race instrumentation: cross-provider and same-provider handoff, checkpoint contents, simultaneous sweeps, committed-switch recovery, unchanged workspace bytes, new user work, provider startup failure, role-specific policy, fresh shared-quota exclusion, and exhaustion of multiple candidates.
- The complete backend test run was executed. Most packages passed, including Chat, daemon, session manager, and SQLite. It was not a clean full-suite pass: two authentication tests timed out under parallel load, and Command Code git-include plus shell EOF/fish tests failed.
- Re-ran the four affected packages on pristine v0.14.9. Authentication tests passed; the Command Code git-include and shell EOF/fish failures reproduced unchanged. Fish is not installed locally; the EOF assertions also fail with the local system shells. These unrelated failures were not modified.
- Build and targeted `go vet` passed. Final focused quota suite also passed with race instrumentation. The complete Chat race suite passed (369.414 s); the complete non-race Chat suite passed (68.267 s).
- No real subscription was intentionally exhausted. Quota errors and crash windows are injected by tests using fake native provider conversations and real SQLite persistence.
- No public API shape changed. The packaged-daemon cwd fix in `frontend/src/shared/daemon-launch.ts` passed all 12 launcher tests using Vitest 3.2.4. The source files were copied unchanged into a temporary isolated test runner.

## Local runtime

- Built CLI reports `0.14.9-fsaldivar.2`.
- Isolated daemon on port 13001: `/healthz` = ok, `/readyz` = ready, `/api/v1/sessions` returned an empty list as expected with temporary data.
- Production configuration saved at `~/.ao/data/quota-fallback.json`, scoped only to `mexquiapp-pilot`.
- Pre-activation SQLite backup: `~/.ao/data/fork-backups/20261105-080238/ao.db` with session inventory.
- Launcher: `~/Applications/Orchestrator Fork.command`. The original vendor desktop is unchanged. The fork copy started successfully with health=ok, ready=ready, and five existing nonterminated sessions on the same branches. The backend version is `0.14.9-fsaldivar.2`; the executable path is under `Orchestrator Fork.app`.

The final Kimi/permission-compatible selector passed all focused tests again, including race instrumentation. A real provider quota exhaustion has not been forced.

## Model-named quota regression

The first build missed real `You've reached your Fable limit` failures because upstream recognized time-window quotas but not model-family quotas. Added Fable/Opus/Sonnet/Haiku family recognition, preserving rejection of unrelated memory limits and quoted prose. The regression includes the exact persisted error string and a no-reset-timestamp fallback. Added explicit excludedSessionIds for already-paused sessions. The active simulator worker (pilot-5) is excluded pending user authorization; pilot-4 was transferred to Codex with its full retained transcript and read-only review role.
