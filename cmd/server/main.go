package main

import (
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/gofiber/fiber/v2/middleware/recover"

	"github.com/mt-sense/backend-service/internal/config"
	"github.com/mt-sense/backend-service/internal/database"
	"github.com/mt-sense/backend-service/internal/router"
)

func main() {
	cfg := config.Load()

	db, err := database.Connect(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	if err := database.Migrate(db); err != nil {
		log.Fatalf("database: %v", err)
	}
	if cfg.SeedOnBoot {
		if err := database.Seed(db); err != nil {
			log.Fatalf("database: %v", err)
		}
	}

	orgID, err := database.DefaultOrgID(db)
	if err != nil {
		log.Fatalf("database: %v", err)
	}

	app := fiber.New(fiber.Config{
		AppName:      "MT-Sense API",
		ErrorHandler: errorHandler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
	})

	app.Use(recover.New())
	app.Use(logger.New(logger.Config{Format: "${time} ${status} ${latency} ${method} ${path}\n"}))
	app.Use(cors.New(cors.Config{
		AllowOrigins:     cfg.CORSOrigins,
		AllowHeaders:     "Origin, Content-Type, Accept, Authorization",
		AllowMethods:     "GET,POST,PUT,PATCH,DELETE,OPTIONS",
		AllowCredentials: true,
	}))

	router.Register(app, db, cfg, orgID)

	go func() {
		if err := app.Listen(":" + cfg.Port); err != nil {
			log.Fatalf("server: %v", err)
		}
	}()
	log.Printf("server: listening on :%s", cfg.Port)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("server: shutting down…")
	if err := app.ShutdownWithTimeout(10 * time.Second); err != nil {
		log.Printf("server: forced shutdown: %v", err)
	}
	if sqlDB, err := db.DB(); err == nil {
		_ = sqlDB.Close()
	}
	log.Println("server: stopped")
}

// errorHandler keeps internal failures opaque to clients — a database error message can
// disclose schema details — while logging the real cause server-side.
func errorHandler(c *fiber.Ctx, err error) error {
	var fiberErr *fiber.Error
	if errors.As(err, &fiberErr) {
		return c.Status(fiberErr.Code).JSON(fiber.Map{"error": fiberErr.Message})
	}
	log.Printf("unhandled error on %s %s: %v", c.Method(), c.Path(), err)
	return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "internal server error"})
}
