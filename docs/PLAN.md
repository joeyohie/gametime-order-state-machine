# Implementation Plan — Order State Machine

Status: v3, revised after Jo's second review round and the independent plan review
(2026-09-16). Structure, framework, and storage decisions below are Jo's.

## 1. Problem restated

A checkout order moves `initialized → payment_authorized → complete`. Failures are
stage-dependent: a payment decline simply rejects the order (no cleanup); a completion
failure after authorization requires voiding the payment hold and cancelling the
order; if that void *also* fails, the order lands in `needs_attention` with both
errors surfaced — never silently swallowed. The service enforces valid transitions,
records timestamped history, and exposes a small API. Payment is stubbed behind an
interface.

Two state machines exist in real life — the processor's (e.g. Stripe's
PaymentIntent) and the marketplace's order machine. This service is the latter.
Mapping to Stripe: create order ≈ create PaymentIntent (manual capture);
authorize ≈ confirm (places the hold); complete's payment half ≈ capture;
void ≈ cancel on an uncaptured intent.

Scope guard: the prompt says roughly three hours and "don't over-engineer." Every
item below is either required by the prompt or cheap enough to defend in a live
review. If it can't be defended in one sentence, it doesn't go in.

## 2. States and transitions

Vocabulary, since the prompt overloads the words:

- A **state** is where the order IS right now — a noun, one at a time. Six exist:
  `initialized`, `payment_authorized`, `complete`, `payment_declined`,
  `cancelled`, `needs_attention`.
- A **transition** is one recorded arrow between two states, with a timestamp.
- An **action** is what gets *attempted* (authorize, attempt-completion).
  **Actions succeed or fail; states don't.** One action produces different
  transitions depending on its outcome. Note the naming collision: "complete" is
  both the action the API exposes and the success-destination state — read the
  action as "attempt fulfillment."

```
                   authorize OK                    fulfillment OK
 initialized ──────────────────► payment_authorized ─────────────► complete ⛔
      │                                 │
      │ authorize declined              │ fulfillment fails → attempt void
      ▼                                 │
 payment_declined ⛔                    ├── void OK ──────► cancelled ⛔
                                        └── void fails ───► needs_attention ⛔
```

Six states, four terminal (⛔). Any arrow not shown is invalid and rejected.

| From                 | To                  | Trigger                                    |
|----------------------|---------------------|--------------------------------------------|
| (none)               | initialized         | order created                              |
| initialized          | payment_authorized  | authorize succeeds                         |
| initialized          | payment_declined ⛔  | authorize declined                         |
| payment_authorized   | complete ⛔          | fulfillment succeeds                       |
| payment_authorized   | cancelled ⛔         | fulfillment fails → void succeeds          |
| payment_authorized   | needs_attention ⛔   | fulfillment fails → void ALSO fails        |

Creation is recorded as the first history entry (`from: "" → to: initialized`),
so the creation timestamp lives in the same list as every other transition and a
happy-path order has exactly three entries.

Not modeled (spec lists three failure modes; this would be a fourth): a processor
error on authorize that is *not* a decline (timeout, outage). The manager simply
returns the error unchanged, the order stays `initialized`, and the handler's
default case makes it a 500. One README sentence, live-review material, no
dedicated code path.

Naming: the prompt says "reject the order" but doesn't mandate a state name.
`payment_declined` pairs with `payment_authorized` (the two outcomes of the same
action) and states the cause directly. No `order_` prefix on any state — the
states live on the Order, so the context is free (Go: `StatePaymentDeclined`).

Why `payment_declined` and `cancelled` are different states: `payment_declined`
means the decline happened and **no hold ever existed** — nothing to clean up.
`cancelled` means a hold existed and was successfully voided. Once an order
reaches `payment_authorized`, failure can never be a clean decline; it must go
through the void. This distinction is the exercise's core.

Design stance: **clients never set states.** The API exposes actions (authorize,
complete); the manager and engine decide the resulting state. A client-writable
state field would bypass the machine entirely.

Why create and authorize are separate steps (defense material): the payment method
arrives after checkout starts; authorization is retryable against the same order
(and sometimes async, e.g. 3D Secure); ticketing reserves inventory at create while
money commits at authorize; amounts change between the two; and
created-but-never-authorized orders are the abandoned-checkout record —
`initialized` is that state.

`needs_attention` is terminal in this service; resolution is a manual/ops flow →
README "with more time."

## 3. Action flows

Two identifiers matter throughout. The **order ID** is ours. The **payment ID** is
the processor's identifier for the payment object (Stripe: the PaymentIntent id),
which outlives the authorization stage — the same id is used to void now or
capture later. Void is called with it because the processor doesn't know our
order IDs. The order stores both, and an order in `needs_attention` **keeps its
payment ID** so ops can find and void the hold in the processor dashboard.

### Authorize — `OrderManager.Authorize(orderID)`

1. Engine check: legal from current state? (must be `initialized`; else 409)
2. Call `PaymentProcessor.Authorize`.
3. OK → store payment ID, transition to `payment_authorized`.
4. `ErrPaymentDeclined` → transition to `payment_declined`; history detail
   carries the decline reason. Business outcome → 200.
5. Any other error → returned as-is, no transition (handler default → 500).
   One branch; not a modeled failure mode.

### Complete — `OrderManager.Complete(orderID)` (the heart of the exercise)

1. Engine check: legal from current state? (must be `payment_authorized`; else 409)
2. Call `TicketFulfillment.Fulfill`.
3. OK → transition to `complete`.
4. Fails → call `PaymentProcessor.Void(paymentID)`:
   - Void OK → transition to `cancelled`; history records the fulfillment error
     AND the successful void.
   - Void fails (any error — declined vs. unavailable doesn't matter here, the
     hold still stands) → transition to `needs_attention`; history records BOTH
     errors. The response body surfaces this explicitly — a caller must be able
     to tell "cleanly cancelled" from "charged but unfulfilled."

Business outcomes (declined, cancelled, needs_attention) return `(order, nil)`
from the manager, so the handler never turns them into a 500. The manager returns
a non-nil error only for caller errors (not found, invalid transition) or an
unexpected accessor error.

The whole flow runs inside the one `POST /orders/:id/complete` request. There is no
public void endpoint: voiding is the system's recovery step, never a client
decision. (Real-life production note, stated in README tradeoffs: completion would
be triggered by a fulfillment worker or webhook consumer, not a public endpoint;
the endpoint stands in for that trigger, and the manager method is identical
either way.)

## 4. Architecture

Go + **Gin**. Layers: endpoints → managers → engines / accessors. Shared types
live in a tiny `models` package that everything imports, so there are no import
cycles. The sentinel errors live there too (`errors.go`), for the same reason:
they are part of the contract between layers.

```
main.go                   init: wire accessors, engine, manager, zap, Gin routes
README.md
demo.sh                   curl walk-through of all scenarios
docs/                     PLAN.md, AI_PROCESS.md
pkg/models/               models.go: Order, State, HistoryEntry
                          errors.go: the three sentinel errors
pkg/endpoints/            Gin handlers: JSON in/out, status codes, no business logic
pkg/managers/             OrderManager — orchestration incl. the recovery flow
pkg/engines/              state machine: legal-transition rules, history append
pkg/accessors/            PaymentProcessor (mock), TicketFulfillment (mock),
                          OrderStore (in-memory)
```

`main.go` sits at the repo root: this is a single small binary, so a `cmd/` tree
adds depth without adding information.

- **Models** is the shared domain contract, no logic: `Order{ID, State,
  AmountCents, PaymentID, History}`, `State` (string enum), `HistoryEntry{From, To,
  Event, At, Detail}`, and the three sentinel errors.
- **Engine is pure**: no Gin, no accessors, no I/O. Validates a move against the
  transition table and appends the history entry. Trivially unit-testable.
- **Manager** owns orchestration, holds the one lock (§5), logs each transition
  with zap, and is the only caller of accessors.
- **Accessors** are interfaces with mock implementations:

```go
type PaymentProcessor interface {
    // Authorize returns models.ErrPaymentDeclined on a decline; any other error
    // means the processor couldn't answer (timeout, outage).
    Authorize(ctx context.Context, orderID string, amountCents int64) (paymentID string, err error)
    Void(ctx context.Context, paymentID string) error
}

type TicketFulfillment interface {
    // Fulfill is the business step between hold and capture: secure the
    // seller's tickets for this order. The spec only requires that completion
    // CAN fail after authorization; this accessor is where that failure lives.
    // Takes the order (not just the ID) so the mock can decide by amount.
    Fulfill(ctx context.Context, order models.Order) error
}

type OrderStore interface {
    // Values, not pointers. Get/Update/List deep-copy History so nothing
    // outside the store ever shares its backing array. Not safe for concurrent
    // use on its own — the manager serializes all access.
    Create(o models.Order) error
    Get(id string) (models.Order, error)
    Update(o models.Order) error
    List() []models.Order
}
```

Sentinel errors all live in `pkg/models/errors.go`, imported by every layer. The
endpoint has one small `statusFor(err) int` helper that checks them with
`errors.Is`. Jo's call: the Go stdlib convention is producer-package errors
(`sql.ErrNoRows`), but with three errors across three packages a single file that
reads top to bottom is easier to review, and the errors are a cross-layer
contract just like the types. A separate `apperrors` package was considered and
rejected as one more package for no gain.

| Error                   | Raised by                         | HTTP |
|-------------------------|-----------------------------------|------|
| `ErrOrderNotFound`      | store                             | 404  |
| `ErrPaymentDeclined`    | PaymentProcessor contract (mock)  | (business outcome, 200) |
| `ErrInvalidTransition`  | engine                            | 409  |
| anything else           | —                                 | 500  |

Fulfillment is its own accessor (not overloaded onto payment) because completion
failure is a fulfillment problem — no tickets left, transfer failed — not a
payment problem.

## 5. Storage, concurrency, logging

- **OrderStore**: in-memory `map[string]models.Order`, no lock of its own. Get
  and Update copy (the history slice included) so nothing outside the store ever
  holds a pointer into the map. No database, no setup — `go run .` must be the
  entire "how to run." Why not a database even a small one: it adds a schema, a
  driver, and the claiming logic below — more to write and defend in a
  three-hour take-home that already stubs payment behind an interface.
  Persistence is stubbed the same way; Postgres + outbox is with-more-time.
- **One lock, in the manager.** Every manager method (reads included) takes one
  `sync.Mutex`, and the manager is the only caller of the store, so the map is
  never touched outside that lock. Why the manager and not the store: a lock
  inside the store would be released between the manager's Get and its Update,
  and that gap is where the race lives — two concurrent `complete` calls both
  Get `payment_authorized`, both pass the engine check, both Fulfill, last write
  wins. The invariant is "check state, act, write" as one unit, and only the
  manager sees all three steps. One global lock serializes all orders, which
  costs throughput, not correctness; honest in the README.
  What replaces the mutex in real life (README material): a Go mutex can't
  coordinate across multiple service instances, so the coordination moves into
  the database — not as a lock held across the processor calls (a transaction
  open during network calls is a known anti-pattern) but as a conditional
  update that claims the order before any side effect:
  `UPDATE orders SET state='completing' WHERE id=? AND state='payment_authorized'`.
  Exactly one concurrent request changes a row; the loser gets 409. The state
  column is the lock. Plus idempotency keys on processor calls so a retry can't
  double-charge.
- **Logging**: `zap` structured logger, one line per transition emitted by the
  manager (`order_id`, `from`, `to`, `event`, `detail`), plus one line per
  action error. This is how Jo watches orders move during manual verification.
  Nothing is written to disk — the order's own `history` is the audit trail, and
  `GET /orders/:id` is how it's queried. (A CSV transition log was considered and
  dropped: it duplicated the history the spec already asks for and added file
  I/O the spec never asks for.)

## 6. API (Gin, JSON)

| Method | Path                    | Purpose                                   |
|--------|-------------------------|-------------------------------------------|
| POST   | /orders                 | create order (amount_cents, event name)   |
| POST   | /orders/:id/authorize   | attempt payment authorization             |
| POST   | /orders/:id/complete    | attempt completion (runs recovery flow)   |
| GET    | /orders/:id             | current state + full history (required)   |
| GET    | /orders                 | list all orders — extra, built last       |

Path shape follows REST convention (and Stripe's): plural collection `/orders`,
member `/orders/:id`, member actions as sub-resources (`/orders/:id/authorize`).

Status codes: business outcomes are 200s with the outcome in the body — a declined
payment is a successful API call that resulted in `payment_declined`; a
`needs_attention` landing is a 200 whose body says so. 4xx is reserved for caller
errors: 400 bad payload, 404 unknown order, 409 invalid transition. Anything
unexpected is a 500. Mirrors processor-API conventions.

Response shape (all endpoints): the order — `id`, `state`, `amount_cents`,
`payment_id` if any, and `history: [{from, to, event, at, detail}]`, where `detail`
carries error text on failures (this is where "don't swallow" is visible).

Mock failure triggers, so every scenario is curl-able with no test hooks in the
API — by amount (cents), with a mnemonic: the bigger the amount, the further into
the pipeline the failure happens:
- `1000` ($10) → authorize declines (fails at step 1)
- `2000` ($20) → fulfillment fails, void succeeds (fails at step 2, clean recovery)
- `3000` ($30) → fulfillment fails AND void fails (fails at step 2 AND recovery)
- anything else → happy path

Documented in README as demo levers; unit tests configure mocks explicitly instead
of using magic numbers. Precedent: Stripe's test card numbers work exactly this
way (4242… succeeds, 4000…0002 declines) — sentinel values that deterministically
trigger each path in a test processor.

## 7. Test plan

Rule: only tests Jo can defend in one sentence in the live review. The manager
scenarios are the graded core; everything else is included only if it's simple.

Engine (pure, table-driven, cheap): every listed move allowed; a handful of
illegal moves rejected (complete from initialized, authorize twice, any action on
a terminal state); one history-append check (entry recorded, timestamp set).

Manager (mocked accessors) — the four required scenarios. Every scenario
asserts against the PERSISTED order (fetched back via `store.Get`), not just the
return value — this catches "computed right, never saved" bugs. Assertions per
scenario: final stored state, history contents, and which mock methods were called.
1. **Happy path**: create → authorize → complete; history exactly three entries
   [→initialized, →payment_authorized, →complete]; payment ID stored.
2. **Payment declined** → `payment_declined`; assert Void was NEVER called (mock
   records calls).
3. **Fulfillment fails, void succeeds** → `cancelled`; history holds the
   fulfillment error and the void event.
4. **Fulfillment fails, void fails** → `needs_attention`; history holds BOTH
   errors; payment ID still present.

Endpoints (httptest + Gin): one happy-path end-to-end and one 409. Written last;
cut if time runs short.

Manual verification (Jo, before submitting): `demo.sh` runs curl sequences for all
scenarios using the magic amounts; Jo hand-checks each history JSON against the
zap log output.

## 8. Explicitly NOT building (→ README "with more time")

- Async, event-driven completion: fulfillment worker / webhook consumer driving
  the same manager method; retries/backoff on void; durable execution (Temporal)
  once steps span services
- Idempotency keys on the POSTs
- Per-order locking: in a database, a conditional update into a claiming state
  (`completing`) replaces the global manager mutex — see §5
- Retryable authorization after a decline (real systems allow a new payment
  method against the same order; the prompt makes a decline terminal, so we
  follow it).
- Processor outage on authorize (timeout, 5xx) as a distinct failure mode: today
  it's a plain 500 with the order left in `initialized`. The dangerous version is
  a timeout *after* the processor placed the hold — we don't know a hold exists.
  Real systems use an idempotency key and query the processor before retrying.
- Real persistence (Postgres) + transactional outbox. With a database the
  store becomes safe for concurrent use on its own (row locks, conditional
  updates), so the "not safe on its own; the manager serializes all calls"
  contract on the in-memory accessors goes away.
- Accessors split into subpackages (`accessors/payment` with the interface and
  a Stripe implementation next to the mock, etc.) once a second real
  implementation exists; at three interfaces and ~150 lines, one package with
  one file per accessor is easier to read.
- needs_attention resolution flow (ops tooling, alerting)
- Customer-initiated cancel endpoint (exists in real APIs; out of scope here —
  cancellation only arises as failure recovery)
- Customer notification on cancellation / needs_attention (in real life the order
  holder must be told their order didn't go through; here the state + history is
  the record)
- AuthN/Z, rate limiting, metrics/tracing (the zap transition log is the seed
  of observability)

## 9. Build order

Graded core first, so a half-finished repo still passes the required tests:

1. `go mod init` (module path = GitHub repo path, Go version pinned), `models`
   (types + errors)
2. `engines` + table tests
3. `accessors`: mocks + in-memory store
4. `managers` + the four scenario tests
5. `endpoints` + `main.go` + `demo.sh`
6. `GET /orders`, endpoint tests (if time)
7. README

One commit per slice. `AI_PROCESS.md` gets an entry per slice: what AI drafted,
what Jo changed, how it was validated (tests run, demo.sh output hand-checked,
bugs caught).

## 10. Deliverables checklist

- [ ] Code + tests per above, one commit per slice
- [ ] README ≈ one page, four sections in the prompt's order: what/why, how to
      run (`go run .`), tradeoffs, with-more-time. Link to docs/ for depth.
- [ ] AI_PROCESS.md kept live through implementation and testing, not just planning
- [ ] demo.sh + magic amounts documented
- [ ] Fresh-clone check: `git clone` → `go test -race ./...` → `go run .` →
      `demo.sh`, following only the README. `-race` is the enforcement for the
      "not safe for concurrent use on its own" contracts: the race detector
      fails the run if any accessor is ever touched outside the manager lock.
