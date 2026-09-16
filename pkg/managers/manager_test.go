package managers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/joeyohie/gametime-order-state-machine/pkg/accessors"
	"github.com/joeyohie/gametime-order-state-machine/pkg/engines"
	"github.com/joeyohie/gametime-order-state-machine/pkg/models"
)

// The fakes below are configured explicitly per scenario and record their
// calls, so each test states exactly what fails and can assert what was (and
// was not) called. They do not use the demo magic amounts.

type fakePayment struct {
	authorizeErr    error
	voidErr         error
	authorizeCalls  int
	voidCalls       int
	voidedPaymentID string
}

func (payment *fakePayment) Authorize(_ context.Context, orderID string, _ int64) (string, error) {
	payment.authorizeCalls++
	if payment.authorizeErr != nil {
		return "", payment.authorizeErr
	}
	return "pay_" + orderID, nil
}

func (payment *fakePayment) Void(_ context.Context, paymentID string) error {
	payment.voidCalls++
	payment.voidedPaymentID = paymentID
	return payment.voidErr
}

type fakeFulfillment struct {
	err   error
	calls int
}

func (fulfillment *fakeFulfillment) Fulfill(_ context.Context, _ models.Order) error {
	fulfillment.calls++
	return fulfillment.err
}

// newTestManager wires a manager with the given fakes, a real in-memory store,
// and the real engine. Returns the store so tests can assert on what was
// PERSISTED, not just what was returned.
func newTestManager(payment *fakePayment, fulfillment *fakeFulfillment) (*OrderManager, *accessors.InMemoryOrderStore) {
	store := accessors.NewInMemoryOrderStore()
	manager := New(store, payment, fulfillment, engines.New(), zap.NewNop())
	return manager, store
}

// events flattens an order's history to its event names.
func events(order models.Order) []string {
	names := make([]string, 0, len(order.History))
	for _, entry := range order.History {
		names = append(names, entry.Event)
	}
	return names
}

func assertEvents(t *testing.T, order models.Order, want ...string) {
	t.Helper()
	got := events(order)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("history events: want %v, got %v", want, got)
	}
}

func TestHappyPath(t *testing.T) {
	ctx := context.Background()
	payment := &fakePayment{}
	fulfillment := &fakeFulfillment{}
	manager, store := newTestManager(payment, fulfillment)

	created, err := manager.Create(ctx, 12000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Authorize(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Complete(ctx, created.ID); err != nil {
		t.Fatal(err)
	}

	stored, err := store.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != models.StateComplete {
		t.Fatalf("state: want %s, got %s", models.StateComplete, stored.State)
	}
	assertEvents(t, stored, EventCreated, EventAuthorizeSucceeded, EventFulfillmentSucceeded)
	if stored.PaymentID != "pay_"+created.ID {
		t.Fatalf("payment id not stored: %q", stored.PaymentID)
	}
	if stored.History[0].From != "" || stored.History[0].To != models.StateInitialized {
		t.Fatalf("creation entry: %+v", stored.History[0])
	}
	if payment.voidCalls != 0 {
		t.Fatalf("void called %d times on the happy path", payment.voidCalls)
	}
}

func TestPaymentDeclined(t *testing.T) {
	ctx := context.Background()
	payment := &fakePayment{authorizeErr: fmt.Errorf("%w: card declined", models.ErrPaymentDeclined)}
	fulfillment := &fakeFulfillment{}
	manager, store := newTestManager(payment, fulfillment)

	created, _ := manager.Create(ctx, 12000)
	returned, err := manager.Authorize(ctx, created.ID)
	if err != nil {
		t.Fatalf("a decline is a business outcome, not an error: got %v", err)
	}
	if returned.State != models.StatePaymentDeclined {
		t.Fatalf("returned state: want %s, got %s", models.StatePaymentDeclined, returned.State)
	}

	stored, _ := store.Get(created.ID)
	if stored.State != models.StatePaymentDeclined {
		t.Fatalf("stored state: want %s, got %s", models.StatePaymentDeclined, stored.State)
	}
	assertEvents(t, stored, EventCreated, EventAuthorizeDeclined)
	if last := stored.History[len(stored.History)-1]; !strings.Contains(last.Detail, "card declined") {
		t.Fatalf("decline reason not in history: %+v", last)
	}
	if stored.PaymentID != "" {
		t.Fatalf("no hold exists on a decline, but payment id is %q", stored.PaymentID)
	}
	// No cleanup needed: nothing after authorize should have been touched.
	if payment.voidCalls != 0 || fulfillment.calls != 0 {
		t.Fatalf("void calls %d, fulfill calls %d; want 0 and 0", payment.voidCalls, fulfillment.calls)
	}
}

func TestCompletionFailsVoidSucceeds(t *testing.T) {
	ctx := context.Background()
	payment := &fakePayment{}
	fulfillment := &fakeFulfillment{err: errors.New("tickets no longer available")}
	manager, store := newTestManager(payment, fulfillment)

	created, _ := manager.Create(ctx, 12000)
	authorized, _ := manager.Authorize(ctx, created.ID)
	returned, err := manager.Complete(ctx, created.ID)
	if err != nil {
		t.Fatalf("a clean cancellation is a business outcome, not an error: got %v", err)
	}
	if returned.State != models.StateCancelled {
		t.Fatalf("returned state: want %s, got %s", models.StateCancelled, returned.State)
	}

	stored, _ := store.Get(created.ID)
	if stored.State != models.StateCancelled {
		t.Fatalf("stored state: want %s, got %s", models.StateCancelled, stored.State)
	}
	assertEvents(t, stored, EventCreated, EventAuthorizeSucceeded, EventFulfillmentFailedVoidSucceeded)
	last := stored.History[len(stored.History)-1]
	if !strings.Contains(last.Detail, "tickets no longer available") || !strings.Contains(last.Detail, "voided") {
		t.Fatalf("history must record the fulfillment error and the void: %+v", last)
	}
	if payment.voidCalls != 1 || payment.voidedPaymentID != authorized.PaymentID {
		t.Fatalf("void: calls %d, payment id %q; want 1 call with %q", payment.voidCalls, payment.voidedPaymentID, authorized.PaymentID)
	}
}

func TestCompletionFailsVoidFails(t *testing.T) {
	ctx := context.Background()
	payment := &fakePayment{voidErr: errors.New("processor unavailable")}
	fulfillment := &fakeFulfillment{err: errors.New("tickets no longer available")}
	manager, store := newTestManager(payment, fulfillment)

	created, _ := manager.Create(ctx, 12000)
	authorized, _ := manager.Authorize(ctx, created.ID)
	returned, err := manager.Complete(ctx, created.ID)
	if err != nil {
		t.Fatalf("needs_attention is a business outcome, not an error: got %v", err)
	}
	if returned.State != models.StateNeedsAttention {
		t.Fatalf("returned state: want %s, got %s", models.StateNeedsAttention, returned.State)
	}

	stored, _ := store.Get(created.ID)
	if stored.State != models.StateNeedsAttention {
		t.Fatalf("stored state: want %s, got %s", models.StateNeedsAttention, stored.State)
	}
	assertEvents(t, stored, EventCreated, EventAuthorizeSucceeded, EventFulfillmentFailedVoidFailed)
	// Don't silently swallow: BOTH errors must be in the record.
	last := stored.History[len(stored.History)-1]
	if !strings.Contains(last.Detail, "tickets no longer available") || !strings.Contains(last.Detail, "processor unavailable") {
		t.Fatalf("history must record both the fulfillment error and the void error: %+v", last)
	}
	// The hold still stands; keep its id so it can be voided manually.
	if stored.PaymentID != authorized.PaymentID {
		t.Fatalf("payment id must be kept for manual resolution: got %q", stored.PaymentID)
	}
	if payment.voidCalls != 1 {
		t.Fatalf("void calls: want 1, got %d", payment.voidCalls)
	}
}

// Invalid transitions are rejected before any side effect.
func TestCompleteBeforeAuthorizeIsRejected(t *testing.T) {
	ctx := context.Background()
	payment := &fakePayment{}
	fulfillment := &fakeFulfillment{}
	manager, store := newTestManager(payment, fulfillment)

	created, _ := manager.Create(ctx, 12000)
	_, err := manager.Complete(ctx, created.ID)
	if !errors.Is(err, models.ErrInvalidTransition) {
		t.Fatalf("want ErrInvalidTransition, got %v", err)
	}
	if fulfillment.calls != 0 || payment.voidCalls != 0 {
		t.Fatalf("side effects ran on a rejected action: fulfill %d, void %d", fulfillment.calls, payment.voidCalls)
	}
	stored, _ := store.Get(created.ID)
	if stored.State != models.StateInitialized || len(stored.History) != 1 {
		t.Fatalf("order changed on a rejected action: %+v", stored)
	}
}

func TestUnknownOrder(t *testing.T) {
	ctx := context.Background()
	manager, _ := newTestManager(&fakePayment{}, &fakeFulfillment{})

	if _, err := manager.Authorize(ctx, "ord_nope"); !errors.Is(err, models.ErrOrderNotFound) {
		t.Fatalf("Authorize: want ErrOrderNotFound, got %v", err)
	}
	if _, err := manager.Complete(ctx, "ord_nope"); !errors.Is(err, models.ErrOrderNotFound) {
		t.Fatalf("Complete: want ErrOrderNotFound, got %v", err)
	}
}
