# 0024: Launching a route with one command

Status: accepted

## Context

Starting a route used to take many manual steps.

## Decision

`herdsman up` does everything from the doctor check through enrolling both agents and delivering the request, and does not stop for input along the way. `--web` adds the browser interface.

On the herdr-core backend `up` can end in two waits that need a person, and reports each with what to do: `needs_attention`, for a startup dialog or a start line that a person submits, and `waiting_for_visibility`, when the app has never shown that it is connected to the selected core. Running `up` again continues from where it stopped.

## Consequences

A new route needs no hand-run setup.
