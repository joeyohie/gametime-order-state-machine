// Package endpoints holds the Gin handlers: JSON in, JSON out, status codes.
// No business logic lives here; every action is one call into the manager.
package endpoints

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/joeyohie/gametime-order-state-machine/pkg/managers"
	"github.com/joeyohie/gametime-order-state-machine/pkg/models"
)

// Handler adapts HTTP to the manager.
type Handler struct {
	manager *managers.OrderManager
	logger  *zap.Logger
}

// NewRouter builds the Gin router with all routes registered.
//
// Paths follow REST convention: plural collection, member by id, member
// actions as sub-resources. Clients never set states; they request actions
// and the manager decides the resulting state.
func NewRouter(manager *managers.OrderManager, logger *zap.Logger) *gin.Engine {
	handler := &Handler{manager: manager, logger: logger}

	// Two middlewares: Recovery turns a handler panic into a 500 instead of
	// killing the server; requestLogger writes one structured line per request
	// with the same zap logger as everything else (Gin's built-in logger is
	// plain text, which would mix formats).
	router := gin.New()
	router.Use(gin.Recovery(), requestLogger(logger))

	// Actions are POSTs to a sub-resource, not PATCH/PUT: the client sends no
	// fields and does not choose the resulting state; it asks the server to
	// attempt an action. (Stripe: POST /payment_intents/:id/confirm.)
	router.POST("/orders", handler.createOrder)
	router.POST("/orders/:id/authorize", handler.authorizeOrder)
	router.POST("/orders/:id/complete", handler.completeOrder)
	router.GET("/orders/:id", handler.getOrder)
	router.GET("/orders", handler.listOrders)
	return router
}

// Money is a whole number of cents (1250 = $12.50), never a float. The
// binding tags drive Gin's validator: required = present and non-zero,
// gt=0 = rejects negatives. They know nothing about cents; that is the
// field's contract.
type createOrderRequest struct {
	AmountCents int64 `json:"amount_cents" binding:"required,gt=0"`
}

func (handler *Handler) createOrder(ctx *gin.Context) {
	var request createOrderRequest
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "amount_cents is required and must be greater than 0"})
		return
	}
	order, err := handler.manager.Create(ctx.Request.Context(), request.AmountCents)
	handler.respond(ctx, http.StatusCreated, order, err)
}

// The :id param is not format-checked: Gin only matches the route when it is
// non-empty, and an unknown id (malformed or not) is a 404 from the store.
func (handler *Handler) authorizeOrder(ctx *gin.Context) {
	order, err := handler.manager.Authorize(ctx.Request.Context(), ctx.Param("id"))
	handler.respond(ctx, http.StatusOK, order, err)
}

func (handler *Handler) completeOrder(ctx *gin.Context) {
	order, err := handler.manager.Complete(ctx.Request.Context(), ctx.Param("id"))
	handler.respond(ctx, http.StatusOK, order, err)
}

func (handler *Handler) getOrder(ctx *gin.Context) {
	order, err := handler.manager.Get(ctx.Request.Context(), ctx.Param("id"))
	handler.respond(ctx, http.StatusOK, order, err)
}

func (handler *Handler) listOrders(ctx *gin.Context) {
	ctx.JSON(http.StatusOK, gin.H{"orders": handler.manager.List(ctx.Request.Context())})
}

// respond writes the order on success or maps the error to a status code.
// Business outcomes (declined, cancelled, needs_attention) arrive here with a
// nil error and the outcome in order.State; the client reads the state.
func (handler *Handler) respond(ctx *gin.Context, successStatus int, order models.Order, err error) {
	if err == nil {
		ctx.JSON(successStatus, order)
		return
	}
	status := statusFor(err)
	if status == http.StatusInternalServerError {
		// Logged once, here, where the status is decided (see managers doc).
		// Every log line about an order carries order_id so logs can be
		// searched by it; the manager adds payment_id once one exists.
		handler.logger.Error("request failed",
			zap.String("order_id", ctx.Param("id")), zap.String("path", ctx.FullPath()), zap.Error(err))
	}
	ctx.JSON(status, gin.H{"error": err.Error()})
}

// statusFor maps the sentinel errors to HTTP status codes. 4xx means the
// caller did something wrong; anything unrecognized is a 500.
func statusFor(err error) int {
	switch {
	case errors.Is(err, models.ErrOrderNotFound):
		return http.StatusNotFound
	case errors.Is(err, models.ErrInvalidTransition):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

// requestLogger emits one structured line per request.
func requestLogger(logger *zap.Logger) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		start := time.Now()
		ctx.Next()
		logger.Info("request",
			zap.String("method", ctx.Request.Method),
			zap.String("path", ctx.Request.URL.Path),
			zap.String("order_id", ctx.Param("id")),
			zap.Int("status", ctx.Writer.Status()),
			zap.Duration("latency", time.Since(start)),
		)
	}
}
