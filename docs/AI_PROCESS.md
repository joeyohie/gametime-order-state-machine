# AI Process Log

Where and why AI was used on this take-home, and how its output was validated or
challenged — kept as a running log while the work happened, not reconstructed
afterward. Tooling: Claude Code.

## Log

### 2026-09-15 — Planning

- AI drafted `PLAN.md` from the prompt PDF: states/transitions, failure-recovery
  flow, interfaces, storage, API shape, package layout, and the test list. The
  draft flags open decisions as **[DECIDE]** items for me rather than resolving
  them silently.
- My review pass of the plan, done as a working session before any code. I made
  sure I understood the problem in my own terms first: worked through what a
  "state machine" actually is here (a status field plus a rulebook of legal
  moves), why payment is stubbed as an interface, the mapping to Stripe's
  PaymentIntents lifecycle (create ≈ create intent, authorize ≈ confirm,
  complete ≈ capture, void ≈ cancel), real-life reasons completion fails after
  authorization (secondary-market inventory vanishing) and why voids fail
  (processor outages, expired auths), why create and authorize are separate
  steps, and why real completion would be async/event-driven — which we captured
  as a README tradeoff instead of building it.
- Changes I made to the AI draft:
  - Go + Gin instead of the stdlib-only proposal — Gin is what I used daily for
    four years; my name is on this code.
  - Restructured to the layered layout I've used on production Go services:
    endpoints → managers → engines / accessors, with the state machine as a pure
    engine and payment, fulfillment, and storage as accessors. Simplified the
    tree: main.go at the repo root (one small binary — no cmd/ nesting), plan
    and process docs under docs/.
  - Agreed fulfillment belongs behind its own accessor (completion failure is a
    fulfillment problem, not a payment problem).
  - Kept the in-memory store, added a mutex after we talked through Gin's
    per-request goroutines, and added an append-only CSV transition log so I can
    watch orders move through states during my manual verification. Source of
    truth stays in memory; the CSV is an audit/debug sink.
  - Flagged that the states-and-transitions section was ambiguous (the prompt
    overloads "complete" as both an action and a state, and it wasn't obvious
    why a failure after authorization can't land in `rejected`). Had it
    rewritten with explicit state/transition/action vocabulary, a diagram, and
    the rejected-vs-cancelled distinction spelled out — if it confused me, it
    would confuse a reviewer skimming the README.
  - Added customer notification on cancellation to the with-more-time list.
  - Renamed the `rejected` state to `payment_declined`: it pairs with
    `payment_authorized` (two outcomes of the same action) and names the actual
    cause. Decided against `order_` prefixes on every state as redundant — the
    states live on the Order. Retryable authorization after a decline noted as
    out of scope.

### 2026-09-16 — Second plan review round

- Naming passes on the accessors: `OrderStore.Save` → `Update` (Create already
  exists, so Update is the honest CRUD name); `TicketFulfillment.Deliver` →
  `Fulfill` (the spec doesn't define delivery; Fulfill names the abstract
  business step between hold and capture without over-claiming a mechanism);
  `TransitionLog` → `StateTransitionLog`.
- Challenged the endpoint paths against industry convention and confirmed the
  plan's shape (plural collection, member, action sub-resources) matches
  REST/Stripe practice; added `GET /orders` as the list endpoint.
- Asked whether the magic-amount failure triggers are a legitimate practice;
  confirmed against Stripe's test-card pattern and documented that the specific
  amounts are arbitrary sentinels, with unit tests configuring mocks explicitly
  instead.
- Strengthened the test plan: every manager scenario now asserts on the
  persisted order fetched back through the store, not just the return value.
- Revisited whether orders themselves should also be written to a CSV for
  visibility and decided no: a second mutable copy of state is a dual-write
  problem. Current-state visibility comes from `GET /orders`; the append-only
  transition CSV covers the chronological view.
- Simplified the mock failure-trigger amounts to 1000/2000/3000 cents with a
  mnemonic: the bigger the amount, the further into the pipeline the failure
  (decline → failed fulfillment with clean void → failed fulfillment and failed
  void). Rejected 1/2/3 — one-cent orders read as test noise.
- Trimmed this process doc to cover the take-home work itself.

### 2026-09-16 — Independent plan review (fresh Claude Code session), triaged

Before writing code I had a separate session review PLAN.md against the prompt
PDF with three questions: is this right-sized for a ~3-hour take-home, what can
be simplified, what's missing. Findings and what I did with them:

- **Accepted — dropped the CSV transition log.** The reviewer pointed out the
  order's own `history` already is the timestamped audit trail the spec asks for,
  and the CSV added a fourth accessor, a second mutex, and file I/O the spec never
  asks for. Replaced with one structured zap log line per transition in the
  manager, which still gives me the play-by-play during manual verification.
- **Accepted — concurrency belongs in the manager, not the store.** I had written
  that the store mutex "serializes transitions." It doesn't: it only makes each
  Get/Update atomic, so two concurrent `complete` calls could both fulfill and
  both void. The read → decide → act → write sequence lives in the manager, so
  that's where the lock goes. In real life the database would do this better (row
  lock or version check); noted as with-more-time.
- **Accepted — tests only if I can defend them.** Manager scenarios are the graded
  core; engine table tests stay because they're simple; endpoint tests trimmed to
  one happy path plus one 409, written last.
- **Accepted — README ≈ one page in the prompt's four-section order**, since it's
  what the live reviewer reads first. Depth lives in docs/.
- **Accepted — tiny `pkg/models` package** for Order/State/HistoryEntry plus the
  sentinel errors, so no import cycles. Reviewer suggested the errors live there
  rather than a `pkg/errors` package, which would shadow the stdlib `errors`.
- **Accepted — creation is a history entry** (`"" → initialized`), so a happy-path
  order has three entries and tests assert three.
- **Changed the reviewer's suggestion — decline vs. processor outage.** Reviewer
  proposed treating any authorize error as a decline for simplicity. I chose to
  handle the distinction: a real decline transitions to `payment_declined`
  (200); any other processor error leaves the order `initialized` and returns
  502, so authorize is retryable. It's a small amount of code and it's the kind
  of thing that matters in payments.
- **Accepted — `needs_attention` keeps the auth ID.** I had to ask what "auth ID"
  meant: it's the processor's identifier for the hold (Stripe's PaymentIntent
  id), distinct from our order ID, and it's what void is called with and what
  ops would use to void manually.
- **Clarified, no change — `GET /orders`.** I read the spec's "querying its
  current state + history" as requiring the list endpoint; the reviewer read
  "its" as one order, satisfied by `GET /orders/:id` returning `history`. Kept
  the list endpoint as a cheap extra, built last.
- **Accepted — build order with the graded core first**, one commit per slice,
  and a process-log entry per slice covering what AI drafted, what I changed, and
  how I validated it (tests, demo.sh output hand-checked, bugs caught).
- **Accepted — fresh-clone details**: go.mod module path matches the GitHub repo,
  Go version pinned.

- **Challenged and reversed — where sentinel errors live.** The reviewer had
  put them in `pkg/models`. I asked whether that was actually Go best practice;
  it isn't — the convention is that an error lives in the package that produces
  it (`sql.ErrNoRows`, `os.ErrNotExist`). Plan now defines each error in its
  producing package (`accessors.ErrOrderNotFound`, `accessors.ErrPaymentDeclined`,
  `engines.ErrInvalidTransition`, `managers.ErrPaymentProcessor`) with one
  `statusFor(err)` helper in endpoints.
- **Made the AI explain the manager-lock claim until I could reproduce the race
  myself**: a store mutex is released between the manager's Get and Update, so
  two concurrent `complete` calls both read `payment_authorized`, both fulfill,
  and the last write wins — plus a data race on the shared history slice since
  the map holds pointers. Only the manager sees check → act → write as one
  sequence, so that's where the lock goes. Create vs. complete on different
  orders never conflict; a global lock just makes them wait (throughput cost,
  noted in README).

- **Overrode the reviewer on error placement, again.** After hearing that
  producer-package errors are the stdlib convention, I still chose one shared
  `pkg/apperrors` package: four errors in one file that reads top to bottom is
  easier to review, and the errors are a cross-layer contract like the models.
  Kept separate from `models` so that stays pure data.
- **Asked whether a database would remove the need for locking entirely.** It
  removes the Go mutex (which can't coordinate across service instances anyway)
  but not its job: real systems close the same race with a conditional update
  that claims the order into an intermediate state before any side effect, not
  with a transaction held open across processor calls. Captured in PLAN §5 as
  README tradeoff material.

- **Simplified after re-reading the plan cold.** Two mutexes (manager + store)
  read as more complicated than the problem. Asked whether a real database would
  be simpler; answer was no for a three-hour take-home (schema, driver, claiming
  logic — more to defend) and in-memory shows the race instead of hiding it.
  Result: one lock in the manager around every method, store is a plain map with
  no lock that hands out copies so no pointer into the map escapes. And errors
  moved from a separate `apperrors` package into `pkg/models/errors.go` — one
  fewer package, same one-sentence defense.

- **Cut my own earlier addition.** I had asked for decline vs. processor outage
  to be handled as a distinct path (502, magic amount, fifth test). On a final
  read that's a fourth failure mode the spec doesn't list. Kept only the
  unavoidable branch (a non-decline error is returned, not treated as a decline)
  falling through to a plain 500; dropped the sentinel, the magic amount, and the
  test. One README sentence instead.

Plan is now v3. Implementation starts from here.

## Implementation

### Slice 1 — module + models (2026-09-16)

- AI wrote `go.mod` (module path = the GitHub repo path so a fresh clone builds
  as-is), `.gitignore`, and `pkg/models` (types + the three sentinel errors)
  straight from PLAN §4. I reviewed the struct fields and JSON tags against the
  response shape in PLAN §6.
- Validation: `gofmt -l` clean, `go vet ./...` clean. No tests at this layer —
  there is no logic to test.

### Slice 2 — accessors (2026-09-16)

- AI wrote the three interfaces, the two mocks with the magic-amount rules, and
  the in-memory store from PLAN §4–§6. Two signature changes from the plan,
  both made while writing and reflected back into PLAN: `Fulfill` takes the
  order value rather than just the ID (the mock decides by amount, and a real
  fulfillment service needs the order anyway), and the store uses values instead
  of pointers so copying is the natural behavior, with a `clone` helper for the
  one reference field (History). `List()` added for `GET /orders`.
- The mock processor remembers the amount behind each hold so `Void` can fail
  for the 3000-cent demo amount without a second lookup path.
- Validation: `go vet` clean; two store tests pass — CRUD plus a test proving a
  returned order can be mutated without changing what the store holds (the
  copy guarantee the manager lock design relies on). `gofmt -l` caught one
  misaligned const block; fixed with `gofmt -w`.

### Slice 3 — engine (2026-09-16)

- AI wrote the engine as a transition table (`map[from][]to`) plus one
  `Transition` method that validates, mutates state, and appends the history
  entry, and a `CanTransition` query the manager uses for the up-front legality
  check. Creation (`"" -> initialized`) is a row in the table so it is recorded
  the same way as everything else. No clock injection: timestamps come from
  `time.Now().UTC()` and the test asserts non-zero and non-decreasing rather
  than exact values.
- Validation: `go vet` clean; three tests pass. The table test covers every
  legal move, skipped steps, a repeated step, decline-after-authorization (the
  exercise's core distinction), and an exit attempt from each terminal state.
  A second test proves history is appended with detail and ordered timestamps;
  a third proves a rejected transition leaves the order untouched.
