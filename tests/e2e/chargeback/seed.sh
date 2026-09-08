#!/usr/bin/env bash
# Seed a running chargeback binary for the browser e2e (#6867).
#
# Everything a customer would have is created through the PUBLIC API as the
# operator (the binary trusts the forward-auth header the CI job configures,
# exactly like bp-oidc-gate does on a Sovereign). The only thing the API has
# no endpoint for is the usage ledger itself — the collectors write it — so
# the hourly records land through psql, the way a collector pass would.
#
# The window is the FIRST SEVEN DAYS OF THE PREVIOUS MONTH: always complete,
# always inside one calendar month, so the statement run and the explorer
# agree to the cent on a fixed number the spec can assert:
#   ECS  7 d × 24 h × 1 instance-hour × 0.5      =  84.000
#   EVS  7 d × 24 h × 100 GB × 0.001 per GB-hour =  16.800
#   list subtotal                                 = 100.800 OMR
set -euo pipefail

CB_BASE="${CB_BASE:-http://127.0.0.1:18080}"
CB_OPERATOR="${CB_OPERATOR:-e2e-operator@example.invalid}"
CB_HEADER="${CB_HEADER:-X-Forwarded-Email}"
CB_PG="${CB_PG:-postgres://chargeback:chargeback@127.0.0.1:15432/chargeback?sslmode=disable}"
OUT="${1:-seed.env}"

api() { # method path [json]
  local m="$1" p="$2" b="${3:-}"
  if [ -n "$b" ]; then
    curl -fsS -X "$m" -H "$CB_HEADER: $CB_OPERATOR" -H 'Content-Type: application/json' -d "$b" "$CB_BASE/api/v1$p"
  else
    curl -fsS -X "$m" -H "$CB_HEADER: $CB_OPERATOR" "$CB_BASE/api/v1$p"
  fi
}
jqv() { python3 -c "import sys,json; print(json.load(sys.stdin)$1)"; }

# First day of the previous month, UTC.
SEED_FROM=$(date -u -d "$(date -u +%Y-%m-01) -1 month" +%Y-%m-%d)
SEED_TO=$(date -u -d "$SEED_FROM +7 days" +%Y-%m-%d)
SEED_PERIOD=${SEED_FROM%-*}

echo "operator identity: $(api GET /auth/me | jqv "['role']")"

BOOK=$(api POST /pricebooks '{"name":"E2E list","scope":"cloud","currency":"OMR","annual_divisor":8760,"bill_stopped":"none"}' | jqv "['id']")
api PUT "/pricebooks/$BOOK/items" '{"items":[{"sku":"ecs.m7n.xlarge.8","unit":"instance-hour","unit_price":"0.5","description":"4 vCPU 32 GB"},{"sku":"evs.ssd.gb","unit":"gb-hour","unit_price":"0.001","description":"SSD per GB"}]}' >/dev/null
# The price book is assigned PER SOURCE (DESIGN.md §2), never to the
# customer: the customer create body carries no book at all, and the file
# source is created with the cloud book that rates it.
CUST=$(api POST /customers "{\"slug\":\"acme-e2e\",\"name\":\"Acme E2E\",\"admin_email\":\"admin@acme-e2e.example\",\"billing_mode\":\"chargeback\",\"start_date\":\"$SEED_FROM\"}" | jqv "['id']")
api PATCH "/customers/$CUST" '{"status":"active"}' >/dev/null
SRC=$(api POST "/customers/$CUST/sources" "{\"kind\":\"file\",\"region\":\"me-east-1\",\"project_id\":\"e2e-project\",\"price_book_id\":\"$BOOK\"}" | jqv "['id']")
# Prove the assignment landed on the source and carries the right layer.
SRC_LAYER=$(api GET "/sources/$SRC" | jqv "['layer']")
SRC_BOOK=$(api GET "/sources/$SRC" | jqv "['price_book_id']")
[ "$SRC_LAYER" = "cloud" ] || { echo "FAIL: source layer=$SRC_LAYER, want cloud"; exit 1; }
[ "$SRC_BOOK" = "$BOOK" ] || { echo "FAIL: source price_book_id=$SRC_BOOK, want $BOOK"; exit 1; }

psql "$CB_PG" -v ON_ERROR_STOP=1 -q <<SQL
INSERT INTO resource_inventory (source_id, resource_id, kind, name, attrs, first_seen, last_seen)
VALUES ('$SRC', 'vm-e2e-1', 'ecs', 'web-1', '{"flavor":"m7n.xlarge.8","status":"ACTIVE","vcpus":4}', '$SEED_FROM', now()),
       ('$SRC', 'vol-e2e-1', 'evs', 'vol-1', '{"size_gb":100,"volume_type":"SSD","attached_to":"vm-e2e-1"}', '$SEED_FROM', now())
ON CONFLICT DO NOTHING;
INSERT INTO usage_records (customer_id, source_id, resource_id, resource_kind, sku, quantity, unit, window_start, window_end, region, labels)
SELECT '$CUST', '$SRC', 'vm-e2e-1', 'ecs', 'ecs.m7n.xlarge.8', 1, 'instance-hour', h, h + interval '1 hour', 'me-east-1',
       '{"name":"web-1","status":"ACTIVE","flavor":"m7n.xlarge.8"}'::jsonb
FROM generate_series('$SEED_FROM'::timestamptz, '$SEED_TO'::timestamptz - interval '1 hour', interval '1 hour') h
ON CONFLICT DO NOTHING;
INSERT INTO usage_records (customer_id, source_id, resource_id, resource_kind, sku, quantity, unit, window_start, window_end, region, labels)
SELECT '$CUST', '$SRC', 'vol-e2e-1', 'evs', 'evs.ssd.gb', 100, 'gb-hour', h, h + interval '1 hour', 'me-east-1',
       '{"name":"vol-1","volume_type":"SSD","attached_to":"vm-e2e-1","server_status":"ACTIVE"}'::jsonb
FROM generate_series('$SEED_FROM'::timestamptz, '$SEED_TO'::timestamptz - interval '1 hour', interval '1 hour') h
ON CONFLICT DO NOTHING;
SQL

N=$(psql "$CB_PG" -Atc "select count(*) from usage_records where customer_id='$CUST'")
[ "$N" = "336" ] || { echo "FAIL: expected 336 seeded records, got $N"; exit 1; }

cat > "$OUT" <<EOF
CB_SEED_FROM=$SEED_FROM
CB_SEED_TO=$SEED_TO
CB_SEED_PERIOD=$SEED_PERIOD
CB_CUSTOMER_ID=$CUST
CB_BOOK_ID=$BOOK
CB_SOURCE_ID=$SRC
EOF
echo "seeded: customer=$CUST book=$BOOK source=$SRC window=$SEED_FROM..$SEED_TO records=$N"
