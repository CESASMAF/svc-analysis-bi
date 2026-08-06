---
name: pipeline-maestro
description: >
  Orchestrates a multi-agent fail-first pipeline for Go development.
  Coordinates domain-architect, test-writer, domain-modeler, application-orchestrator,
  infra-implementer, code-reviewer, go-quality-checker, integration-validator.
  Use when implementing features that span multiple layers.
user_invocable: true
---

# Pipeline Maestro -- Go Multi-Agent Pipeline

## Agent Roster

| Agent | Role | Writes To | Never Touches |
|-------|------|-----------|---------------|
| domain-architect | Type contracts | 001-contracts/ | implementations, tests |
| test-writer | Failing tests | 002-tests/ | implementations, internal/ |
| domain-modeler | Domain code | 003-domain/ + internal/domain/ | ingestion, store, api, export, tests |
| application-orchestrator | Ingestion pipeline | 003-application/ + internal/ingestion/ | domain impl, store, api, export, tests |
| infra-implementer | Infrastructure | 003-infra/ + internal/store/, api/, export/, cmd/ | domain, ingestion, tests |
| code-reviewer | Architecture audit | 004-code-review/ | cannot modify code |
| go-quality-checker | Go quality | 005-go-quality/ | cannot modify code |
| integration-validator | Build + test | 006-integration/ | cannot modify anything |

## Execution Waves

### Wave 1: Design
1. **domain-architect** -- reads request + OpenAPI/AsyncAPI contracts, produces type-level artifacts

### Wave 2: Tests (TDD Red)
2. **test-writer** -- reads contracts, writes failing tests (Go testing + table-driven)

### Wave 3: Implementation (parallel where independent)
3. **domain-modeler** -- reads contracts + tests, implements domain (make tests green)
4. **application-orchestrator** -- reads contracts + tests + domain REPORT, implements ingestion pipeline
5. **infra-implementer** -- reads ALL REPORTs, implements store, API, export, cmd

### Wave 4: Quality Gates
6. **code-reviewer** -- audits all code against CLAUDE.md rules + lgpd-compliance (governance)
7. **go-quality-checker** -- audits Go language quality (vet, error handling, goroutines, naming)
8. **integration-validator** -- runs `go vet ./... && go build ./... && go test -race -cover ./...`

### LGPD Cross-Cutting (consulted by agents when relevant)
Agents that touch anonymization, PII, or data handling MUST consult:
- `lgpd-seguranca` -- technical measures, anonimizacao, incidentes

## Communication Protocol

### REPORT.md Public API Chain
1. domain-modeler lists domain functions -> application-orchestrator reads it
2. application-orchestrator lists ingestion interfaces + ports -> infra-implementer reads it
3. infra-implementer reads ALL reports to know what to implement

### Fresh Context Protocol
Each agent gets ONLY the context it needs:
- domain-modeler: 001-contracts/, 002-tests/ (domain only)
- application-orchestrator: 001-contracts/, 002-tests/ (ingestion only), 003-domain/REPORT.md
- infra-implementer: 001-contracts/, 002-tests/ (infra only), ALL 003-*/REPORT.md

### Review Loops
- Max 3 rounds per review stage
- Issues routed to SPECIFIC implementer by file/layer
- After 3 rejections -> escalate to user

## Phases

Each phase reports its result in its final answer rather than writing files to disk.

```
  request                 -- original user request
  000-discuss/CONTEXT.md  -- design decisions (if discuss phase used)
  001-contracts/           -- type definitions, interfaces, errors
  002-tests/               -- failing tests (TDD red)
  003-domain/REPORT.md     -- domain Public API
  003-application/REPORT.md -- ingestion pipeline Public API
  003-infra/REPORT.md      -- infrastructure summary
  004-code-review/         -- review rounds
  005-go-quality/          -- quality check results
  006-integration/         -- build + test results
  STATE.md                 -- current pipeline state
```

## Granularity
- 1 ticket = 1 atomic unit (1 value object, 1 event handler, 1 encoder, 1 handler)
- Complete one ticket end-to-end before starting next
- Never implement multiple concerns simultaneously

## Parallel Ticket Execution

### When to Parallelize

Tickets MAY run in parallel when ALL of the following are true:
1. **Zero file overlap** — each ticket writes to a completely different package/directory
2. **No dependency chain** — ticket B does not need output from ticket A
3. **Shared types are stable** — the domain layer they all depend on is already COMPLETE
4. **Dependencies pre-installed** — go.mod has all libraries before agents launch

### Pre-Flight Safety Protocol

Before launching parallel agents, the orchestrator MUST:

```
1. DEPENDENCY LOCK
   - Identify ALL libraries each ticket needs
   - Run `go get <all deps>` in a single sequential step
   - Run `go mod tidy && go build ./...` to verify
   - Run existing tests to confirm no regression
   - After this point: NO AGENT may modify go.mod or go.sum

2. BOUNDARY DECLARATION
   - For each ticket, explicitly list:
     - WRITE ALLOWED: exact paths/globs the agent may create/modify
     - READ ALLOWED: what it may read for context
     - FORBIDDEN: what it must NEVER touch
   - Verify zero intersection between all WRITE sets

3. DOMAIN FREEZE
   - If any agent might need new domain types → STOP
   - Add those types BEFORE launching parallel agents
   - Once launched, internal/domain/ is READ-ONLY for all agents
```

### Risk Matrix

| Risk | Probability | Impact | Mitigation |
|------|:-----------:|:------:|------------|
| go.mod race (concurrent `go get`) | HIGH | Corruption | Pre-flight dep lock — agents forbidden from `go get/mod` |
| go.sum corruption | HIGH | Build break | Same as above — go.sum frozen before launch |
| Same-file write conflict | LOW | Data loss | Boundary declaration with zero overlap verification |
| Build failure during creation | MEDIUM | Transient | Agents must tolerate `go build` failures from incomplete sibling packages |
| `go mod tidy` removes sibling deps | MEDIUM | Build break | Agents forbidden from running `go mod tidy` |
| Domain type addition needed | LOW | Deadlock | Domain freeze — escalate to orchestrator if new types needed |
| Test interference (`go test ./...`) | MEDIUM | False failure | Agents test ONLY their own package: `go test ./internal/<own>/...` |

### Agent Isolation Rules

Each parallel agent receives these MANDATORY instructions in its prompt:

```markdown
## SAFETY: FILE BOUNDARIES (CRITICAL)
You may ONLY write to: [explicit paths]
You MUST NOT modify: go.mod, go.sum, [sibling packages], cmd/
You MUST NOT run: `go get`, `go mod tidy`, `go mod download`
You MUST test only YOUR package: `go test ./internal/<yours>/...`
You MUST NOT run `go test ./...` (may fail due to incomplete siblings)
```

### Conflict Detection (Post-Completion)

After ALL parallel agents complete, the orchestrator MUST run:

```bash
# 1. Verify go.mod unchanged
git diff --name-only go.mod go.sum  # should be empty vs pre-flight state

# 2. Check no boundary violations
# For each ticket, verify files created are within declared boundaries

# 3. Full integration build
go build ./...

# 4. Full test suite
go test -race ./...

# 5. If failures: identify which agent's code broke, route fix to that agent only
```

### Conflict Resolution Protocol

If post-completion validation fails:

| Failure Type | Resolution |
|-------------|------------|
| Build error in package A due to package B's types | Route to package B's agent to fix the contract |
| Import cycle detected | Orchestrator refactors the dependency direction |
| go.mod was modified by an agent | Revert go.mod to pre-flight state, re-run `go mod tidy` once |
| Two agents created same filename | Impossible if boundaries were verified — escalate to user |
| Test flaky due to timing | Re-run with `-count=3` — if still fails, route to owning agent |

### Parallel Execution State Tracking

STATE.md for parallel tickets uses a grouped format:

```markdown
# Parallel Execution Batch: TICKET-004, TICKET-005, TICKET-006

| Ticket | Package | Status | Agent | Started | Completed |
|--------|---------|--------|-------|---------|-----------|
| TICKET-004 | internal/api/ | IN_PROGRESS | app-orch #1 | ... | |
| TICKET-005 | internal/store/ | IN_PROGRESS | app-orch #2 | ... | |
| TICKET-006 | internal/export/ | IN_PROGRESS | app-orch #3 | ... | |

## Pre-Flight
- [x] Dependencies locked (chi, pgx, nats.go — the only three direct requires; encoders are stdlib)
- [x] Boundaries verified (zero overlap)
- [x] Domain frozen
- [x] Existing tests pass

## Post-Completion
- [ ] go.mod unchanged
- [ ] `go build ./...` passes
- [ ] `go test -race ./...` passes
- [ ] No boundary violations
```

### Maximum Parallelism

- **Recommended: 3 tickets max** — beyond this, context window pressure and notification handling degrades quality
- **Hard limit: 5 tickets** — Claude Code notification buffer has practical limits
- **Sequential within ticket** — waves inside a ticket are ALWAYS sequential (contracts → tests → impl → review)

## Commit Convention
```
feat(<scope>): <description>

- [what was created]
- [error handling]
- [coverage stats]

Pipeline: [agents used], [review rounds]
```
