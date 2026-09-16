package models

import "errors"

// Sentinel errors, checked with errors.Is. They are part of the contract
// between layers, so they live with the shared types rather than in the
// package that raises each one.
var (
	// ErrOrderNotFound is returned by the store for an unknown order ID.
	ErrOrderNotFound = errors.New("order not found")

	// ErrInvalidTransition is returned by the engine when an action is not
	// legal from the order's current state.
	ErrInvalidTransition = errors.New("invalid state transition")

	// ErrPaymentDeclined is returned by a PaymentProcessor when the processor
	// answered and said no. Any other error from Authorize means the processor
	// could not answer, which is not a decline.
	ErrPaymentDeclined = errors.New("payment declined")
)
