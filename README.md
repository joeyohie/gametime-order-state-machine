# Order State Machine

A small Go service that models a checkout order's lifecycle with
stage-dependent failure recovery, built for Gametime's take-home. About five
hours total: one planning, three implementing with AI assistance, one
reviewing and writing this README. The AI process is documented in
[docs/AI_PROCESS.md](docs/AI_PROCESS.md).

## What I built and why

A happy-path order moves from `initialized → payment_authorized → complete`.
What happens on failure depends on where it happens:

| Failure                               | Recovery                        | Ends in            |
| ------------------------------------- | ------------------------------- | ------------------ |
| Payment declined                      | none needed, no hold exists     | `payment_declined` |
| Fulfillment fails after authorization | void the payment hold           | `cancelled`        |
| Fulfillment fails AND the void fails  | surface both errors for a human | `needs_attention`  |

The API exposes **actions**, not states: clients ask to `authorize` or
`complete`, and the service decides the resulting state. Every transition is
recorded on the order with a timestamp, and failure entries carry the error
text so nothing is silently swallowed. An order that lands in `needs_attention`
keeps its payment id so ops can void the hold manually.

Layout is endpoints → manager → engine / accessors:

- `pkg/endpoints` — Gin handlers: JSON in and out, status codes, no logic.
- `pkg/managers` — orchestration: calls the accessors, asks the engine to record
  the outcome, persists, logs. Holds the one lock that makes each action atomic.
- `pkg/engines` — the state machine: a table of allowed transitions and one
  `Transition` that validates, applies, and records. Pure; no I/O.
- `pkg/accessors` — payment processor, ticket fulfillment, and order store
  behind interfaces, with mocks. Payment is stubbed as the prompt asks;
  fulfillment is its own accessor because completion failure is a fulfillment
  problem, not a payment problem.

## How to run

Requires Go 1.21 or newer. go.mod pins 1.27, and older Go downloads it
automatically. No database, no setup.

```bash
go test -race ./...
go run .
```

Then in another terminal, `./demo.sh` runs all four scenarios, or follow
[docs/CURL.md](docs/CURL.md) one command at a time. The mocks pick the outcome
by amount: **1000** cents declines, **2000** fails fulfillment and voids
cleanly, **3000** fails fulfillment and the void, anything else completes.

| Method | Path                  | Purpose                                 |
| ------ | --------------------- | --------------------------------------- |
| POST   | /orders               | create (`{"amount_cents": 5000}`) → 201 |
| POST   | /orders/:id/authorize | attempt the payment hold                |
| POST   | /orders/:id/complete  | attempt fulfillment; runs the recovery  |
| GET    | /orders/:id           | current state + full history            |
| GET    | /orders               | all orders, in creation order           |

Business outcomes are 200s with the outcome in `state`. 4xx is for caller
errors: 400 bad payload, 404 unknown order, 409 action not allowed from the
current state.

## Tradeoffs

- **`needs_attention` returns 200, not 5xx.** The request was processed and the
  state persisted; a 5xx could invite a retry that gets a 409 and would count a
  handled partial failure as a server failure. Clients read `state`, exactly as
  they do for `payment_declined`; ops gets an error-level log line.
- **No database, one global lock.** To keep the service runnable out of the
  box I used an in-memory map, with a mutex in the manager so only one action
  runs at a time. The lock is in the manager because it is the only layer
  that sees check, act, and write as one unit. The cost: unrelated orders wait
  on each other, state is lost on restart, and only one instance can run. With
  Postgres, a conditional update on the order row replaces the lock.
- **Demo amounts trigger failures.** Specific order amounts make the mocks
  fail at a chosen stage, so every scenario is reachable with curl and no test
  hooks in the API. Unit tests do not rely on this; they use test doubles that
  fail on command. Real processors do the same in sandbox mode with test card
  numbers.
- **Hand-written test fakes, not mockgen.** The repo has exactly two fakes in
  one test file. mockgen is what I reach for on a team, but a generation
  pipeline here would be more scaffolding than test code; I'd switch once the
  interface count justified it.
- **The failure-recovery flow runs inside the `complete` request.** In
  production it would be driven by a fulfillment worker or webhook; the
  manager method is the same either way.
- **A processor error that is not a decline** (timeout, outage) is not
  modeled; the spec lists three failure modes and this would be a fourth. The
  code returns a 500 but the mock never triggers it, so the demo cannot reach
  it. In real life the caller would retry with an idempotency key.

## With more time

- A `void_pending` state with a retry worker for transient processor errors
  before escalating to `needs_attention`; alerting on that error log line;
  immediate notification to the customer and to Gametime support.
- Database setup: Postgres with a conditional update (`WHERE state = 'payment_authorized'`)
  replacing the global lock. Three tables: orders, payments (foreign key to
  orders via orderID), and order_transitions (one row per history entry).
  Primary key on orderID, enums for events and state, and any other field
  validators as needed. Constraint violations the caller can fix map to 4xx;
  infrastructure errors stay 5xx.
- Async completion driven by a fulfillment worker or webhook consumer.
- A response type separate from the domain model, with raw error text redacted
  from client-facing responses in favor of user-safe reason codes.
- An ops resolution flow for `needs_attention` orders, voiding or refunding
  from a dashboard, plus customer and support notifications.
- The usual production concerns: API authorization, rate
  limiting, metrics and tracing (the structured logs are a start), and
  analytics on user behavior through the funnel.

The full design, including every decision and its alternatives, is in
[docs/PLAN.md](docs/PLAN.md).

---

© 2026 Jo Whang. Shared for Gametime's hiring evaluation; no license granted for reuse.
