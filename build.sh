#!/usr/bin/env bash
# Build the tmux-spaces binary: run the tests, then compile ./tmux-spaces.
set -euo pipefail

cd "$(dirname "$0")"

go test ./...
go build -o tmux-spaces ./cmd/tmux-spaces

echo "built ./tmux-spaces"
