package accessors

import (
	"context"
	"fmt"

	"github.com/joeyohie/gametime-order-state-machine/pkg/models"
)

// PaymentProcessor is the payment side of checkout, stubbed behind an
// interface as the prompt asks. Authorize places a hold and returns the
// processor's identifier for the payment; Void releases an uncaptured hold.
type PaymentProcessor interface {
	// Authorize returns models.ErrPaymentDeclined when the processor answered
	// and declined. Any other error means the processor could not answer.
	Authorize(ctx context.Context, orderID string, amountCents int64) (paymentID string, err error)
	Void(ctx context.Context, paymentID string) error
}

// Demo levers. The mock processor and fulfillment decide success or failure
// by order amount, so every scenario is reachable with curl and no test hooks
// in the API. Mnemonic: the bigger the amount, the further into the pipeline
// the failure happens. Unit tests configure their own fakes explicitly and do
// not depend on these.
const (
	DeclineAmountCents          int64 = 1000 // authorize is declined
	FulfillmentFailsAmountCents int64 = 2000 // fulfillment fails, void succeeds
	VoidFailsAmountCents        int64 = 3000 // fulfillment fails AND void fails
)

// MockPaymentProcessor is the processor the demo runs against. Void only
// receives a payment ID, so like a real processor it keeps its own record of
// open holds and uses it to fail the void for the VoidFails amount. Not safe
// for concurrent use on its own; the manager serializes all calls.
type MockPaymentProcessor struct {
	openHolds map[string]int64 // paymentID -> amount
}

// Compile-time interface guard.
var _ PaymentProcessor = (*MockPaymentProcessor)(nil)

func NewMockPaymentProcessor() *MockPaymentProcessor {
	return &MockPaymentProcessor{openHolds: map[string]int64{}}
}

// The context is unused because the mock does no I/O; a real implementation
// would pass it to the network call for timeouts and cancellation.
func (payment *MockPaymentProcessor) Authorize(_ context.Context, orderID string, amountCents int64) (string, error) {
	if amountCents == DeclineAmountCents {
		return "", fmt.Errorf("%w: insufficient funds", models.ErrPaymentDeclined)
	}
	paymentID := "pay_" + orderID
	payment.openHolds[paymentID] = amountCents
	return paymentID, nil
}

func (payment *MockPaymentProcessor) Void(_ context.Context, paymentID string) error {
	amount, ok := payment.openHolds[paymentID]
	if !ok {
		return fmt.Errorf("void %s: unknown payment", paymentID)
	}
	if amount == VoidFailsAmountCents {
		return fmt.Errorf("void %s: processor unavailable", paymentID)
	}
	delete(payment.openHolds, paymentID) // no longer open; the order's history is the durable record
	return nil
}
