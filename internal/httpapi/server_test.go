package httpapi

import (
	"bytes"
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

func TestRequestLoggingUsesRouteTemplateWithoutSecrets(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	handler := NewServer(nil, &fakeOrderService{}, "test-secret", "partner-1", logger)
	request := httptest.NewRequest(http.MethodPost, "/partner/v1/orders/private-reference/products?token=private-query", strings.NewReader(`{"event_ref":"private-body","products":[]}`))
	request.Header.Set("Authorization", "Bearer test-secret")
	request.Header.Set("X-Correlation-ID", "trace-123")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	var entry struct {
		Route         string `json:"route"`
		Status        int    `json:"status"`
		CorrelationID string `json:"correlation_id"`
	}
	if err := json.Unmarshal(logs.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	if entry.Route != "POST /partner/v1/orders/{orderRef}/products" || entry.Status != http.StatusBadRequest || entry.CorrelationID != "trace-123" {
		t.Fatalf("unexpected request log: %#v", entry)
	}
	for _, secret := range []string{"private-reference", "private-query", "private-body", "test-secret"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("request log contains %q", secret)
		}
	}
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

func TestAPIConsoleTemplatesAndFields(t *testing.T) {
	response := httptest.NewRecorder()
	newTestHandler(nil).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/swagger/endpoints", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	var endpoints []struct {
		Path     string `json:"path"`
		Template struct {
			OrderRef   string           `json:"order_ref"`
			OccurredAt time.Time        `json:"occurred_at"`
			Buyer      orders.Party     `json:"buyer"`
			Seller     orders.Party     `json:"seller"`
			EventRef   string           `json:"event_ref"`
			Products   []orders.Product `json:"products"`
		} `json:"template"`
		ProductTemplate *orders.Product `json:"product_template"`
		Fields          []struct {
			Name     string `json:"name"`
			Required bool   `json:"required"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &endpoints); err != nil {
		t.Fatal(err)
	}
	tested := 0
	for _, endpoint := range endpoints {
		if endpoint.Path != "/partner/v1/orders" && endpoint.Path != "/partner/v1/orders/{order_ref}/products" {
			continue
		}
		tested++
		fields := make(map[string]bool)
		for _, field := range endpoint.Fields {
			fields[field.Name] = field.Required
		}
		for _, name := range []string{"Authorization", "products[].identifiers", "products[].identifiers[].scheme", "products[].resources[].uri"} {
			if required, ok := fields[name]; !ok || !required {
				t.Errorf("%s: %s should be required when included", endpoint.Path, name)
			}
		}
		for _, name := range []string{"products[].model", "products[].resources", "X-Correlation-ID"} {
			if required, ok := fields[name]; !ok || required {
				t.Errorf("%s: %s should be optional", endpoint.Path, name)
			}
		}
		if endpoint.Path == "/partner/v1/orders" {
			if fields["products"] || endpoint.Template.OccurredAt.IsZero() || len(endpoint.Template.Products) != 0 || endpoint.ProductTemplate == nil || len(endpoint.ProductTemplate.Identifiers) != 1 {
				t.Fatal("order products must be optional and timestamp prefilled")
			}
			order := orders.Request{OrderRef: "order", OccurredAt: endpoint.Template.OccurredAt,
				Buyer:  orders.Party{ExternalID: "buyer", Type: "PERSON"},
				Seller: orders.Party{ExternalID: "seller", Type: "ORGANIZATION"}, Products: endpoint.Template.Products}
			if _, _, err := orders.NormalizeAndValidate(order); err != nil {
				t.Fatalf("completed order template: %v", err)
			}
		} else {
			if !fields["order_ref"] || !fields["event_ref"] || !fields["products"] {
				t.Fatal("addition order_ref, event_ref and products must be required")
			}
			if len(endpoint.Template.Products) != 1 || len(endpoint.Template.Products[0].Identifiers) != 1 || len(endpoint.Template.Products[0].Resources) != 0 {
				t.Fatal("product addition template must contain an identifier and no optional resources")
			}
			endpoint.Template.Products[0].Identifiers[0].Scheme = "serial"
			endpoint.Template.Products[0].Identifiers[0].Value = "example"
			if _, err := orders.NormalizeProducts(endpoint.Template.Products, false); err != nil {
				t.Fatalf("completed product template: %v", err)
			}
		}
	}
	if tested != 2 {
		t.Fatalf("found %d partner templates, want 2", tested)
	}
}
