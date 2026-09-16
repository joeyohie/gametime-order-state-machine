# Manual walkthrough with curl

`demo.sh` runs every scenario in one go. This is the same thing one command at a
time, with the expected status code and state next to each step, for hand
verification and for the live review.

Start the server in one terminal:

```bash
go run .
```

Everything below runs in a second terminal. `jq` pretty-prints the JSON; the
`-w '%{stderr}status: ...'` sends the status code to stderr so it shows up after
the body without breaking the pipe. Paste the `id` from each create response
into the commands that follow it.

The mock processor and fulfillment decide the outcome by amount (cents). The
bigger the amount, the further into the pipeline the failure happens:

| amount_cents  | outcome                                           |
| ------------- | ------------------------------------------------- |
| 1000          | authorize declined → `payment_declined`           |
| 2000          | fulfillment fails, void ok → `cancelled`          |
| 3000          | fulfillment fails, void fails → `needs_attention` |
| anything else | happy path → `complete`                           |

## Happy path (amount 5000)

Create — expect **201**, state `initialized`, one history entry:

```bash
curl -s -w '%{stderr}status: %{http_code}\n' -X POST localhost:8080/orders -H 'Content-Type: application/json' -d '{"amount_cents": 5000}' | jq
```

Authorize — expect **200**, state `payment_authorized`, a `payment_id`:

```bash
curl -s -w '%{stderr}status: %{http_code}\n' -X POST localhost:8080/orders/PASTE_ID/authorize | jq
```

Complete — expect **200**, state `complete`:

```bash
curl -s -w '%{stderr}status: %{http_code}\n' -X POST localhost:8080/orders/PASTE_ID/complete | jq
```

Check — expect **200**, three history entries with rising timestamps:

```bash
curl -s -w '%{stderr}status: %{http_code}\n' localhost:8080/orders/PASTE_ID | jq
```

## Payment declined (amount 1000)

Create — expect **201**:

```bash
curl -s -w '%{stderr}status: %{http_code}\n' -X POST localhost:8080/orders -H 'Content-Type: application/json' -d '{"amount_cents": 1000}' | jq
```

Authorize — expect **200**, state `payment_declined`, the decline reason in
the last history entry's `detail`, no `payment_id` (no hold ever existed):

```bash
curl -s -w '%{stderr}status: %{http_code}\n' -X POST localhost:8080/orders/PASTE_ID/authorize | jq
```

Complete — expect **409**; a declined order is terminal:

```bash
curl -s -w '\nstatus: %{http_code}\n' -X POST localhost:8080/orders/PASTE_ID/complete
```

## Fulfillment fails, void succeeds (amount 2000)

Create — expect **201**:

```bash
curl -s -w '%{stderr}status: %{http_code}\n' -X POST localhost:8080/orders -H 'Content-Type: application/json' -d '{"amount_cents": 2000}' | jq
```

Authorize — expect **200**, state `payment_authorized`:

```bash
curl -s -w '%{stderr}status: %{http_code}\n' -X POST localhost:8080/orders/PASTE_ID/authorize | jq
```

Complete — expect **200**, state `cancelled`; the last `detail` names the
fulfillment error and says the payment hold was voided:

```bash
curl -s -w '%{stderr}status: %{http_code}\n' -X POST localhost:8080/orders/PASTE_ID/complete | jq
```

## Fulfillment fails, void fails (amount 3000)

Create — expect **201**:

```bash
curl -s -w '%{stderr}status: %{http_code}\n' -X POST localhost:8080/orders -H 'Content-Type: application/json' -d '{"amount_cents": 3000}' | jq
```

Authorize — expect **200**, state `payment_authorized`:

```bash
curl -s -w '%{stderr}status: %{http_code}\n' -X POST localhost:8080/orders/PASTE_ID/authorize | jq
```

Complete — expect **200**, state `needs_attention`; the last `detail` carries
BOTH errors, and `payment_id` is still present so the hold can be voided
manually. The server terminal shows one error-level log line for this order:

```bash
curl -s -w '%{stderr}status: %{http_code}\n' -X POST localhost:8080/orders/PASTE_ID/complete | jq
```

## Caller errors

Complete before authorize on a fresh order — expect **409**:

```bash
curl -s -w '\nstatus: %{http_code}\n' -X POST localhost:8080/orders/PASTE_FRESH_ID/complete
```

Authorize an already-authorized order — expect **409**:

```bash
curl -s -w '\nstatus: %{http_code}\n' -X POST localhost:8080/orders/PASTE_AUTHORIZED_ID/authorize
```

Unknown order — expect **404**:

```bash
curl -s -w '\nstatus: %{http_code}\n' localhost:8080/orders/ord_nope
```

Negative amount — expect **400**:

```bash
curl -s -w '\nstatus: %{http_code}\n' -X POST localhost:8080/orders -H 'Content-Type: application/json' -d '{"amount_cents": -5}'
```

Missing body — expect **400**:

```bash
curl -s -w '\nstatus: %{http_code}\n' -X POST localhost:8080/orders
```

## Snapshot

Every order, in creation order:

```bash
curl -s localhost:8080/orders | jq
```
