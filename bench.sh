#!/usr/bin/env bash
set -euo pipefail

# Run litelog benchmarks and save results with timestamp.
# Usage: ./bench.sh [label]
#   label: optional tag appended to filename (e.g. "pull-api")

LABEL="${1:-}"
TIMESTAMP=$(date +%Y%m%d-%H%M%S)
OUTDIR="benchmarks"
mkdir -p "$OUTDIR"

if [ -n "$LABEL" ]; then
    OUTFILE="${OUTDIR}/bench-${TIMESTAMP}-${LABEL}.txt"
else
    OUTFILE="${OUTDIR}/bench-${TIMESTAMP}.txt"
fi

echo "Running benchmarks..."
echo "Output: $OUTFILE"

go test -bench=. -benchmem -count=3 -timeout=300s ./... 2>&1 | tee "$OUTFILE"

echo ""
echo "Saved to $OUTFILE"
