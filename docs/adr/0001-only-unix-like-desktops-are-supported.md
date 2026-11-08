# 0001: Only Unix-like desktops are supported

Status: accepted

## Context

Each operating system adds its own process, socket and filesystem behaviour to test.

## Decision

Releases target macOS and Linux. There is no Windows build.

## Consequences

Code may rely on Unix sockets and POSIX process handling without a portability layer.
