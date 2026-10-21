#!/usr/bin/env sh
# Fail if any cmd/*.go (non-test) file defines anything other than exactly one
# cobra.Command.
fail=0
for f in cmd/*.go; do
  case "$f" in *_test.go) continue ;; esac
  n=$(grep -c '&cobra.Command{' "$f")
  if [ "$n" -ne 1 ]; then
    echo "$f defines $n commands"
    fail=1
  fi
done
exit $fail
