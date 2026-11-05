#!/usr/bin/env bash
# Sourced by demo.tape: stands in for the tools the recording container lacks and
# prints what capture.sh saved from the real run, in order.
step=0
whatdies() {
  step=$((step + 1))
  cat "examples/demo/captured/$step.txt"
}
kubectl() { :; }
PS1='$ '
clear
