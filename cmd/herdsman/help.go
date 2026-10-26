package main

// The --help texts of the slice-1 commands: the usage, the settings, and
// one table of exit codes each (A25).

const runCheckHelp = `usage: herdsman run-check <command> [args]
       herdsman run-check --publish <hours>

Runs one heavy check at a time on this machine, under a shared lock, behind
a load gate, at a lower priority. --publish writes the machine's load
reading every HERDSMAN_PUBLISH_EVERY for <hours> (or
HERDSMAN_PUBLISH_SECONDS), at most a day.

settings: HERDSMAN_LOCK, HERDSMAN_READING, HERDSMAN_LOCK_WAIT (5400),
HERDSMAN_GATE_WAIT (600), HERDSMAN_GATE_POLL (15), HERDSMAN_MIN_FREE (10),
HERDSMAN_NICE (10), HERDSMAN_LOAD_LIMIT (the core count), HERDSMAN_PROBES,
HERDSMAN_PUBLISH_EVERY (15), HERDSMAN_PUBLISH_SECONDS. Durations are seconds
or a number with s, m, h or d.

exit codes:
  N   the check's own exit code
  2   an invalid setting or hour count; nothing ran
  64  usage error
  69  the lock file cannot be opened or locked
  70  the start marker cannot be written
  73  the publisher cannot write its reading (--publish)
  75  the load gate gave up, or no lock came within HERDSMAN_LOCK_WAIT
  126 the command cannot be started
  127 the command was not found
`

const statusHelp = `usage: herdsman status

Prints a health snapshot of every agent in HERDSMAN_STATUS_SPECS
("name:pane:tree ...") and of the machine. LIMIT_MIN (30) flags a
background job running that many minutes or more.

exit codes:
  0  the snapshot was printed
  2  usage error, HERDSMAN_STATUS_SPECS is not set, or an invalid
     LIMIT_MIN
`

const routeHelp = `usage: herdsman route check <route folder> [plan|launch|close]
       herdsman route log <path to 00-route.md> "<text>"

route check prints an "ok" or "MISSING" line per item of the route at a stage
(plan by default); route log appends a dated run-state line.

exit codes:
  0  nothing is missing, or the line was written
  1  something is missing, or the line could not be written
  2  usage error
`

const waitHelp = `usage: herdsman wait

Polls every POLL (60) and returns after the first pass with events
(REPORT, REPORT-UPDATED, REVIEW, BLOCKED, GOAL-DONE-NO-REPORT, STALL,
OVERDUE, HEALTH), or after DEADLINE (1500) with a TICK line.
HERDSMAN_HEALTH_EVERY (300) paces the health check; HERDSMAN_DUE_<name> is
a due time in Unix seconds. Durations are seconds or a number with s, m, h
or d, at most a day.

exit codes:
  0  events or a TICK were printed
  2  usage error, or an invalid setting; nothing ran
`

const watchHelp = `usage: herdsman watch implementers|reviewer|prs

implementers and reviewer print events until interrupted; prs returns on
the first pull request change, or after HERDSMAN_PR_DEADLINE (1500) with a
TICK line, polling every HERDSMAN_PR_INTERVAL (120).

exit codes:
  0  a change or a TICK was printed, or the watcher was stopped
  1  no gh token, or a pull request could not be read three polls in a
     row
  2  usage error, or a missing or invalid setting; nothing ran
`

const gateHelp = `usage: herdsman approval-gate consume <go-file> <action> <target> <revision> <draft-file|->
       herdsman approval-gate complete <request-id> <outcome>

Binds a relayed go to one effect: consume checks the go and records the
request (CONSUME, DUPLICATE, RECONCILE or REFUSED lines); complete records
its outcome (COMPLETED). --json prints each line as an Event object.
HEALTH_AFTER (1 to 86400) and LOCK_WAIT (1 to 60) are strict settings.

exit codes:
  0  consumed or completed; act once
  1  refused: the go does not bind this effect
  2  usage error, HERDSMAN_INBOX is not set, an invalid setting, or the
     inbox cannot be used
  3  a human must repeat the setup
  4  a duplicate: the request already has an outcome
  5  reconcile: consumed without an outcome; inspect external state first
`

const auditHelp = `usage: herdsman transcript-audit claude|codex <transcript> <session-id> [allowed-write-root/ ...]

Audits a transcript for writes: GAP, FLAG and AUDIT lines. --json prints
each line as an Event object. AUDIT_GRANT_FILE and AUDIT_CHANNEL_DIR as in
the skill.

exit codes:
  0  clean
  1  flagged calls: report each to the human and stop work
  2  a gap: the transcript could not be audited
`
