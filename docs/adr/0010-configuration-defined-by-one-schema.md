# 0010: Configuration defined by one schema

Status: accepted

## Context

Settings must be usable from any language and must not drift between file formats.

## Decision

A protobuf message is the only definition of the settings, and generators produce the code that reads them, so TOML, YAML, JSON and binary files all mean the same thing. A project's file defaults to a place outside the repository, beyond an agent's reach.

## Consequences

Adding a setting starts with a schema change, and no format can fall behind.
