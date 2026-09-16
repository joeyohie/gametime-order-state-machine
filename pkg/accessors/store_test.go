package accessors

import (
	"errors"
	"testing"

	"github.com/joeyohie/gametime-order-state-machine/pkg/models"
)

func TestInMemoryOrderStore_CreateGetUpdate(t *testing.T) {
	store := NewInMemoryOrderStore()

	if _, err := store.Get("missing"); !errors.Is(err, models.ErrOrderNotFound) {
		t.Fatalf("Get missing: want ErrOrderNotFound, got %v", err)
	}
	if err := store.Update(models.Order{ID: "missing"}); !errors.Is(err, models.ErrOrderNotFound) {
		t.Fatalf("Update missing: want ErrOrderNotFound, got %v", err)
	}

	order := models.Order{ID: "o1", State: models.StateInitialized}
	if err := store.Create(order); err != nil {
		t.Fatal(err)
	}
	// Create must actually persist, not just return nil.
	created, err := store.Get("o1")
	if err != nil {
		t.Fatal(err)
	}
	if created.State != models.StateInitialized {
		t.Fatalf("state after Create: want %s, got %s", models.StateInitialized, created.State)
	}
	if err := store.Create(order); err == nil {
		t.Fatal("Create duplicate: want error")
	}

	order.State = models.StatePaymentAuthorized
	if err := store.Update(order); err != nil {
		t.Fatal(err)
	}
	updated, err := store.Get("o1")
	if err != nil {
		t.Fatal(err)
	}
	if updated.State != models.StatePaymentAuthorized {
		t.Fatalf("state after Update: want %s, got %s", models.StatePaymentAuthorized, updated.State)
	}
}

// The store hands out copies: mutating what Get returned must not change what
// the store holds.
func TestInMemoryOrderStore_GetReturnsCopy(t *testing.T) {
	store := NewInMemoryOrderStore()
	if err := store.Create(models.Order{
		ID:      "o1",
		History: []models.HistoryEntry{{To: models.StateInitialized}},
	}); err != nil {
		t.Fatal(err)
	}

	fetched, _ := store.Get("o1")
	// "Vandalize" the order itself.
	fetched.History[0].To = models.StateComplete
	fetched.History = append(fetched.History, models.HistoryEntry{To: models.StateCancelled})

	refetched, _ := store.Get("o1")
	// Check that the original order was not changed.
	if len(refetched.History) != 1 || refetched.History[0].To != models.StateInitialized {
		t.Fatalf("store was mutated through a returned copy: %+v", refetched.History)
	}
}
