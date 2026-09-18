# Frontend Interaction Audit Matrix

Status: executable acceptance baseline

## Audit method

Every row requires: code inspection, real browser action, post-action DOM assertion, accepted screenshot for important states, and a named evidence limit. Build success alone is not evidence.

## Workbench matrix

| Surface | Required behaviors |
| --- | --- |
| Project list | open row, search, filter, rename, delete impact, cancel, confirm, menu dismissal, keyboard dismissal |
| Run selector | open/close, choose historical Run, selected marker, current Artifact synchronization, no cross-Run process leakage |
| Artifact tree | select, expand, collapse, reopen, switch group, preserve independent group state, long list scrolling, stable active marker |
| Artifact workspace | loading, empty, error, current version, historical version, return latest, search, section expand/collapse |
| Manual editing | enter, dirty state, sticky save/cancel, save error, save success, discard on navigation, beforeunload guard |
| Script editor | single episode source, toolbar states, text selection, context reference, save new version, cancel, long-document scroll |
| Script collection | ordered reading, episode index, read-only notice, navigate to single episode for editing |
| Agent timeline | project messages, message references, Run summary, process details disclosure, approval card, configuration card, errors |
| Composer | active Artifact context, explicit selection context, remove selection, Skill menu, attachments, send, disabled Run state |
| Runtime controls | pause, resume, retry, cancel confirmation, current status, completed content retention |
| Responsive desktop | 1280x800, 1440x900, 1920x1080, no horizontal overflow, usable three-column widths |

## State transitions that must be tested

1. Expand an episode group, select another episode, collapse it, switch Artifact group, return and reopen it.
2. Start editing, change content, attempt Artifact switch, cancel the discard prompt, then confirm it.
3. Open version history, inspect an older version, verify read-only state, return to latest.
4. Select structured text and script text, verify Composer reference, send, reload and verify message reference persistence.
5. Change Artifact or version with a live selection and verify the stale reference is removed.
6. View a historical Run while another Run is active and verify controls and process history do not target the wrong Run.
7. Exercise every modal and anchored menu with click-away and Escape.
8. Exercise loading, request failure, retry, empty and long-content states.

## Audit rules

- Test from user goals, not implementation function names.
- Do not reuse implementation assumptions as test assertions without an independent expected behavior.
- Inspect hit areas, alignment, clipping, bottom gaps, sticky controls and scroll ownership.
- A screenshot is required for layout claims; DOM assertions are required for state transition claims.
- Record anything not tested as a limit rather than marking it passed.
