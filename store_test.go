package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStoreAddNumbersOrdersAndComputesTotal(t *testing.T) {
	s := NewStore()
	o := s.Add("Sarah", "sarah@test.io", []OrderItem{{ProductID: 1, Subtotal: 10.1}, {ProductID: 2, Subtotal: 5.25}})
	if o.Number != "CMD-0001" || o.Total != 15.35 || o.Status != StatusConfirmed {
		t.Fatalf("commande inattendue : %+v", o)
	}
	if s.Add("B", "b@test.io", nil).Number != "CMD-0002" {
		t.Fatal("numérotation incorrecte")
	}
}

func TestStoreNotificationFilterAndCancel(t *testing.T) {
	s := NewStore()
	a := s.Add("A", "a@test.io", nil)
	s.Add("B", "b@test.io", nil)
	if _, err := s.SetNotification(a.ID, Notification{Channel: "email", Message: "ok"}); err != nil {
		t.Fatal(err)
	}
	f := false
	if pending := s.List(&f); len(pending) != 1 || pending[0].CustomerName != "B" {
		t.Fatalf("filtre notified=false incorrect : %+v", pending)
	}
	if _, err := s.Cancel(a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Cancel(a.ID); !errors.Is(err, ErrAlreadyCancelled) {
		t.Fatal("une double annulation doit être refusée")
	}
	if _, err := s.Get(999); !errors.Is(err, ErrNotFound) {
		t.Fatal("commande inexistante attendue")
	}
}

func TestCreateOrderCompensatesWhenStockIsInsufficient(t *testing.T) {
	released := 0
	catalog := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/products/1/reserve":
			_ = json.NewEncoder(w).Encode(Product{ID: 1, Name: "Souris", Price: 10})
		case r.URL.Path == "/products/2/reserve":
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"message":"Stock insuffisant"}`))
		case strings.HasSuffix(r.URL.Path, "/release"):
			released++
			_ = json.NewEncoder(w).Encode(Product{ID: 1})
		}
	}))
	defer catalog.Close()

	srv := NewServer(NewStore(), NewCatalogClient(catalog.URL, 0), Config{CatalogURL: catalog.URL})
	body := `{"customerName":"Sarah","customerEmail":"sarah@test.io","items":[{"productId":1,"quantity":1},{"productId":2,"quantity":3}]}`
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/orders", strings.NewReader(body)))

	if rec.Code != http.StatusConflict {
		t.Fatalf("statut attendu 409, reçu %d : %s", rec.Code, rec.Body.String())
	}
	if released != 1 {
		t.Fatalf("le stock du produit 1 aurait dû être libéré (compensation), releases=%d", released)
	}
	if len(srv.store.List(nil)) != 0 {
		t.Fatal("aucune commande ne doit être enregistrée")
	}
}
