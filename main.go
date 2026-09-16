// Order state machine service. `go run .` is the whole "how to run"; see
// README.md for the API and demo.sh for a curl walk-through.
package main

import (
	"os"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/joeyohie/gametime-order-state-machine/pkg/accessors"
	"github.com/joeyohie/gametime-order-state-machine/pkg/endpoints"
	"github.com/joeyohie/gametime-order-state-machine/pkg/engines"
	"github.com/joeyohie/gametime-order-state-machine/pkg/managers"
)

func main() {
	logger, err := zap.NewProduction()
	if err != nil {
		panic(err)
	}
	// Sync flushes buffered log lines at shutdown. Its error is ignored on
	// purpose: syncing a terminal fails with a meaningless ioctl error.
	defer func() { _ = logger.Sync() }()

	// Inject dependencies: accessors -> engine -> manager -> endpoints. The accessors are
	// handed only to the manager, which is what makes its lock sufficient.
	manager := managers.New(
		accessors.NewInMemoryOrderStore(),
		accessors.NewMockPaymentProcessor(),
		accessors.NewMockTicketFulfillment(),
		engines.New(),
		logger,
	)

	gin.SetMode(gin.ReleaseMode)
	router := endpoints.NewRouter(manager, logger)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	logger.Info("listening", zap.String("addr", ":"+port))
	if err := router.Run(":" + port); err != nil {
		logger.Fatal("server stopped", zap.Error(err))
	}
}
