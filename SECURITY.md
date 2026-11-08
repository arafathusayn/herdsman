# Security policy

## Reporting a vulnerability

Report it privately through GitHub: the repository's Security tab, "Report a vulnerability". Please do not open a public issue.

Include the version (`herdsman version`), the platform and the steps to reproduce.

## Supported versions

The latest release. herdsman runs on macOS and Linux.

## What protects you

`docs/threat-model.md` lists the inputs herdsman reads and how each is handled. Third-party code is pinned in this repository; `MAINTAINING.md` describes how an upstream fix lands.
