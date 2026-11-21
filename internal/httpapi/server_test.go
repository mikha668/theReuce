package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"theregister/internal/orders"
)

type fakeOrderService struct {
	createCalls int
	addCalls    int
}

func (s *fakeOrderService) CreateOrder(_ context.Context, _ string, request orders.Request, _ [32]byte, _ string) (orders.Response, error) {
	s.createCalls++
	return orders.Response{
		EventID: "event-1", Status: "accepted", OrderRef: request.OrderRef,
		ReceivedAt: time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC),
		Products:   []orders.ProductResult{},
	}, nil
}

func (s *fakeOrderService) AddProducts(_ context.Context, _ string, orderRef, _ string, products []orders.Product, _ [32]byte, _ string) (orders.Response, error) {
	s.addCalls++
	return orders.Response{
		EventID: "addition-1", Status: "accepted", OrderRef: orderRef,
		ReceivedAt: time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC),
		Products:   make([]orders.ProductResult, len(products)),
	}, nil
}

func newTestHandler(service OrderService) http.Handler {
	logger := slog.New(slog.NewTextHandler(httptest.NewRecorder(), nil))
	return NewServer(nil, service, "test-secret", "partner-1", logger)
}

func TestHealthAndReadinessHaveCorrelationIDs(t *testing.T) {
	handler := newTestHandler(nil)
	for _, test := range []struct {
		path       string
		wantStatus int
	}{
		{path: "/health", wantStatus: http.StatusOK},
		{path: "/ready", wantStatus: http.StatusServiceUnavailable},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
		if response.Code != test.wantStatus {
			t.Fatalf("%s status = %d, want %d", test.path, response.Code, test.wantStatus)
		}
		if response.Header().Get("X-Correlation-ID") == "" {
			t.Fatalf("%s did not return a correlation ID", test.path)
		}
	}
}

func TestCreateOrderRequiresPartnerBearerToken(t *testing.T) {
	service := &fakeOrderService{}
	handler := newTestHandler(service)
	for _, authorization := range []string{"", "test-secret", "Basic test-secret", "Bearer wrong-secret"} {
		request := httptest.NewRequest(http.MethodPost, "/partner/v1/orders", strings.NewReader(`{}`))
		request.Header.Set("Authorization", authorization)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("authorization %q: status = %d, want %d", authorization, response.Code, http.StatusUnauthorized)
		}
	}
	if service.createCalls != 0 {
		t.Fatal("unauthorized request reached order service")
	}
}

func TestCreateOrderPassesValidatedRequestAndReturnsAccepted(t *testing.T) {
	service := &fakeOrderService{}
	handler := newTestHandler(service)
	body := `{"order_ref":"order-1","occurred_at":"2026-11-21T12:00:00Z","buyer":{"external_id":"buyer-1","type":"PERSON"},"seller":{"external_id":"seller-1","type":"ORGANIZATION"}}`
	request := httptest.NewRequest(http.MethodPost, "/partner/v1/orders", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer test-secret")
	request.Header.Set("X-Correlation-ID", "request-123")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusAccepted, response.Body.String())
	}
	if response.Header().Get("X-Correlation-ID") != "request-123" {
		t.Fatalf("correlation ID = %q", response.Header().Get("X-Correlation-ID"))
	}
	var result orders.Response
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result.OrderRef != "order-1" || service.createCalls != 1 {
		t.Fatalf("unexpected result %#v, calls %d", result, service.createCalls)
	}
}

func TestAddProductsRejectsMalformedRequests(t *testing.T) {
	service := &fakeOrderService{}
	handler := newTestHandler(service)
	request := httptest.NewRequest(http.MethodPost, "/partner/v1/orders/order-1/products", strings.NewReader(`{"event_ref":"add-1","products":[]}`))
	request.Header.Set("Authorization", "Bearer test-secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
	if service.addCalls != 0 {
		t.Fatal("invalid request reached product addition service")
	}
}
