#!/bin/bash
# Public message grammar. Own binding comes only from durable session setup.
set -eu
DIR=$(cd "$(dirname "$0")" && pwd -P)
ME=${HERDSMAN_NAME:?set the enrolled name from session configuration}
action=${1:-status}
[ $# = 0 ] || shift
usage() { printf 'message: status|wait|read [claim]|release <claim>|ack <claim> <id> <record>|send <to> <file> <kind> [reply]|hold <name> <reason>|unhold <name>\n' >&2; exit 2; }
case "$action" in
  status) [ $# = 0 ] || usage; exec /bin/bash "$DIR/message-status.sh" "$ME" ;;
  wait) [ $# = 0 ] || usage; exec /bin/bash "$DIR/message-wait.sh" "$ME" ;;
  read) [ $# -le 1 ] || usage; exec /bin/bash "$DIR/message-read.sh" "$ME" "$@" ;;
  release) [ $# = 1 ] || usage; exec /bin/bash "$DIR/message-read.sh" "$ME" "$1" --release ;;
  ack) [ $# = 3 ] || usage; exec /bin/bash "$DIR/message-ack.sh" "$ME" "$@" ;;
  send) [ $# = 3 ] || [ $# = 4 ] || usage; exec /bin/bash "$DIR/message-send.sh" "$ME" "$@" ;;
  hold) [ $# = 2 ] || usage; exec /bin/bash "$DIR/message-hold.sh" "$ME" hold "$@" ;;
  unhold) [ $# = 1 ] || usage; exec /bin/bash "$DIR/message-hold.sh" "$ME" unhold "$@" ;;
  *) usage ;;
esac
