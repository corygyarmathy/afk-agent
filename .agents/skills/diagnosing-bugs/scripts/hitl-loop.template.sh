#!/usr/bin/env bash
# Human-in-the-loop reproduction loop.
# Copy this file, edit the steps below, and run it.
# The agent runs the script; the user follows prompts in their terminal.
#
# Usage:
#   bash hitl-loop.template.sh
#
# Two helpers:
#   step "<instruction>"          → show instruction, wait for Enter
#   capture VAR "<question>"      → show question, read response into VAR
#
# At the end, captured values are printed as KEY=VALUE for the agent to parse.
#
# `capture` prints its value back to the terminal, where the agent reads it,
# so capture observations, and leave anything the user must simply do as a `step`.

set -euo pipefail

step() {
	printf '\n>>> %s\n' "$1"
	read -r -p "    [Enter when done] " _
}

capture() {
	local var="$1" question="$2" answer
	printf '\n>>> %s\n' "$question"
	read -r -p "    > " answer
	printf -v "$var" '%s' "$answer"
}

# --- edit below ---------------------------------------------------------

step "Start the service and wait until it reports ready."

capture FAILED "Run the action that triggers the bug. Did it fail? (y/n)"

capture DETAIL "Paste the error, or the wrong output (or 'none'):"

# --- edit above ---------------------------------------------------------

printf '\n--- Captured ---\n'
printf 'FAILED=%s\n' "$FAILED"
printf 'DETAIL=%s\n' "$DETAIL"
