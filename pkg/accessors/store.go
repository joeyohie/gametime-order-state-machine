package accessors

import (
	"fmt"
	"sort"
	"time"

	"github.com/joeyohie/gametime-order-state-machine/pkg/models"
)

// OrderStore persists orders. Every method copies the order, so no caller
// ever holds a reference into the store's own data. Not safe for concurrent
// use on its own: the manager is the only caller and serializes all access.
type OrderStore interface {
	Create(order models.Order) error
	Get(orderID string) (models.Order, error)
	Update(order models.Order) error
	List() []models.Order
}

// InMemoryOrderStore is a map. No database, no setup: `go run .` is the whole
// "how to run". Postgres plus a transactional outbox is the with-more-time
// answer.
type InMemoryOrderStore struct {
	orders map[string]models.Order
}

// Compile-time interface guard.
var _ OrderStore = (*InMemoryOrderStore)(nil)

func NewInMemoryOrderStore() *InMemoryOrderStore {
	return &InMemoryOrderStore{orders: map[string]models.Order{}}
}

func (store *InMemoryOrderStore) Create(order models.Order) error {
	if _, exists := store.orders[order.ID]; exists {
		return fmt.Errorf("create order %s: already exists", order.ID)
	}
	store.orders[order.ID] = clone(order)
	return nil
}

func (store *InMemoryOrderStore) Get(orderID string) (models.Order, error) {
	order, ok := store.orders[orderID]
	if !ok {
		return models.Order{}, models.ErrOrderNotFound
	}
	return clone(order), nil
}

func (store *InMemoryOrderStore) Update(order models.Order) error {
	if _, ok := store.orders[order.ID]; !ok {
		return models.ErrOrderNotFound
	}
	store.orders[order.ID] = clone(order)
	return nil
}

// List returns every order in creation order. Map iteration is random, so
// without the sort the list would shuffle between calls.
func (store *InMemoryOrderStore) List() []models.Order {
	list := make([]models.Order, 0, len(store.orders))

	for _, order := range store.orders {
		list = append(list, clone(order))
	}

	sort.Slice(list, func(i, j int) bool {
		return createdAt(list[i]).Before(createdAt(list[j]))
	})

	return list
}

// createdAt is the timestamp of the creation history entry. The manager always
// records creation before storing, but the store can't know that, so an order
// with no history sorts first instead of panicking on the index.
func createdAt(order models.Order) time.Time {
	if len(order.History) == 0 {
		return time.Time{}
	}
	return order.History[0].At
}

// clone deep-copies the one reference field, History, so a stored order and a
// returned order never share a backing array.
//
// A database returns a copy of the row for free; an in-memory map does not,
// so the store copies here.
func clone(order models.Order) models.Order {
	order.History = append([]models.HistoryEntry(nil), order.History...)
	return order
}
