# AI Process Log

Where and why AI was used on this take-home, and how its output was validated or
challenged — kept as a running log while the work happened, not reconstructed
afterward. Tooling: Claude Code.

## TL;DR — what I changed and why

AI drafted the plan and each code slice; I reviewed every slice line by line
before it was committed, and hand-verified every scenario against the running
server. The calls that were mine:

- **Architecture.** Rejected the stdlib-only, flat-layout draft for Gin and the
  layered layout I ran in production for four years: endpoints → managers →
  engines / accessors, with a pure engine, an orchestrating manager, and all
  I/O behind accessor interfaces. Fulfillment is its own accessor because
  completion failure is a fulfillment problem, not a payment problem.
- **Concurrency.** Caught that the draft's store mutex did not actually
  serialize transitions (it is released between Get and Update, exactly where
  two concurrent completes both read `payment_authorized`). Moved the lock to
  the manager, the only layer that sees check → act → write as one unit, then
  simplified two mutexes to one with the store handing out copies. `go test
  -race` is the enforcement. Documented what replaces it with Postgres: a
  conditional update that claims the order before any side effect.
- **needs_attention is a 200, not a 5xx.** The request was processed and the
  state persisted; a 5xx invites a retry that would 409 and mis-counts a
  handled partial failure as a server failure. Clients read `state`; ops gets
  an Error log line. With more time: a `void_pending` retry state, alerting,
  and customer/support notification.
- **Scope discipline.** Only what I can defend in one sentence. Cut the draft's
  CSV audit log (the order's history already is the audit trail), a
  processor-outage failure mode the spec doesn't list, an unneeded order
  field, and extra tests.
- **Operability.** Made every log line about an order carry `order_id` and
  `payment_id`, because that is how a payments incident actually gets
  debugged. Raised a processor-outage log from Warn to Error. Set the policy
  of logging an error once, at the layer that picks the status code.
- **Naming and contracts.** `payment_id` over `auth_id` (the processor's id
  outlives the authorization stage). Explicit identifiers throughout.
  Compile-time interface guards on every mock; no interface on the engine,
  because interfaces are for substitution and it has one pure implementation.
  Sentinel errors in one shared file, a deliberate departure from the
  producer-package convention for readability.
- **Tests that prove persistence.** Every manager scenario asserts on the order
  fetched back from the store, not the return value, using explicit fakes with
  call counters rather than the demo's magic amounts. Caught a random-order
  `List()` and a store test that never checked Create persisted.

The rest of this file is the running log those decisions came from.

## Log

### 2026-09-15 — Planning

- AI drafted `PLAN.md` from the prompt PDF: states/transitions, failure-recovery
  flow, interfaces, storage, API shape, package layout, and the test list. The
  draft flags open decisions as **[DECIDE]** items for me rather than resolving
  them silently.
- My review pass of the plan, done as a working session before any code.
  Grounded it in the real domain: the mapping to Stripe's PaymentIntents
  lifecycle (create ≈ create intent, authorize ≈ confirm, complete ≈ capture,
  void ≈ cancel), real-life reasons completion fails after authorization
  (secondary-market inventory vanishing) and why voids fail (processor outages,
  expired auths), why create and authorize are separate steps, and why real
  completion would be async/event-driven — captured as a README tradeoff
  instead of building it.
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
- **Accepted — `needs_attention` keeps the processor's payment ID** (Stripe's
  PaymentIntent id, distinct from our order ID: what void is called with, and
  what ops would use to void manually). While reviewing the models slice I
  renamed it from the reviewer's `auth_id` to `payment_id`: the processor's id
  belongs to the payment object for its whole life (void now, capture later),
  and "auth" only described the stage it was in when stored.
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
  response shape in PLAN §6, renamed `auth_id` → `payment_id` (see above), kept
  `from` without `omitempty` so every history entry has the same shape, and
  asked whether a failed void needs its own error — no: sentinels exist only
  where a caller branches on them with `errors.Is`, and a failed void is a
  business outcome (`needs_attention` + history detail), not a returned error.
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
- My file-by-file review of the AI draft, and what changed because of it:
  - Renamed every short identifier (`s`, `o`, `id`, `m`, `e`) to explicit
    names (`store`, `order`, `orderID`, `payment`, `engine`) across the whole
    codebase; the manager will read `store.Get`, `payment.Authorize`,
    `fulfillment.Fulfill`.
  - `List()` returned orders in random map order, which defeats its purpose
    (a sanity snapshot after running the demo). Added a sort on the creation
    entry's timestamp. Tried inlining `History[0].At`, then went back to the
    guarded `createdAt` helper because an order with no history would panic
    on the index and the store can't assume the manager always wrote one.
  - Asked whether the store test should verify Create actually persisted, not
    just returned nil. It should; added a Get-after-Create assertion.
  - Asked whether to split accessors into subpackages (interface + impl per
    accessor). Decided no at three interfaces / ~150 lines; recorded as a
    with-more-time item for when a second real implementation exists.
  - Asked why the payment mock keeps its own amount map instead of a field on
    Order. Because `Void` only receives a payment ID, like a real processor;
    the processor's bookkeeping stays on its side of the interface. Renamed
    the map `openHolds` and kept `delete` on void (this is a record of what is
    open; the order's history is the durable record).
  - Challenged whether the "not safe for concurrent use on its own" comments
    are enforceable. They aren't at runtime; enforcement is wiring (only the
    manager holds the accessors) plus `go test -race`, now the standard test
    command. With a real database the store becomes safe on its own; noted in
    with-more-time.
  - Asked whether the fulfillment mock should distinguish the two failing
    amounts. No — both fail fulfillment identically; what differs is the void
    that follows, which is the payment mock's decision. Added a comment saying
    so instead of splitting the case.
  - Added compile-time interface guards (`var _ Iface = (*Impl)(nil)`) for all
    three implementations, a pattern from my previous team.
  - Final pass trimming comments: shortened the mock processor doc, moved the
    void-deletion rationale inline, rewrapped two long lines.

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
- My review of the AI draft, and what changed because of it:
  - Renamed the `legal` map to `allowedTransitions` and reworded "legal" to
    "allowed" throughout; same meaning, more explicit.
  - Asked whether the engine should sit behind an interface with a guard like
    the accessors. No: interfaces in Go are for substitution, and the engine
    has one pure implementation with nothing to swap or mock. The accessors
    have interfaces because the mocks stand in for real services. Also
    confirmed `CanTransition` is not only used by `Transition` — the manager
    calls it before any side effect so an illegal action never reaches the
    processor or fulfillment.
  - My linter flagged the membership loop; replaced with `slices.Contains`.
  - Made the invalid-transition error read `from "x" to "y"` instead of
    `"x" -> "y"`.

### Slice 4 — manager (2026-09-16)

- AI wrote `OrderManager` from PLAN §3–§5 (one lock, `record` helper, zap
  transition log, decline-vs-other-error branch on authorize, the
  fulfillment → void → cancelled/needs_attention recovery) and six tests: the
  four required scenarios plus invalid-transition and unknown-order. Tests use
  explicit fakes with error fields and call counters, not the demo magic
  amounts, and assert on the order fetched back from the real store.
- Validation: `go vet` clean; `go test -race ./...` passes across all three
  tested packages. Dependencies added: zap (logging, my choice), google/uuid
  (order ids, my choice over a hand-rolled crypto/rand helper).
- My review of the AI draft, and what changed because of it:
  - Renamed the manager fields `storeAccessor` / `paymentAccessor` /
    `fulfillmentAccessor` so call sites say which layer they hit.
  - Caught a naming collision: `Create` took an `eventName` (the ticketed
    event) while history entries have an `Event` (the action outcome). Decided
    the order field wasn't needed by the spec at all and dropped it; create
    now takes only `amount_cents`.
  - Asked "log the error too?" at every return. Answer, now in the package
    comment: log once, at the layer that handles it (the endpoint, which picks
    the status code). The manager logs only transitions and the two failures
    it alone has context for. Raised the processor-error log from Warn to
    Error since it's an operational problem.
  - Asked where customer notification would go; added "with more time" markers
    at the complete and cancel points rather than building it.
  - Flagged the hand-rolled id helper as over-engineered; replaced with
    `uuid.NewString()`.
  - Test review: confirmed the scenarios run the real manager, engine, and
    store with only the two outside services faked. mockgen is my usual
    tooling; kept hand-written fakes here because the repo has exactly two
    fakes in one test file, and a generation pipeline (go:generate, Makefile,
    committed mocks package) would be more scaffolding than test code. Same
    power either way; noted in the README as what I'd switch to on a team.
    Made the history assertion check length explicitly so a failure says how
    many entries were expected.
  - Longest discussion: should `needs_attention` return a 5xx so clients and
    support are alerted? Walked through real reasons a void fails (processor
    outage/timeout, hold already captured, hold expired, our own bug) and
    separated two questions. The order's state must be `needs_attention`
    regardless of cause (the spec says so, and leaving it authorized would
    make it look completable). The status code stays 200 because the request
    was processed and the state persisted; a 5xx invites a retry that would
    409 and mis-counts a handled partial failure as a server failure. The
    customer is told by the client reading `state`, support by the Error log.
    README TL;DR agreed: with more time, a `void_pending` retry state for
    processor errors, monitoring on the error log, immediate customer and
    support notification, and an agreed distinct status code if the team
    wants one. Captured in PLAN §6 and §8.

### Slice 5 — endpoints, main, demo (2026-09-16)

- AI wrote the Gin handlers (five routes, a `respond` helper mapping sentinel
  errors to 404/409/500, a zap request-log middleware), two endpoint tests on
  the real wiring (happy path through all four endpoints; complete-before-
  authorize is a 409), `main.go`, and `demo.sh`.
- Validation: `go test -race ./...` passes in all four packages. I ran the
  server and `demo.sh` end to end and read every response: all four scenarios
  land in the right state with the right history, the cancelled and
  needs_attention details carry the error text, the payment id is retained on
  needs_attention, complete on a declined order returns a 409 with a readable
  message, and the server log shows one Error line for the needs_attention
  case with both errors as named fields.
- My review of the AI draft, and what changed because of it:
  - Asked whether the two middlewares were necessary or boilerplate. Recovery
    (panic → 500 instead of a dead server) is one word and stays; the request
    logger stays because it uses the same structured logger as everything
    else, which matters for the next point. Both now explained in a comment.
  - Asked whether authorize/complete should be PATCH or PUT. No: the client
    sends no fields and does not choose the resulting state; actions are POSTs
    to a sub-resource (Stripe's confirm/capture/cancel). Now in a comment.
  - Asked whether the `:id` path param should be format-checked. No: Gin only
    matches a non-empty segment, and any unknown id is a 404 from the store.
  - Asked whether every log line carries the order id, since in real life logs
    get searched by order id and payment id. They didn't: the endpoint's two
    lines had only the path, and the manager's transition line lacked the
    payment id. Fixed: every order-related log line now has `order_id`, and
    the manager's lines add `payment_id` once one exists. Verified by
    re-running the demo and reading the server log.
  - Replaced a `//nolint:errcheck` directive (for a linter this repo doesn't
    run) with the plain Go idiom `_ = logger.Sync()`.
  - Reworded the 400 message so it doesn't contain a `>` that Go's JSON
    encoder escapes to `\u003e` in the raw response.

### Slice 6 — README and final pass (2026-09-16)

- AI drafted `README.md` from PLAN to about one page in the prompt's four
  sections (what and why, how to run, tradeoffs, with more time), linking to
  the plan, this log, and the curl walkthrough for depth. I reviewed it as the
  live reviewer would read it, since it's the first file they open.
- Wrote `docs/CURL.md` from my own go-to curl commands (status code printed via
  `-w`, body through `jq`) with the expected status and state beside each
  step, and walked every scenario and every caller-error case by hand against
  the running server.
- Decided against changing the API response shape (full order on every
  endpoint): the spec asks for state + history and full-object responses
  mirror Stripe. Recorded the real-life version — a response type decoupled
  from the domain model, raw error text redacted from client-facing detail —
  as a with-more-time item.
- Final pass on this log: removed a handful of my own clarifying questions
  where nothing changed as a result, keeping every entry where I challenged
  the draft or made a call.
- Fresh-clone check: cloned the repo to a temp directory and ran
  `go test -race ./...` and `go build` following only the README.
