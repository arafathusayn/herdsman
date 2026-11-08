# 0024: Launching a route with one command

Status: accepted

## Context

Starting a route used to take many manual steps.

## Decision

`herdsman up` does everything from the doctor check through enrolling both agents and delivering the request, and does not stop for input along the way. `--web` adds the browser interface.

## Consequences

A new route needs no hand-run setup.
