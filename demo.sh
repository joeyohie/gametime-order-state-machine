#!/usr/bin/env bash
# Walks every scenario against a running server (default http://localhost:8080).
# Start the server first: go run .
#
# The mock processor and fulfillment decide by amount (cents):
#   1000  authorize is declined                     -> payment_declined
#   2000  fulfillment fails, void succeeds          -> cancelled
#   3000  fulfillment fails AND void fails          -> needs_attention
#   else  happy path                                -> complete
set -euo pipefail

BASE="${BASE:-http://localhost:8080}"

# Pretty-print JSON with jq if available, otherwise print raw.
pretty() { if command -v jq >/dev/null; then jq .; else cat; fi; }

# id extracts the order id from a create response without depending on jq.
id() { sed -n 's/.*"id":"\([^"]*\)".*/\1/p'; }

scenario() {
  local title="$1" amount="$2"
  echo
  echo "=== $title (amount_cents=$amount) ==="
  local order_id
  order_id=$(curl -s -X POST "$BASE/orders" -H 'Content-Type: application/json' \
    -d "{\"amount_cents\": $amount}" | id)
  echo "created $order_id"
  echo "--- authorize"
  curl -s -X POST "$BASE/orders/$order_id/authorize" | pretty
  echo "--- complete"
  curl -s -X POST "$BASE/orders/$order_id/complete" | pretty
}

scenario "1. Happy path"                              5000
scenario "2. Payment declined"                        1000
scenario "3. Completion fails, void succeeds"         2000
scenario "4. Completion fails, void fails"            3000

echo
echo "=== All orders (creation order) ==="
curl -s "$BASE/orders" | pretty
