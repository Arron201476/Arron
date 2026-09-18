# Novel2Script Agent

Novel2Script Agent is a local novel/material-to-short-drama production workspace. Each work has an independent project, conversation, files, run history, artifacts, approvals, and script workspace.

## Current architecture

- `frontend/`: React + TypeScript product UI, including the production Lexical script editor.
- `backend/`: Go HTTP service, Eino Main Agent/content graphs, native rollback runtime, content worker, SQLite workspace/runtime persistence.
- 小说原文拆集采用全局骨架、每批 5 集定界、检查点续跑和确定性全文覆盖校验；前端仍呈现为一个产品步骤。
- `design/`: seven code-aligned current specifications, runtime prompts/rules, and the unified `preview.html` entry.
- `design/archive/`: superseded research and prototypes; never treated as runtime source of truth.
- `runs/`: local databases and run output. Generated content is ignored by Git.

The old `novel2script_mainflow` project is outside this repository and is not read or modified by this application.

## Run locally

Prerequisites: Node.js 20+, npm, and Go 1.22+. Copy `backend/.env.example` to `backend/.env` and configure the model provider.

```powershell
./scripts/dev.ps1
```

Frontend: `http://127.0.0.1:8832`  
Backend health: `http://127.0.0.1:8831/healthz`

The script checks both ports before starting services and does not blindly replace an existing process.

## Quality commands

```powershell
./scripts/test.ps1
./scripts/test.ps1 -WithGoCoverage
./scripts/build.ps1
npm --prefix frontend run gate:editor
```

`test.ps1` runs all frontend unit/component tests and all Go tests. `build.ps1` produces the frontend production bundle and compiles the backend server without writing binaries into the source tree.

## Release and local data

```powershell
./scripts/release-gate.ps1
./scripts/package.ps1
```

The package is written to `release/Novel2ScriptAgent`. It serves the production frontend and Go API from one local address. The package never includes `backend/.env` or any API key.

Stop the application before backup or restore:

```powershell
./scripts/backup-data.ps1
./scripts/restore-data.ps1 -Backup ./backups/novel2script-backup-YYYYMMDD-HHMMSS.zip -ConfirmRestore
./scripts/view-logs.ps1 -Limit 50
```

Backups contain the two SQLite state databases only. Model trace logs and API keys are intentionally excluded.

## Central internal testing

The internal test host runs the packaged application on `127.0.0.1:8842` and exposes it through the existing named Cloudflare Tunnel. This keeps test projects, conversations, runs, artifacts, and logs on the development host for live observation while leaving the `8831/8832` development services unchanged.

```powershell
./scripts/start-internal-test-tunnel.ps1
./scripts/stop-internal-test-tunnel.ps1
```

The scripts manage only the package process and tunnel process that they start. The package `.env`, tunnel PID, tunnel log, and test databases stay under the ignored `release/` directory and are never included in the distributable zip.

## Product contracts

Open `design/preview.html` for the unified document preview. See `design/INDEX.md` for the current specification map; superseded contracts and audits are kept only under `design/archive/`.

Runtime state uses SQLite. Generation configuration is authoritative at the run level; artifacts store a version reference. Changes to an artifact preserve existing downstream content until the user explicitly chooses whether to regenerate affected downstream artifacts.
