package pkg

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	stdsync "sync"
	"time"
)

// HealthState tracks the health status of the application
type HealthState struct {
	mu              stdsync.RWMutex
	initialized     bool
	configLoaded    bool
	workersStarted  bool
	lastHealthCheck time.Time
}

// HealthResponse represents the health check response
type HealthResponse struct {
	Status  string    `json:"status"`
	Message string    `json:"message,omitempty"`
	Time    time.Time `json:"timestamp"`
}

var healthState = &HealthState{
	initialized:    false,
	configLoaded:   false,
	workersStarted: false,
}

var healthServer *http.Server

// InitHealthCheck starts the health check server
func InitHealthCheck() error {
	portStr := os.Getenv("HEALTH_CHECK_PORT")
	if portStr == "" {
		portStr = "8081"
	}

	port, err := strconv.Atoi(portStr)
	if err != nil {
		slog.Warn("invalid HEALTH_CHECK_PORT, using default", "provided", portStr, "default", 8081)
		port = 8081
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health/live", handleLiveness)
	mux.HandleFunc("/health/ready", handleReadiness)
	mux.HandleFunc("/health", handleHealth)

	healthServer = &http.Server{
		Addr:         fmt.Sprintf(":%d", port),
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}

	// Start health server in a goroutine
	go func() {
		slog.Info("starting health check server", "port", port)
		if err := healthServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("health check server error", "error", err)
		}
	}()

	// Give server a moment to start
	time.Sleep(100 * time.Millisecond)

	return nil
}

// ShutdownHealthCheck gracefully shuts down the health check server
func ShutdownHealthCheck(ctx context.Context) error {
	if healthServer == nil {
		return nil
	}
	return healthServer.Shutdown(ctx)
}

// MarkConfigLoaded marks configuration as loaded
func MarkConfigLoaded() {
	healthState.mu.Lock()
	defer healthState.mu.Unlock()
	healthState.configLoaded = true
}

// MarkWorkersStarted marks workers as started
func MarkWorkersStarted() {
	healthState.mu.Lock()
	defer healthState.mu.Unlock()
	healthState.workersStarted = true
	healthState.initialized = true
}

// handleLiveness returns 200 if the application is running
// Kubernetes will restart the pod if this fails
func handleLiveness(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	response := HealthResponse{
		Status:  "alive",
		Message: "Application is running",
		Time:    time.Now(),
	}
	json.NewEncoder(w).Encode(response)
}

// handleReadiness returns 200 if the application is ready to accept work
// Kubernetes will remove the pod from the load balancer if this fails
func handleReadiness(w http.ResponseWriter, r *http.Request) {
	healthState.mu.RLock()
	defer healthState.mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")

	if !healthState.configLoaded || !healthState.workersStarted {
		w.WriteHeader(http.StatusServiceUnavailable)
		response := HealthResponse{
			Status:  "not_ready",
			Message: "Application is initializing",
			Time:    time.Now(),
		}
		json.NewEncoder(w).Encode(response)
		return
	}

	w.WriteHeader(http.StatusOK)
	response := HealthResponse{
		Status:  "ready",
		Message: "Application is ready to process work",
		Time:    time.Now(),
	}
	json.NewEncoder(w).Encode(response)
}

// handleHealth provides detailed health information
func handleHealth(w http.ResponseWriter, r *http.Request) {
	healthState.mu.RLock()
	defer healthState.mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")

	status := "healthy"
	statusCode := http.StatusOK

	if !healthState.initialized {
		status = "initializing"
		statusCode = http.StatusServiceUnavailable
	}

	w.WriteHeader(statusCode)
	response := map[string]interface{}{
		"status":            status,
		"timestamp":         time.Now(),
		"config_loaded":     healthState.configLoaded,
		"workers_started":   healthState.workersStarted,
		"initialized":       healthState.initialized,
		"last_health_check": healthState.lastHealthCheck,
	}
	json.NewEncoder(w).Encode(response)
}

// WaitForHealthPort waits for the health check server to be ready
func WaitForHealthPort() error {
	portStr := os.Getenv("HEALTH_CHECK_PORT")
	if portStr == "" {
		portStr = "8081"
	}

	maxRetries := 10
	for i := 0; i < maxRetries; i++ {
		conn, err := net.Dial("tcp", fmt.Sprintf("localhost:%s", portStr))
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}

	return fmt.Errorf("health check server not ready after %d attempts", maxRetries)
}
