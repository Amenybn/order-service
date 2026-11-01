package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

const (
	serviceName = "order-service"
	version     = "1.0.0"
)

type Config struct {
	Port       string
	CatalogURL string
	Timeout    time.Duration
}

func loadConfig() Config {
	cfg := Config{
		Port:       getenv("PORT", "8002"),
		CatalogURL: getenv("CATALOG_URL", "http://catalog-service:8001"),
		Timeout:    5 * time.Second,
	}
	if ms, err := strconv.Atoi(os.Getenv("CATALOG_TIMEOUT_MS")); err == nil && ms > 0 {
		cfg.Timeout = time.Duration(ms) * time.Millisecond
	}
	return cfg
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// healthcheck : "order-service healthcheck" renvoie 0 si /health répond (sonde Docker sans shell).
func healthcheck(port string) int {
	client := &http.Client{Timeout: 3 * time.Second}
	res, err := client.Get("http://127.0.0.1:" + port + "/health")
	if err != nil {
		return 1
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

func main() {
	cfg := loadConfig()
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck(cfg.Port))
	}

	log.SetFlags(log.LstdFlags | log.LUTC)
	srv := NewServer(NewStore(), NewCatalogClient(cfg.CatalogURL, cfg.Timeout), cfg)
	httpServer := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("%s v%s démarré sur le port %s (catalog-service : %s)", serviceName, version, cfg.Port, cfg.CatalogURL)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("erreur serveur : %v", err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	<-ctx.Done()
	log.Printf("signal d'arrêt reçu, fermeture propre...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdownCtx)
	log.Printf("%s arrêté", serviceName)
}
