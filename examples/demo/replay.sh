#!/usr/bin/env bash
# Sourced by demo.tape: stands in for the tools the recording container lacks and
# prints what capture.sh saved from the real run, in order, with its exit status.
step=0
whatdies() {
  step=$((step + 1))
  cat "examples/demo/captured/$step.txt"
  local rc
  rc=$(cat "examples/demo/captured/$step.rc") || return 1
  return "$rc"
}
kubectl() { :; }
PS1='$ '
clear
