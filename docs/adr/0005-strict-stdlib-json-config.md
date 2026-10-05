# 5. Strict, standard-library JSON configuration

- Status: Accepted
- Date: 2026-10-05

## Context

Both binaries need configuration that an operator can read and edit. Config
typos are a common source of silent misbehaviour: a misspelled key is ignored
and a default is used instead, and the operator never learns. We also want
configuration to stay dependency-free and shared.

## Decision

Configure both binaries with **strict JSON** parsed by the standard library
(`internal/config`):

- Unknown fields are rejected (`DisallowUnknownFields`).
- Trailing data after the first document is rejected.
- Durations are Go duration strings, never bare numbers (a custom `Duration`
  type with `UnmarshalJSON`/`MarshalJSON`).
- Defaults are applied before decoding, so an explicit `false` wins over a
  default `true`.
- `${ENV}` references are expanded before decoding, so secrets can come from
  the environment.
- `schema_version` must be `1`; there is no silent upgrade.

## Consequences

- A typo fails loudly at startup with the offending field named.
- JSON has no comments, so the example files must be self-explanatory and are
  documented in `docs/configuration.md`.
- Adding a field is a breaking change for configs that already set an unknown
  key, which is intentional: the schema is explicit.
- No third-party config or TOML/YAML dependency.
