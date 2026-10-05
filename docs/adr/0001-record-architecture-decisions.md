# 1. Record architecture decisions

- Status: Accepted
- Date: 2026-10-05

## Context

you-as-bee makes non-obvious choices (raw USB passthrough, no TLS, a specific
driver-unbind strategy) that are easy to "fix" later without knowing why they
were made. Future maintainers need the reasoning, not just the code.

## Decision

Record every architecturally significant decision as an ADR in `docs/adr`,
numbered sequentially, using the Nygard template (Context, Decision,
Consequences, Status). A decision that changes gets a new ADR that supersedes
the old one; the old one is not edited.

## Consequences

- The reasoning behind each choice is discoverable next to the code.
- The documentation set grows; authors must keep ADRs short and factual.
- A superseded decision is visible rather than silently overwritten.
