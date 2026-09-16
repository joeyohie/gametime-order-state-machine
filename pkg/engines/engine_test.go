package engines

import (
	"errors"
	"testing"

	"github.com/joeyohie/gametime-order-state-machine/pkg/models"
)

func TestCanTransition(t *testing.T) {
	engine := New()
	cases := []struct {
		from, to models.State
		want     bool
	}{
		// every listed move
		{"", models.StateInitialized, true},
		{models.StateInitialized, models.StatePaymentAuthorized, true},
		{models.StateInitialized, models.StatePaymentDeclined, true},
		{models.StatePaymentAuthorized, models.StateComplete, true},
		{models.StatePaymentAuthorized, models.StateCancelled, true},
		{models.StatePaymentAuthorized, models.StateNeedsAttention, true},
		// skipping a step
		{models.StateInitialized, models.StateComplete, false},
		{"", models.StatePaymentAuthorized, false},
		// repeating a step
		{models.StatePaymentAuthorized, models.StatePaymentAuthorized, false},
		// a decline can only happen before a hold exists
		{models.StatePaymentAuthorized, models.StatePaymentDeclined, false},
		// terminal states have no way out
		{models.StateComplete, models.StateCancelled, false},
		{models.StatePaymentDeclined, models.StatePaymentAuthorized, false},
		{models.StateCancelled, models.StateComplete, false},
		{models.StateNeedsAttention, models.StateCancelled, false},
	}
	for _, testCase := range cases {
		if got := engine.CanTransition(testCase.from, testCase.to); got != testCase.want {
			t.Errorf("CanTransition(%q, %q) = %v, want %v", testCase.from, testCase.to, got, testCase.want)
		}
	}
}

func TestTransition_RecordsHistory(t *testing.T) {
	engine := New()
	order := models.Order{ID: "o1"}

	if err := engine.Transition(&order, models.StateInitialized, "created", ""); err != nil {
		t.Fatal(err)
	}
	if err := engine.Transition(&order, models.StatePaymentDeclined, "authorize_declined", "insufficient funds"); err != nil {
		t.Fatal(err)
	}

	if order.State != models.StatePaymentDeclined {
		t.Fatalf("state: got %s", order.State)
	}
	if len(order.History) != 2 {
		t.Fatalf("history: want 2 entries, got %d", len(order.History))
	}
	last := order.History[1]
	if last.From != models.StateInitialized || last.To != models.StatePaymentDeclined {
		t.Errorf("last entry: %+v", last)
	}
	if last.Detail != "insufficient funds" {
		t.Errorf("detail not recorded: %+v", last)
	}
	if last.At.IsZero() || last.At.Before(order.History[0].At) {
		t.Errorf("timestamps: %v then %v", order.History[0].At, last.At)
	}
}

func TestTransition_RejectsAndLeavesOrderUntouched(t *testing.T) {
	engine := New()
	order := models.Order{ID: "o1", State: models.StateInitialized}

	err := engine.Transition(&order, models.StateComplete, "complete", "")
	if !errors.Is(err, models.ErrInvalidTransition) {
		t.Fatalf("want ErrInvalidTransition, got %v", err)
	}
	if order.State != models.StateInitialized || len(order.History) != 0 {
		t.Fatalf("order changed on rejected transition: %+v", order)
	}
}
