// Package models holds the domain types shared by every layer: the order, its
// state, its history entries, and the sentinel errors (errors.go). No logic
// lives here.
package models

import "time"

// State is where an order is right now. Exactly one at a time.
type State string

const (
	StateInitialized       State = "initialized"
	StatePaymentAuthorized State = "payment_authorized"
	StateComplete          State = "complete"
	StatePaymentDeclined   State = "payment_declined"
	StateCancelled         State = "cancelled"
	StateNeedsAttention    State = "needs_attention"
)

// HistoryEntry records one transition. From is empty for the creation entry.
// Detail carries error text on failure transitions so nothing is swallowed.
type HistoryEntry struct {
	From   State     `json:"from"`
	To     State     `json:"to"`
	Event  string    `json:"event"`
	At     time.Time `json:"at"`
	Detail string    `json:"detail,omitempty"`
}

// Order is the marketplace-side order. PaymentID is the payment processor's
// identifier for the payment object (Stripe: the PaymentIntent id). It is set
// once authorization succeeds and kept on a needs_attention order so the hold
// can be voided manually.
type Order struct {
	ID          string         `json:"id"`
	AmountCents int64          `json:"amount_cents"`
	State       State          `json:"state"`
	PaymentID   string         `json:"payment_id,omitempty"`
	History     []HistoryEntry `json:"history"`
}
