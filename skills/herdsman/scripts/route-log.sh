#!/bin/bash
# Append one run-state line "- HH:MM <text>" to a route file.
# The time comes from `date`, never from the orchestrator: typed times were wrong more than once.
# Usage: route-log.sh <path to 00-route.md> "<text>"
if [ $# -lt 2 ] || [ -z "$2" ] || [ ! -f "$1" ]; then
  echo 'usage: route-log.sh <path to 00-route.md> "<text>" (the file must exist)' >&2
  exit 2
fi
printf -- '- %s %s\n' "$(date +%H:%M)" "$2" >> "$1"
