#!/bin/sh
set -eu
/usr/local/bin/panda-connect-init
exec /usr/local/bin/prusa-connect "$@"
