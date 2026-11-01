package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// CatalogClient appelle catalog-service via son URL interne (DNS du cluster / réseau Docker).
type CatalogClient struct {
	baseURL string
	http    *http.Client
}

type Product struct {
	ID    int     `json:"id"`
	Name  string  `json:"name"`
	Emoji string  `json:"emoji"`
	Price float64 `json:"price"`
	Stock int     `json:"stock"`
}

// CatalogError porte le statut HTTP à renvoyer au client.
type CatalogError struct {
	Status  int
	Message string
}

func (e *CatalogError) Error() string { return e.Message }

func NewCatalogClient(baseURL string, timeout time.Duration) *CatalogClient {
	return &CatalogClient{baseURL: strings.TrimRight(baseURL, "/"), http: &http.Client{Timeout: timeout}}
}

func (c *CatalogClient) post(ctx context.Context, requestID, path string, body any) (*Product, error) {
	payload, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if requestID != "" {
		req.Header.Set("X-Request-Id", requestID)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, &CatalogError{Status: http.StatusServiceUnavailable, Message: "catalog-service indisponible : " + err.Error()}
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		var apiErr struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(res.Body).Decode(&apiErr)
		if apiErr.Message == "" {
			apiErr.Message = fmt.Sprintf("catalog-service a répondu %d", res.StatusCode)
		}
		status := res.StatusCode
		if status >= 500 {
			status = http.StatusBadGateway
		}
		return nil, &CatalogError{Status: status, Message: apiErr.Message}
	}
	var p Product
	if err := json.NewDecoder(res.Body).Decode(&p); err != nil {
		return nil, &CatalogError{Status: http.StatusBadGateway, Message: "réponse invalide de catalog-service"}
	}
	return &p, nil
}

func (c *CatalogClient) Reserve(ctx context.Context, requestID string, productID, quantity int) (*Product, error) {
	return c.post(ctx, requestID, fmt.Sprintf("/products/%d/reserve", productID), map[string]int{"quantity": quantity})
}

func (c *CatalogClient) Release(ctx context.Context, requestID string, productID, quantity int) error {
	_, err := c.post(ctx, requestID, fmt.Sprintf("/products/%d/release", productID), map[string]int{"quantity": quantity})
	return err
}
