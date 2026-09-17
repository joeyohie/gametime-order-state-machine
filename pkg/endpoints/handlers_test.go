package endpoints

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/joeyohie/gametime-order-state-machine/pkg/accessors"
	"github.com/joeyohie/gametime-order-state-machine/pkg/engines"
	"github.com/joeyohie/gametime-order-state-machine/pkg/managers"
	"github.com/joeyohie/gametime-order-state-machine/pkg/models"
)

// The endpoint tests run the real wiring (demo mocks, in-memory store), so
// they also exercise the magic-amount levers the demo script relies on.
func newTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	manager := managers.New(
		accessors.NewInMemoryOrderStore(),
		accessors.NewMockPaymentProcessor(),
		accessors.NewMockTicketFulfillment(),
		engines.New(),
		zap.NewNop(),
	)
	return NewRouter(manager, zap.NewNop())
}

// do sends one request and decodes the JSON body into out (if non-nil).
// No server is started: Gin's router is an http.Handler, so each test builds a
// request in memory and calls router.ServeHTTP with a recorder that captures
// the status and body. Same code path as a real request, minus the network.
func do(t *testing.T, router *gin.Engine, method, path string, body any, out any) int {
	t.Helper()
	var payload bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&payload).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, path, &payload)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if out != nil {
		if err := json.Unmarshal(recorder.Body.Bytes(), out); err != nil {
			t.Fatalf("decode %s %s: %v: %s", method, path, err, recorder.Body.String())
		}
	}
	return recorder.Code
}

func TestHappyPathEndToEnd(t *testing.T) {
	router := newTestRouter()

	var order models.Order
	if status := do(t, router, http.MethodPost, "/orders", gin.H{"amount_cents": 5000}, &order); status != http.StatusCreated {
		t.Fatalf("create: want 201, got %d", status)
	}
	if order.State != models.StateInitialized || order.ID == "" {
		t.Fatalf("create: %+v", order)
	}

	if status := do(t, router, http.MethodPost, "/orders/"+order.ID+"/authorize", nil, &order); status != http.StatusOK {
		t.Fatalf("authorize: want 200, got %d", status)
	}
	if order.State != models.StatePaymentAuthorized || order.PaymentID == "" {
		t.Fatalf("authorize: %+v", order)
	}

	if status := do(t, router, http.MethodPost, "/orders/"+order.ID+"/complete", nil, &order); status != http.StatusOK {
		t.Fatalf("complete: want 200, got %d", status)
	}
	if order.State != models.StateComplete {
		t.Fatalf("complete: %+v", order)
	}

	if status := do(t, router, http.MethodGet, "/orders/"+order.ID, nil, &order); status != http.StatusOK {
		t.Fatalf("get: want 200, got %d", status)
	}
	if len(order.History) != 3 {
		t.Fatalf("history: want 3 entries, got %d: %+v", len(order.History), order.History)
	}
}

func TestCompleteBeforeAuthorizeIs409(t *testing.T) {
	router := newTestRouter()

	var order models.Order
	do(t, router, http.MethodPost, "/orders", gin.H{"amount_cents": 5000}, &order)

	var body map[string]string
	if status := do(t, router, http.MethodPost, "/orders/"+order.ID+"/complete", nil, &body); status != http.StatusConflict {
		t.Fatalf("want 409, got %d: %v", status, body)
	}
	if body["error"] == "" {
		t.Fatalf("409 must explain itself: %v", body)
	}
}
