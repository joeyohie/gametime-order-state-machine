// Package engines holds the order state machine: the table of allowed
// transitions and the one operation that applies a transition and records it.
// It is pure. No Gin, no accessors, no I/O, no clock injection: the manager
// decides WHICH transition to apply; the engine decides WHETHER it is allowed.
package engines

import (
	"fmt"
	"slices"
	"time"

	"github.com/joeyohie/gametime-order-state-machine/pkg/models"
)

// allowedTransitions is the rulebook. Any (from, to) pair not listed is
// rejected. Creation ("" -> initialized) is included so it is recorded like
// every other transition. The four terminal states have no outgoing arrows.
var allowedTransitions = map[models.State][]models.State{
	"":                            {models.StateInitialized},
	models.StateInitialized:       {models.StatePaymentAuthorized, models.StatePaymentDeclined},
	models.StatePaymentAuthorized: {models.StateComplete, models.StateCancelled, models.StateNeedsAttention},
}

// Engine validates and applies transitions.
type Engine struct{}

func New() *Engine { return &Engine{} }

// CanTransition reports whether moving from -> to is allowed. The manager
// calls this before any side effect so an illegal action is rejected without
// touching the processor or fulfillment.
func (engine *Engine) CanTransition(from, to models.State) bool {
	return slices.Contains(allowedTransitions[from], to)
}

// Transition moves the order to the given state and appends a timestamped
// history entry. Event names the action outcome; detail carries error text on
// failure paths so it is never swallowed. Returns models.ErrInvalidTransition
// (wrapped with the offending states) and leaves the order untouched when the
// move is not allowed.
func (engine *Engine) Transition(order *models.Order, to models.State, event, detail string) error {
	from := order.State // saved before the overwrite below; the history entry needs it
	if !engine.CanTransition(from, to) {
		return fmt.Errorf("%w: from %q to %q", models.ErrInvalidTransition, from, to)
	}
	order.State = to // the transition itself; everything else is validation and bookkeeping
	order.History = append(order.History, models.HistoryEntry{
		From:   from,
		To:     to,
		Event:  event,
		At:     time.Now().UTC(),
		Detail: detail,
	})
	return nil
}
