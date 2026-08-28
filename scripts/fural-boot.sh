#!/usr/bin/env bash
set -euo pipefail
if [ -f "${HOME}/.env" ]; then set -a; source "${HOME}/.env"; set +a; fi
export PATH="${HOME}/.local/bin:${HOME}/go-sdk/go/bin:${PATH}"
cd "${SCRAPER_ROOT:-/home/dev/www}"
mkdir -p bin
if [ ! -x bin/scraper ] || [ -n "$(find cmd internal go.mod -type f -newer bin/scraper 2>/dev/null | head -1)" ]; then
  CGO_ENABLED=0 go build -o bin/scraper ./cmd/server
fi
exec ./bin/scraper -addr "0.0.0.0:${PORT:-3000}"
