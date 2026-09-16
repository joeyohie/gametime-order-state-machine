// Package managers holds the orchestration: OrderManager runs each action
// (create, authorize, complete) by calling the accessors, asking the engine
// to record the outcome, and persisting the result. It is the only caller of
// the accessors and holds the one lock that makes each action atomic.
//
// Logging policy: an error is logged once, at the layer that handles it. The
// endpoint decides the status code and logs there. The manager logs only what
// the endpoint cannot see: every transition, and the processor and
// needs_attention failures with their context.
package managers

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/joeyohie/gametime-order-state-machine/pkg/accessors"
	"github.com/joeyohie/gametime-order-state-machine/pkg/engines"
	"github.com/joeyohie/gametime-order-state-machine/pkg/models"
)

// Event names recorded on history entries. Each names an action outcome, not
// a state: one action can end in different states depending on the outcome.
const (
	EventCreated                        = "created"
	EventAuthorizeSucceeded             = "authorize_succeeded"
	EventAuthorizeDeclined              = "authorize_declined"
	EventFulfillmentSucceeded           = "fulfillment_succeeded"
	EventFulfillmentFailedVoidSucceeded = "fulfillment_failed_void_succeeded"
	EventFulfillmentFailedVoidFailed    = "fulfillment_failed_void_failed"
)

// OrderManager orchestrates order actions.
//
// Concurrency: one mutex, held for the whole of every method. The invariant
// is "check state, act, write" as one unit, and only the manager sees all
// three steps, so this is the only place a lock can cover them. A lock inside
// the store would be released between Get and Update, which is exactly where
// two concurrent completes would both read payment_authorized and both
// fulfill.
//
// It is one global lock, so the service handles one order action at a time
// even for unrelated orders. That costs throughput, which does not matter for
// a prototype, and buys correctness, which does. A real system locks per
// order in the database (a conditional update) so unrelated orders proceed in
// parallel. The mutex is a field, not a constructor argument, because it is
// internal state: nothing else may ever lock it.
type OrderManager struct {
	mutex               sync.Mutex
	storeAccessor       accessors.OrderStore
	paymentAccessor     accessors.PaymentProcessor
	fulfillmentAccessor accessors.TicketFulfillment
	engine              *engines.Engine
	logger              *zap.Logger
}

func New(
	storeAccessor accessors.OrderStore,
	paymentAccessor accessors.PaymentProcessor,
	fulfillmentAccessor accessors.TicketFulfillment,
	engine *engines.Engine,
	logger *zap.Logger,
) *OrderManager {
	return &OrderManager{
		storeAccessor:       storeAccessor,
		paymentAccessor:     paymentAccessor,
		fulfillmentAccessor: fulfillmentAccessor,
		engine:              engine,
		logger:              logger,
	}
}

// Create makes a new order in the initialized state. Creation is recorded as
// the first history entry so the creation timestamp lives with every other
// transition.
func (manager *OrderManager) Create(_ context.Context, amountCents int64) (models.Order, error) {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()

	order := models.Order{
		ID:          newOrderID(),
		AmountCents: amountCents,
	}

	if err := manager.engine.Transition(&order, models.StateInitialized, EventCreated, ""); err != nil {
		return models.Order{}, err
	}
	manager.logTransition(order)
	if err := manager.storeAccessor.Create(order); err != nil {
		return models.Order{}, err
	}
	return order, nil
}

// Authorize attempts to place the payment hold. A decline is a business
// outcome: the order moves to payment_declined and no error is returned. Any
// other processor error means nothing we know of happened, so the order stays
// initialized and the error is returned as-is.
func (manager *OrderManager) Authorize(ctx context.Context, orderID string) (models.Order, error) {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()

	order, err := manager.storeAccessor.Get(orderID)
	if err != nil {
		return models.Order{}, err
	}
	// Reject before any side effect: no second hold on an already-authorized
	// order, no hold on a finished one.
	if !manager.engine.CanTransition(order.State, models.StatePaymentAuthorized) {
		return models.Order{}, fmt.Errorf("%w: cannot authorize an order in state %q", models.ErrInvalidTransition, order.State)
	}

	paymentID, err := manager.paymentAccessor.Authorize(ctx, order.ID, order.AmountCents)
	switch {
	case errors.Is(err, models.ErrPaymentDeclined):
		// No hold exists, so there is nothing to clean up. The decline reason
		// is kept in the history entry, not swallowed.
		if err := manager.record(&order, models.StatePaymentDeclined, EventAuthorizeDeclined, err.Error()); err != nil {
			return models.Order{}, err
		}
		return order, nil
	case err != nil:
		// Not a decline: the processor could not answer (timeout, outage).
		// The mock never does this; a real processor would. Error level
		// because it is an operational problem someone should act on.
		manager.logger.Error("authorize: processor error",
			zap.String("order_id", order.ID), zap.String("payment_id", order.PaymentID), zap.Error(err))
		return models.Order{}, fmt.Errorf("authorize order %s: %w", order.ID, err)
	}

	order.PaymentID = paymentID
	if err := manager.record(&order, models.StatePaymentAuthorized, EventAuthorizeSucceeded, ""); err != nil {
		return models.Order{}, err
	}
	return order, nil
}

// Complete attempts fulfillment and runs the stage-dependent recovery:
//
//	fulfillment ok                  -> complete
//	fulfillment fails, void ok      -> cancelled       (history keeps the fulfillment error)
//	fulfillment fails, void fails   -> needs_attention (history keeps BOTH errors)
//
// All three are business outcomes and return the order with a nil error; the
// caller must read the state to tell "cleanly cancelled" from "charged but
// unfulfilled". Voiding is the system's recovery step, never a client action.
func (manager *OrderManager) Complete(ctx context.Context, orderID string) (models.Order, error) {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()

	order, err := manager.storeAccessor.Get(orderID)
	if err != nil {
		return models.Order{}, err
	}
	// Reject before any side effect: never fulfill an order that was not paid.
	if !manager.engine.CanTransition(order.State, models.StateComplete) {
		return models.Order{}, fmt.Errorf("%w: cannot complete an order in state %q", models.ErrInvalidTransition, order.State)
	}

	// Happy path: tickets secured.
	fulfillErr := manager.fulfillmentAccessor.Fulfill(ctx, order)
	if fulfillErr == nil {
		if err := manager.record(&order, models.StateComplete, EventFulfillmentSucceeded, ""); err != nil {
			return models.Order{}, err
		}
		// TODO: notify the customer that their tickets are ready.
		return order, nil
	}

	// Fulfillment cannot happen, and a hold exists, so failure here can never
	// be a clean decline: void the hold.
	voidErr := manager.paymentAccessor.Void(ctx, order.PaymentID)
	if voidErr == nil {
		// Void succeeded: the order is cleanly cancelled and the customer is
		// not charged.
		detail := fmt.Sprintf("fulfillment failed: %v; payment hold voided", fulfillErr)
		if err := manager.record(&order, models.StateCancelled, EventFulfillmentFailedVoidSucceeded, detail); err != nil {
			return models.Order{}, err
		}
		// TODO: notify the customer that the order was cancelled.
		return order, nil
	}

	// Partial failure: the customer may be holding a charge for tickets they
	// will not receive. Surface both errors and keep the payment ID so the
	// hold can be voided manually.
	detail := fmt.Sprintf("fulfillment failed: %v; void failed: %v", fulfillErr, voidErr)

	manager.logger.Error("complete: void failed after fulfillment failure, needs attention",
		zap.String("order_id", order.ID), zap.String("payment_id", order.PaymentID),
		zap.NamedError("fulfillment_error", fulfillErr), zap.NamedError("void_error", voidErr))
	// TODO: notify the customer AND Gametime Support that void failed and needs manual attention.

	if err := manager.record(&order, models.StateNeedsAttention, EventFulfillmentFailedVoidFailed, detail); err != nil {
		return models.Order{}, err
	}
	return order, nil
}

// Get returns the order with its full history.
func (manager *OrderManager) Get(_ context.Context, orderID string) (models.Order, error) {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	return manager.storeAccessor.Get(orderID)
}

// List returns every order in creation order.
func (manager *OrderManager) List(_ context.Context) []models.Order {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	return manager.storeAccessor.List()
}

// record applies a transition through the engine, logs it, and persists the
// order. Every state change after creation goes through here.
func (manager *OrderManager) record(order *models.Order, to models.State, event, detail string) error {
	if err := manager.engine.Transition(order, to, event, detail); err != nil {
		return err
	}
	manager.logTransition(*order)
	return manager.storeAccessor.Update(*order)
}

// logTransition emits one structured line for the most recent history entry.
// This is the play-by-play for manual verification; the order's history is
// the durable record.
func (manager *OrderManager) logTransition(order models.Order) {
	entry := order.History[len(order.History)-1]
	manager.logger.Info("order transition",
		zap.String("order_id", order.ID),
		zap.String("payment_id", order.PaymentID),
		zap.String("from", string(entry.From)),
		zap.String("to", string(entry.To)),
		zap.String("event", entry.Event),
		zap.String("detail", entry.Detail),
	)
}

// newOrderID returns a random id like ord_1b4e28ba-2fa1-11d2-883f-0016d3cca427.
// Random rather than sequential so ids are not guessable.
func newOrderID() string {
	return "ord_" + uuid.NewString()
}
