package accessors

import (
	"context"
	"fmt"

	"github.com/joeyohie/gametime-order-state-machine/pkg/models"
)

// TicketFulfillment is the business step between the payment hold and
// completion: secure the seller's tickets for this order. The prompt only
// requires that completion CAN fail after authorization; this is where that
// failure lives. It is a fulfillment problem (no tickets left, transfer
// failed), not a payment problem, so it is not overloaded onto the payment
// accessor.
type TicketFulfillment interface {
	Fulfill(ctx context.Context, order models.Order) error
}

// MockTicketFulfillment fails for the two demo amounts that exercise the
// post-authorization recovery paths.
type MockTicketFulfillment struct{}

// Compile-time interface guard.
var _ TicketFulfillment = (*MockTicketFulfillment)(nil)

func NewMockTicketFulfillment() *MockTicketFulfillment {
	return &MockTicketFulfillment{}
}

func (fulfillment *MockTicketFulfillment) Fulfill(_ context.Context, order models.Order) error {
	// Both amounts fail fulfillment the same way. What differs between them is
	// whether the void that follows succeeds, and that is the payment mock's
	// decision, not this accessor's.
	switch order.AmountCents {
	case FulfillmentFailsAmountCents, VoidFailsAmountCents:
		return fmt.Errorf("fulfill %s: tickets no longer available", order.ID)
	}
	return nil
}
