package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"theregister/internal/orders"
	"theregister/internal/store"
)

type OrderService interface {
	CreateOrder(context.Context, string, orders.Request, [32]byte, string) (orders.Response, error)
	AddProducts(context.Context, string, string, string, []orders.Product, [32]byte, string) (orders.Response, error)
}

type Server struct {
	db        *sql.DB
	orders    OrderService
	apiKey    string
	partnerID string
	logger    *slog.Logger
}

type Problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail"`
}

var correlationFallbackCounter atomic.Uint64

func NewServer(db *sql.DB, orderService OrderService, apiKey, partnerID string, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	server := &Server{db: db, orders: orderService, apiKey: apiKey, partnerID: partnerID, logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", server.health)
	mux.HandleFunc("GET /ready", server.ready)
	mux.Handle("POST /partner/v1/orders", server.partnerAuth(http.HandlerFunc(server.createOrder)))
	mux.Handle("POST /partner/v1/orders/{orderRef}/products", server.partnerAuth(http.HandlerFunc(server.addProducts)))
	return server.requestLogging(mux)
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		writeProblem(w, http.StatusServiceUnavailable, "Not ready", "database is not configured")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	if err := s.db.PingContext(ctx); err != nil {
		writeProblem(w, http.StatusServiceUnavailable, "Not ready", "database is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) partnerAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.apiKey == "" || s.partnerID == "" {
			writeProblem(w, http.StatusServiceUnavailable, "Partner API unavailable", "partner authentication is not configured")
			return
		}
		authorization := r.Header.Get("Authorization")
		if !strings.HasPrefix(authorization, "Bearer ") {
			writeProblem(w, http.StatusUnauthorized, "Unauthorized", "a valid partner bearer token is required")
			return
		}
		provided := strings.TrimPrefix(authorization, "Bearer ")
		if len(provided) != len(s.apiKey) || subtle.ConstantTimeCompare([]byte(provided), []byte(s.apiKey)) != 1 {
			writeProblem(w, http.StatusUnauthorized, "Unauthorized", "a valid partner bearer token is required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) createOrder(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, orders.MaxRequestBytes))
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "Invalid request", "request body is too large or could not be read")
		return
	}
	request, hash, err := orders.DecodeRequest(body)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "Invalid request", err.Error())
		return
	}
	if s.orders == nil {
		writeProblem(w, http.StatusServiceUnavailable, "Service unavailable", "order storage is not configured")
		return
	}
	response, err := s.orders.CreateOrder(r.Context(), s.partnerID, request, hash, correlationID(r.Context()))
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, response)
}

func (s *Server) addProducts(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, orders.MaxRequestBytes))
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "Invalid request", "request body is too large or could not be read")
		return
	}
	eventRef, products, hash, err := orders.DecodeProductAddition(body)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "Invalid request", err.Error())
		return
	}
	if s.orders == nil {
		writeProblem(w, http.StatusServiceUnavailable, "Service unavailable", "order storage is not configured")
		return
	}
	response, err := s.orders.AddProducts(
		r.Context(), s.partnerID, r.PathValue("orderRef"), eventRef, products, hash, correlationID(r.Context()),
	)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, response)
}

func (s *Server) writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrIdempotencyConflict):
		writeProblem(w, http.StatusConflict, "Conflict", err.Error())
	case errors.Is(err, store.ErrIdentifierConflict):
		writeProblem(w, http.StatusConflict, "Conflict", err.Error())
	case errors.Is(err, store.ErrOrderNotFound):
		writeProblem(w, http.StatusNotFound, "Not found", "order does not exist")
	default:
		s.logger.Error("order operation failed", slog.Any("error", err))
		writeProblem(w, http.StatusInternalServerError, "Internal server error", "the request could not be completed")
	}
}

func (s *Server) requestLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.Header.Get("X-Correlation-ID"))
		if !validCorrelationID(id) {
			generatedID, err := newCorrelationID()
			if err != nil {
				s.logger.Error("generate correlation ID", slog.Any("error", err))
				id = fmt.Sprintf("fallback-%d-%d", time.Now().UnixNano(), correlationFallbackCounter.Add(1))
			} else {
				id = generatedID
			}
		}
		w.Header().Set("X-Correlation-ID", id)
		ctx := context.WithValue(r.Context(), correlationIDKey{}, id)
		capture := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		started := time.Now()
		next.ServeHTTP(capture, r.WithContext(ctx))
		s.logger.InfoContext(ctx, "http request",
			slog.String("method", r.Method),
			slog.Int("status", capture.status),
			slog.Duration("duration", time.Since(started)),
			slog.String("correlation_id", id),
		)
	})
}

type correlationIDKey struct{}

func correlationID(ctx context.Context) string {
	value, _ := ctx.Value(correlationIDKey{}).(string)
	return value
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(body []byte) (int, error) {
	return w.ResponseWriter.Write(body)
}

func validCorrelationID(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9' || char == '-' || char == '_' || char == '.') {
			return false
		}
	}
	return true
}

func newCorrelationID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Internal server error", "the response could not be encoded")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n'))
}

func writeProblem(w http.ResponseWriter, status int, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Problem{
		Type:   "about:blank",
		Title:  title,
		Status: status,
		Detail: detail,
	})
}
