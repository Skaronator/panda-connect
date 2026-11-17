#!/bin/sh
set -eu
/usr/local/bin/pandalink-init
exec /usr/local/bin/prusa-connect "$@"
