package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"df-build-server/internal/handler"
	"df-build-server/internal/middleware"
	"df-build-server/internal/repository"
	"df-build-server/internal/scheduler"
	"df-build-server/internal/web"
	"df-build-server/pkg/config"
	"df-build-server/pkg/logger"

	"github.com/gin-gonic/gin"
)

func main() {
	// Load configuration - support CLI flag, env var, or default
	configPath := "config/config.yaml"
	if envPath := os.Getenv("CONFIG_PATH"); envPath != "" {
		configPath = envPath
	}
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		for _, p := range []string{"./config.yaml", "../config/config.yaml"} {
			if _, err := os.Stat(p); err == nil {
				configPath = p
				break
			}
		}
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Printf("Failed to load config: %v\n", err)
		os.Exit(1)
	}

	// Initialize logger
	logger.Init(cfg.Log.Level, cfg.Log.Output, cfg.Log.FilePath)
	logger.Log.Info("Starting DF Build Server...")

	// Initialize database
	if err := repository.InitDB(&cfg.Database); err != nil {
		logger.Log.Fatalf("Failed to initialize database: %v", err)
	}

	// Run migrations and seed data
	if err := repository.AutoMigrate(); err != nil {
		logger.Log.Fatalf("Failed to run migrations: %v", err)
	}
	repository.RunMigrations()
	repository.SeedCoreData()

	// Setup Gin
	if cfg.Server.Mode == "release" {
		gin.SetMode(gin.ReleaseMode)
	}

	// Initialize scheduler
	scheduler.Init()
	scheduler.StartCronJobs()

	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(middleware.CORS())
	r.Use(middleware.ErrorHandler())
	r.Use(requestLogger())

	// Health check with DB probe
	r.GET("/health", func(c *gin.Context) {
		health := gin.H{"status": "ok", "service": "df-build-server"}

		sqlDB, err := repository.DB.DB()
		if err != nil || sqlDB.Ping() != nil {
			health["status"] = "degraded"
			health["database"] = "unhealthy"
			c.JSON(http.StatusServiceUnavailable, health)
			return
		}
		health["database"] = "healthy"
		c.JSON(http.StatusOK, health)
	})

	// API routes
	api := r.Group("/api")

	handler.NewAuthHandler().RegisterRoutes(api)
	handler.NewApplicationHandler().RegisterRoutes(api)
	handler.NewTemplateHandler().RegisterRoutes(api)
	handler.NewExecutorHandler().RegisterRoutes(api)
	handler.NewBuildConfigHandler().RegisterRoutes(api)
	handler.NewServerHandler().RegisterRoutes(api)
	handler.NewNotificationHandler().RegisterRoutes(api)
	handler.NewSettingsHandler().RegisterRoutes(api)
	handler.NewTaskHandler().RegisterRoutes(api)
	handler.NewPipelineHandler().RegisterRoutes(api)
	handler.NewSSEHandler().RegisterRoutes(api)
	handler.NewRemoteHandler().RegisterRoutes(api)
	handler.NewDashboardHandler().RegisterRoutes(api)
	handler.NewArtifactHandler().RegisterRoutes(api)
	handler.NewConfigItemHandler().RegisterRoutes(api)
	handler.NewServerMgmtHandler().RegisterRoutes(api)
	handler.NewWebSSHHandler().RegisterRoutes(api)
	handler.NewWebSFTPHandler().RegisterRoutes(api)
	handler.NewServerMonitorHandler().RegisterRoutes(api)
	handler.NewKubernetesHandler().RegisterRoutes(api)
	handler.NewK8sWatchHandler().RegisterRoutes(api)
	handler.NewBatchDeployHandler().RegisterRoutes(api)
	handler.NewNotificationMsgHandler().RegisterRoutes(api)
	handler.NewDeployHandler().RegisterRoutes(api)
	handler.NewPostgreSQLHandler().RegisterRoutes(api)

	// SPA static file serving (embedded frontend)
	web.RegisterSPA(r)

	// Start server with graceful shutdown
	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	srv := &http.Server{
		Addr:    addr,
		Handler: r,
	}

	go func() {
		logger.Log.Infof("Server listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Log.Fatalf("Failed to start server: %v", err)
		}
	}()

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	logger.Log.Info("Shutting down server...")

	// Graceful shutdown with 30 second timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		logger.Log.Errorf("Server forced to shutdown: %v", err)
	}
	logger.Log.Info("Server exited gracefully")
}

// requestLogger logs each HTTP request with path, status, and latency
func requestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		method := c.Request.Method

		c.Next()

		latency := time.Since(start)
		status := c.Writer.Status()

		// Skip noisy endpoints
		if path == "/health" {
			return
		}

		if status >= 500 {
			logger.Log.Errorf("[%d] %s %s %v", status, method, path, latency)
		} else if status >= 400 {
			logger.Log.Warnf("[%d] %s %s %v", status, method, path, latency)
		} else {
			logger.Log.Debugf("[%d] %s %s %v", status, method, path, latency)
		}
	}
}
