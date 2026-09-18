# OpenAI Agents SDK full-migration acceptance

Date: 2026-08-26  
Status: full SDK implementation complete; isolated stack accepted by automated and live checks

## Runtime boundary

- SDK owns Session, compaction, Runner, main-Agent decisions, tools, Skill
  Agent orchestration, multimodal context, and Artifact patch generation.
- Go owns authoritative transactions and durable business state only.
- The standard Go server requires Sidecar and does not initialize the old Eino
  main Agent, Go control model, image model, revision generator, or demo content
  worker.
- `request_ai_revision` creates a targeted SDK patch proposal. It is distinct
  from explicit whole-step regeneration.

## Current checks

- Sidecar: 82 tests passed.
- Frontend: 18 files and 89 tests passed; TypeScript and production build passed.
- Go: all normally executable packages passed. `mediaprocessor` and
  `modelprovider` passed from flat test binaries; the latest `shell` binary was
  compiled but its launch was blocked by local Windows Application Control.
- Live main-Agent turn: returned the requested exact response through Sidecar.
- SDK-native compaction: full history compacted to the gateway-compatible SDK
  summary item, followed by Responses reasoning/tool items.
- Compacted-memory recall: recovered the early project passphrase exactly.
- Targeted revision: changed only
  `/episodes/2/ending_hook/hook_strength`; episodes 1 and 2 remained bytewise
  equivalent after JSON normalization, with no regeneration plan.
- Image input: previously unreadable project image was read successfully.
- Video input: six-frame extraction and multimodal description succeeded.
- Skill opt-out: an explicit no-Skill request stayed generic chat and created
  no capability card or Run.
- Stop: a 350 ms client abort persisted zero marked messages and Sidecar stayed
  healthy.

## Isolated validation stack

- frontend: `http://127.0.0.1:8892`
- Go Runtime: `http://127.0.0.1:8890`
- SDK Sidecar: `http://127.0.0.1:8891`
- project: `prj_076c514601192dde7cf0aeb7583faa97`

## Standard stack

`scripts/start-local-demo.ps1` now starts the required three-process SDK stack:
frontend 8860, Go 8850, and Sidecar 8871. The old two-process/Eino fallback
startup is no longer the standard path.

## Known gaps found during live acceptance

- A natural-language revision currently resolves to one Artifact version. A
  request that applies to multiple episode Artifacts must be expanded into one
  independently versioned Revision per episode; that multi-target orchestration
  is not implemented yet.
