package main

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"
)

const (
	StatusConfirmed = "CONFIRMED"
	StatusCancelled = "CANCELLED"
)

var (
	ErrNotFound         = errors.New("commande introuvable")
	ErrAlreadyCancelled = errors.New("commande déjà annulée")
)

type OrderItem struct {
	ProductID int     `json:"productId"`
	Name      string  `json:"name"`
	Emoji     string  `json:"emoji"`
	UnitPrice float64 `json:"unitPrice"`
	Quantity  int     `json:"quantity"`
	Subtotal  float64 `json:"subtotal"`
}

type Notification struct {
	Channel string    `json:"channel"`
	Message string    `json:"message"`
	Worker  string    `json:"worker"`
	SentAt  time.Time `json:"sentAt"`
}

type Order struct {
	ID            int           `json:"id"`
	Number        string        `json:"number"`
	CustomerName  string        `json:"customerName"`
	CustomerEmail string        `json:"customerEmail"`
	Items         []OrderItem   `json:"items"`
	Total         float64       `json:"total"`
	Status        string        `json:"status"`
	CreatedAt     time.Time     `json:"createdAt"`
	CancelledAt   *time.Time    `json:"cancelledAt"`
	Notification  *Notification `json:"notification"`
}

type WorkerHeartbeat struct {
	Name          string    `json:"name"`
	Hostname      string    `json:"hostname"`
	Processed     int       `json:"processed"`
	LowStockCount int       `json:"lowStockCount"`
	LastSeen      time.Time `json:"lastSeen"`
}

// Store : stockage en mémoire propre à order-service, protégé par un verrou.
type Store struct {
	mu      sync.RWMutex
	orders  []*Order
	nextID  int
	workers map[string]*WorkerHeartbeat
}

func NewStore() *Store {
	return &Store{nextID: 1, workers: map[string]*WorkerHeartbeat{}}
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}

func (s *Store) Add(name, email string, items []OrderItem) Order {
	s.mu.Lock()
	defer s.mu.Unlock()
	total := 0.0
	for _, it := range items {
		total += it.Subtotal
	}
	o := &Order{
		ID:            s.nextID,
		Number:        fmt.Sprintf("CMD-%04d", s.nextID),
		CustomerName:  name,
		CustomerEmail: email,
		Items:         items,
		Total:         round2(total),
		Status:        StatusConfirmed,
		CreatedAt:     time.Now().UTC(),
	}
	s.nextID++
	s.orders = append(s.orders, o)
	return *o
}

// List renvoie des copies, de la plus récente à la plus ancienne. notified=nil : pas de filtre.
func (s *Store) List(notified *bool) []Order {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]Order, 0, len(s.orders))
	for i := len(s.orders) - 1; i >= 0; i-- {
		o := s.orders[i]
		if notified != nil && (o.Notification != nil) != *notified {
			continue
		}
		result = append(result, *o)
	}
	return result
}

func (s *Store) find(id int) *Order {
	for _, o := range s.orders {
		if o.ID == id {
			return o
		}
	}
	return nil
}

func (s *Store) Get(id int) (Order, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if o := s.find(id); o != nil {
		return *o, nil
	}
	return Order{}, ErrNotFound
}

func (s *Store) SetNotification(id int, n Notification) (Order, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.find(id)
	if o == nil {
		return Order{}, ErrNotFound
	}
	if o.Notification == nil {
		n.SentAt = time.Now().UTC()
		o.Notification = &n
	}
	return *o, nil
}

func (s *Store) Cancel(id int) (Order, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.find(id)
	if o == nil {
		return Order{}, ErrNotFound
	}
	if o.Status == StatusCancelled {
		return *o, ErrAlreadyCancelled
	}
	now := time.Now().UTC()
	o.Status = StatusCancelled
	o.CancelledAt = &now
	return *o, nil
}

func (s *Store) Heartbeat(h WorkerHeartbeat) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h.LastSeen = time.Now().UTC()
	s.workers[h.Name+"@"+h.Hostname] = &h
}

func (s *Store) Workers() []WorkerHeartbeat {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]WorkerHeartbeat, 0, len(s.workers))
	for _, w := range s.workers {
		result = append(result, *w)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].LastSeen.After(result[j].LastSeen) })
	return result
}

type Stats struct {
	Orders    int     `json:"orders"`
	Confirmed int     `json:"confirmed"`
	Cancelled int     `json:"cancelled"`
	Pending   int     `json:"pendingNotifications"`
	Revenue   float64 `json:"revenue"`
}

func (s *Store) Stats() Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st := Stats{Orders: len(s.orders)}
	for _, o := range s.orders {
		if o.Status == StatusConfirmed {
			st.Confirmed++
			st.Revenue += o.Total
		} else {
			st.Cancelled++
		}
		if o.Notification == nil {
			st.Pending++
		}
	}
	st.Revenue = round2(st.Revenue)
	return st
}
