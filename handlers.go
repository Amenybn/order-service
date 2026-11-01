package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/mail"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type Server struct {
	store     *Store
	catalog   *CatalogClient
	cfg       Config
	startedAt time.Time
	hostname  string
}

func NewServer(store *Store, catalog *CatalogClient, cfg Config) *Server {
	host, _ := os.Hostname()
	return &Server{store: store, catalog: catalog, cfg: cfg, startedAt: time.Now().UTC(), hostname: host}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "UP"})
	})
	mux.HandleFunc("GET /info", s.info)
	mux.HandleFunc("GET /orders", s.listOrders)
	mux.HandleFunc("POST /orders", s.createOrder)
	mux.HandleFunc("GET /orders/{id}", s.getOrder)
	mux.HandleFunc("POST /orders/{id}/cancel", s.cancelOrder)
	mux.HandleFunc("POST /orders/{id}/notification", s.notifyOrder)
	mux.HandleFunc("POST /workers/heartbeat", s.workerHeartbeat)
	mux.HandleFunc("GET /workers", s.listWorkers)
	return logRequests(mux)
}

// ---------- Supervision ----------

func (s *Server) info(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"service":       serviceName,
		"version":       version,
		"language":      "Go " + strings.TrimPrefix(runtime.Version(), "go"),
		"hostname":      s.hostname,
		"startedAt":     s.startedAt,
		"uptimeSeconds": int(time.Since(s.startedAt).Seconds()),
		"catalogUrl":    s.cfg.CatalogURL,
		"stats":         s.store.Stats(),
		"workers":       len(s.store.Workers()),
	})
}

// ---------- Commandes ----------

func (s *Server) listOrders(w http.ResponseWriter, r *http.Request) {
	var notified *bool
	if v := r.URL.Query().Get("notified"); v != "" {
		b := v == "true"
		notified = &b
	}
	writeJSON(w, http.StatusOK, s.store.List(notified))
}

func (s *Server) getOrder(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	o, err := s.store.Get(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, o)
}

type createOrderRequest struct {
	CustomerName  string `json:"customerName"`
	CustomerEmail string `json:"customerEmail"`
	Items         []struct {
		ProductID int `json:"productId"`
		Quantity  int `json:"quantity"`
	} `json:"items"`
}

type reservation struct{ productID, quantity int }

func (s *Server) createOrder(w http.ResponseWriter, r *http.Request) {
	var req createOrderRequest
	if !decode(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.CustomerName)
	email := strings.TrimSpace(req.CustomerEmail)
	if name == "" {
		writeError(w, http.StatusBadRequest, "Le nom du client est obligatoire")
		return
	}
	if _, err := mail.ParseAddress(email); err != nil || !strings.Contains(email, ".") {
		writeError(w, http.StatusBadRequest, "Email invalide")
		return
	}
	if len(req.Items) == 0 || len(req.Items) > 20 {
		writeError(w, http.StatusBadRequest, "La commande doit contenir entre 1 et 20 produits")
		return
	}

	// Regroupe les lignes d'un même produit
	quantities := map[int]int{}
	order := []int{}
	for _, it := range req.Items {
		if it.ProductID <= 0 || it.Quantity <= 0 || it.Quantity > 100 {
			writeError(w, http.StatusBadRequest, "Produit ou quantité invalide (quantité entre 1 et 100)")
			return
		}
		if _, seen := quantities[it.ProductID]; !seen {
			order = append(order, it.ProductID)
		}
		quantities[it.ProductID] += it.Quantity
	}

	requestID := r.Header.Get("X-Request-Id")
	var reserved []reservation
	var items []OrderItem
	for _, pid := range order {
		qty := quantities[pid]
		p, err := s.catalog.Reserve(r.Context(), requestID, pid, qty)
		if err != nil {
			s.compensate(requestID, reserved)
			var ce *CatalogError
			if errors.As(err, &ce) {
				writeError(w, ce.Status, ce.Message)
			} else {
				writeError(w, http.StatusBadGateway, err.Error())
			}
			return
		}
		reserved = append(reserved, reservation{pid, qty})
		items = append(items, OrderItem{
			ProductID: p.ID, Name: p.Name, Emoji: p.Emoji, UnitPrice: p.Price,
			Quantity: qty, Subtotal: round2(p.Price * float64(qty)),
		})
	}

	o := s.store.Add(name, email, items)
	log.Printf("commande %s créée pour %s : %d produit(s), total %.2f € (request-id=%s)", o.Number, email, len(items), o.Total, requestID)
	writeJSON(w, http.StatusCreated, o)
}

// compensate libère le stock déjà réservé quand une commande échoue en cours de route.
func (s *Server) compensate(requestID string, reserved []reservation) {
	if len(reserved) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, r := range reserved {
		if err := s.catalog.Release(ctx, requestID, r.productID, r.quantity); err != nil {
			log.Printf("ATTENTION compensation impossible pour le produit %d (x%d) : %v", r.productID, r.quantity, err)
		}
	}
	log.Printf("commande échouée : %d réservation(s) annulée(s) (request-id=%s)", len(reserved), requestID)
}

func (s *Server) cancelOrder(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	o, err := s.store.Cancel(id)
	switch {
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
		return
	case errors.Is(err, ErrAlreadyCancelled):
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	requestID := r.Header.Get("X-Request-Id")
	for _, it := range o.Items {
		if err := s.catalog.Release(r.Context(), requestID, it.ProductID, it.Quantity); err != nil {
			log.Printf("ATTENTION stock non remis pour %s (x%d) : %v", it.Name, it.Quantity, err)
		}
	}
	log.Printf("commande %s annulée, stock remis au catalogue", o.Number)
	writeJSON(w, http.StatusOK, o)
}

// ---------- Appels du worker ----------

func (s *Server) notifyOrder(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var n Notification
	if !decode(w, r, &n) {
		return
	}
	if strings.TrimSpace(n.Message) == "" {
		writeError(w, http.StatusBadRequest, "message obligatoire")
		return
	}
	o, err := s.store.SetNotification(id, n)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, o)
}

func (s *Server) workerHeartbeat(w http.ResponseWriter, r *http.Request) {
	var h WorkerHeartbeat
	if !decode(w, r, &h) {
		return
	}
	if strings.TrimSpace(h.Name) == "" {
		writeError(w, http.StatusBadRequest, "name obligatoire")
		return
	}
	s.store.Heartbeat(h)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listWorkers(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.store.Workers())
}

// ---------- Utilitaires HTTP ----------

func pathID(w http.ResponseWriter, r *http.Request) (int, bool) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "identifiant invalide")
		return 0, false
	}
	return id, true
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "JSON invalide")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": http.StatusText(status), "message": message})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		if r.URL.Path != "/health" {
			log.Printf("%s %s -> %d (%d ms)", r.Method, r.URL.RequestURI(), rec.status, time.Since(start).Milliseconds())
		}
	})
}
