package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
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

	"github.com/jackc/pgx/v5/pgconn"

	"theregister/internal/orders"
	"theregister/internal/store"
)

type OrderService interface {
	CreateOrder(context.Context, string, orders.Request, [32]byte, string) (orders.Response, error)
	AddProducts(context.Context, string, string, string, []orders.Product, [32]byte, string) (orders.Response, error)
}

type Server struct {
	db               *sql.DB
	orders           OrderService
	apiKey           string
	partnerID        string
	logger           *slog.Logger
	partyIdentityKey []byte
}

type Problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail"`
}

var correlationFallbackCounter atomic.Uint64

func NewServer(db *sql.DB, orderService OrderService, apiKey, partnerID string, logger *slog.Logger, partyIdentityKey ...[]byte) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	server := &Server{db: db, orders: orderService, apiKey: apiKey, partnerID: partnerID, logger: logger}
	if len(partyIdentityKey) > 0 {
		server.partyIdentityKey = partyIdentityKey[0]
	}
	mux := http.NewServeMux()
	mux.HandleFunc("OPTIONS /", server.options)
	mux.HandleFunc("OPTIONS /api/", server.options)
	mux.HandleFunc("GET /health", server.health)
	mux.HandleFunc("GET /ready", server.ready)
	mux.HandleFunc("GET /api/v1/overview", server.overview)
	mux.HandleFunc("GET /api/v1/partners", server.listPartners)
	mux.HandleFunc("POST /api/v1/partners", server.createPartner)
	mux.HandleFunc("GET /api/v1/parties", server.listParties)
	mux.HandleFunc("POST /api/v1/parties", server.createParty)
	mux.HandleFunc("GET /api/v1/party-profiles", server.listPartyProfiles)
	mux.HandleFunc("POST /api/v1/party-profiles", server.createPartyProfile)
	mux.HandleFunc("PUT /api/v1/party-profiles/{partyID}", server.updatePartyProfile)
	mux.HandleFunc("POST /api/v1/party/login", server.partyLogin)
	mux.HandleFunc("GET /api/v1/party/{partyID}/purchases", server.partyPurchases)
	mux.HandleFunc("GET /api/v1/swagger/endpoints", server.swaggerEndpoints)
	mux.Handle("POST /partner/v1/orders", server.partnerAuth(http.HandlerFunc(server.createOrder)))
	mux.Handle("POST /partner/v1/orders/{orderRef}/products", server.partnerAuth(http.HandlerFunc(server.addProducts)))
	return server.cors(server.requestLogging(mux))
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Correlation-ID")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) options(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	type counts struct {
		Partners  int `json:"partners"`
		Parties   int `json:"parties"`
		Purchases int `json:"purchases"`
		Products  int `json:"products"`
	}
	result := counts{}
	for query, dest := range map[string]*int{
		`SELECT COUNT(*) FROM partners`:          &result.Partners,
		`SELECT COUNT(*) FROM party_profiles`:    &result.Parties,
		`SELECT COUNT(*) FROM purchase_events`:   &result.Purchases,
		`SELECT COUNT(*) FROM product_instances`: &result.Products,
	} {
		if err := s.db.QueryRowContext(r.Context(), query).Scan(dest); err != nil {
			writeProblem(w, http.StatusInternalServerError, "Internal server error", "could not load overview")
			return
		}
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) listPartners(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.QueryContext(r.Context(), `SELECT id::text, name, partner_id, api_key, created_at FROM partners ORDER BY created_at DESC`)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Internal server error", "could not load partners")
		return
	}
	defer rows.Close()
	type partner struct {
		ID        string    `json:"id"`
		Name      string    `json:"name"`
		PartnerID string    `json:"partner_id"`
		APIKey    string    `json:"api_key"`
		CreatedAt time.Time `json:"created_at"`
	}
	result := []partner{}
	for rows.Next() {
		var item partner
		if err := rows.Scan(&item.ID, &item.Name, &item.PartnerID, &item.APIKey, &item.CreatedAt); err != nil {
			writeProblem(w, http.StatusInternalServerError, "Internal server error", "could not decode partners")
			return
		}
		result = append(result, item)
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) createPartner(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name      string `json:"name"`
		PartnerID string `json:"partner_id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&input); err != nil {
		writeProblem(w, http.StatusBadRequest, "Invalid request", "invalid partner JSON")
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	input.PartnerID = strings.TrimSpace(input.PartnerID)
	if input.Name == "" || input.PartnerID == "" {
		writeProblem(w, http.StatusBadRequest, "Invalid request", "name and partner_id are required")
		return
	}
	keyBytes := make([]byte, 32)
	if _, err := rand.Read(keyBytes); err != nil {
		s.logger.ErrorContext(r.Context(), "generate partner key failed", slog.String("correlation_id", correlationID(r.Context())))
		writeProblem(w, http.StatusInternalServerError, "Internal server error", "could not generate partner key")
		return
	}
	apiKey := base64.RawURLEncoding.EncodeToString(keyBytes)
	var id string
	err := s.db.QueryRowContext(r.Context(), `
		INSERT INTO partners (name, partner_id, api_key) VALUES ($1, $2, $3)
		RETURNING id::text`, input.Name, input.PartnerID, apiKey,
	).Scan(&id)
	if err != nil {
		writeProblem(w, http.StatusConflict, "Conflict", "partner could not be created")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id, "name": input.Name, "partner_id": input.PartnerID, "api_key": apiKey})
}

func (s *Server) partyHash(externalID string) []byte {
	mac := hmac.New(sha256.New, s.partyIdentityKey)
	mac.Write([]byte(strings.TrimSpace(externalID)))
	return mac.Sum(nil)
}

func (s *Server) listParties(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.QueryContext(r.Context(), `SELECT id::text, partner_id, party_type, created_at FROM parties ORDER BY created_at DESC`)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Internal server error", "could not load parties")
		return
	}
	defer rows.Close()
	type party struct {
		ID        string    `json:"id"`
		PartnerID string    `json:"partner_id"`
		Type      string    `json:"party_type"`
		CreatedAt time.Time `json:"created_at"`
	}
	result := []party{}
	for rows.Next() {
		var item party
		if err := rows.Scan(&item.ID, &item.PartnerID, &item.Type, &item.CreatedAt); err != nil {
			writeProblem(w, http.StatusInternalServerError, "Internal server error", "could not decode parties")
			return
		}
		result = append(result, item)
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) createParty(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ExternalID string `json:"external_id"`
		PartnerID  string `json:"partner_id"`
		Type       string `json:"party_type"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&input); err != nil {
		writeProblem(w, http.StatusBadRequest, "Invalid request", "invalid party JSON")
		return
	}
	input.ExternalID = strings.TrimSpace(input.ExternalID)
	input.PartnerID = strings.TrimSpace(input.PartnerID)
	input.Type = strings.ToUpper(strings.TrimSpace(input.Type))
	if input.ExternalID == "" || input.PartnerID == "" || !oneOf(input.Type, "PERSON", "ORGANIZATION", "UNKNOWN") {
		writeProblem(w, http.StatusBadRequest, "Invalid request", "external_id, partner_id and a valid party_type are required")
		return
	}
	var id string
	err := s.db.QueryRowContext(r.Context(), `
		INSERT INTO parties (partner_id, external_id_hash, party_type)
		VALUES ($1, $2, $3)
		ON CONFLICT (partner_id, external_id_hash) DO UPDATE SET party_type = EXCLUDED.party_type
		RETURNING id::text`, input.PartnerID, s.partyHash(input.ExternalID), input.Type,
	).Scan(&id)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Internal server error", "could not create party")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id, "partner_id": input.PartnerID, "external_id": input.ExternalID, "party_type": input.Type})
}

func (s *Server) listPartyProfiles(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT pp.id::text, pp.name, pp.party_type, pp.display_id, pp.created_at,
		       COALESCE((SELECT json_agg(px.alias ORDER BY px.created_at, px.id)
		                 FROM party_profile_aliases px WHERE px.party_profile_id = pp.id), '[]'::json)
		FROM party_profiles pp ORDER BY pp.created_at DESC`)
	if err != nil {
		s.logger.ErrorContext(r.Context(), "load party profiles", slog.Any("error", err))
		writeProblem(w, http.StatusInternalServerError, "Internal server error", "could not load party profiles")
		return
	}
	defer rows.Close()
	type profile struct {
		ID        string    `json:"id"`
		Name      string    `json:"name"`
		Type      string    `json:"party_type"`
		DisplayID string    `json:"display_id"`
		CreatedAt time.Time `json:"created_at"`
		Aliases   []string  `json:"aliases"`
	}
	result := []profile{}
	for rows.Next() {
		var item profile
		var aliases []byte
		if err := rows.Scan(&item.ID, &item.Name, &item.Type, &item.DisplayID, &item.CreatedAt, &aliases); err != nil {
			s.logger.ErrorContext(r.Context(), "decode party profile", slog.Any("error", err))
			writeProblem(w, http.StatusInternalServerError, "Internal server error", "could not decode party profiles")
			return
		}
		if err := json.Unmarshal(aliases, &item.Aliases); err != nil {
			s.logger.ErrorContext(r.Context(), "decode party aliases", slog.Any("error", err))
			writeProblem(w, http.StatusInternalServerError, "Internal server error", "could not decode party profiles")
			return
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		s.logger.ErrorContext(r.Context(), "read party profiles", slog.Any("error", err))
		writeProblem(w, http.StatusInternalServerError, "Internal server error", "could not load party profiles")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

type profileInput struct {
	Name      string   `json:"name"`
	Type      string   `json:"party_type"`
	DisplayID string   `json:"display_id"`
	Aliases   []string `json:"aliases"`
}

func (s *Server) createPartyProfile(w http.ResponseWriter, r *http.Request) {
	s.savePartyProfile(w, r, false)
}

func (s *Server) updatePartyProfile(w http.ResponseWriter, r *http.Request) {
	s.savePartyProfile(w, r, true)
}

func (s *Server) savePartyProfile(w http.ResponseWriter, r *http.Request, update bool) {
	var input profileInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&input); err != nil {
		writeProblem(w, http.StatusBadRequest, "Invalid request", "invalid party profile JSON")
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	input.Type = strings.ToUpper(strings.TrimSpace(input.Type))
	input.DisplayID = strings.TrimSpace(input.DisplayID)
	if input.Name == "" || input.DisplayID == "" || !oneOf(input.Type, "PERSON", "ORGANIZATION", "UNKNOWN") {
		writeProblem(w, http.StatusBadRequest, "Invalid request", "name, display_id and a valid party_type are required")
		return
	}
	seen := make(map[string]bool)
	for i := range input.Aliases {
		alias := &input.Aliases[i]
		*alias = strings.TrimSpace(*alias)
		if *alias == "" || strings.Contains(*alias, ",") {
			writeProblem(w, http.StatusBadRequest, "Invalid request", "aliases must be non-empty and must not contain commas")
			return
		}
		if seen[*alias] {
			writeProblem(w, http.StatusBadRequest, "Invalid request", "duplicate alias")
			return
		}
		seen[*alias] = true
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Internal server error", "could not start party profile creation")
		return
	}
	defer tx.Rollback()
	var id string
	if update {
		err = tx.QueryRowContext(r.Context(), `
			UPDATE party_profiles SET name = $1, party_type = $2, display_id = $3
			WHERE id::text = $4 RETURNING id::text`, input.Name, input.Type, input.DisplayID, r.PathValue("partyID")).Scan(&id)
	} else {
		err = tx.QueryRowContext(r.Context(), `
			INSERT INTO party_profiles (name, party_type, display_id)
			VALUES ($1, $2, $3) RETURNING id::text`, input.Name, input.Type, input.DisplayID).Scan(&id)
	}
	if errors.Is(err, sql.ErrNoRows) {
		writeProblem(w, http.StatusNotFound, "Not found", "party profile does not exist")
		return
	}
	if err != nil {
		s.writeProfileError(w, r, err)
		return
	}
	if update {
		rows, err := tx.QueryContext(r.Context(), `SELECT alias FROM party_profile_aliases WHERE party_profile_id = $1`, id)
		if err != nil {
			s.writeProfileError(w, r, err)
			return
		}
		var removed []string
		for rows.Next() {
			var existing string
			if err := rows.Scan(&existing); err != nil {
				rows.Close()
				s.writeProfileError(w, r, err)
				return
			}
			if seen[existing] {
				delete(seen, existing)
			} else {
				removed = append(removed, existing)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			s.writeProfileError(w, r, err)
			return
		}
		for _, alias := range removed {
			if _, err := tx.ExecContext(r.Context(), `DELETE FROM party_profile_aliases WHERE party_profile_id = $1 AND alias = $2`, id, alias); err != nil {
				s.writeProfileError(w, r, err)
				return
			}
		}
	}
	for _, alias := range input.Aliases {
		if !seen[alias] {
			continue
		}
		if _, err := tx.ExecContext(r.Context(), `
			INSERT INTO party_profile_aliases (party_profile_id, alias)
			VALUES ($1, $2)`, id, alias,
		); err != nil {
			s.writeProfileError(w, r, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		s.writeProfileError(w, r, err)
		return
	}
	status := http.StatusCreated
	if update {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]string{"id": id, "name": input.Name, "display_id": input.DisplayID, "party_type": input.Type})
}

func (s *Server) writeProfileError(w http.ResponseWriter, r *http.Request, err error) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		writeProblem(w, http.StatusConflict, "Conflict", "party ID or alias is already in use")
		return
	}
	s.logger.ErrorContext(r.Context(), "save party profile failed", slog.String("correlation_id", correlationID(r.Context())), slog.String("error_type", fmt.Sprintf("%T", err)))
	writeProblem(w, http.StatusInternalServerError, "Internal server error", "could not save party profile")
}

func (s *Server) swaggerEndpoints(w http.ResponseWriter, r *http.Request) {
	type field struct {
		Name     string `json:"name"`
		Location string `json:"location"`
		Required bool   `json:"required"`
		Type     string `json:"type"`
		Note     string `json:"note,omitempty"`
	}
	type endpoint struct {
		Method          string  `json:"method"`
		Path            string  `json:"path"`
		Summary         string  `json:"summary"`
		Auth            string  `json:"auth"`
		RequestBody     string  `json:"request_body"`
		Fields          []field `json:"fields"`
		Template        any     `json:"template,omitempty"`
		ProductTemplate any     `json:"product_template,omitempty"`
	}
	productFields := []field{
		{Name: "products[].manufacturer", Location: "body", Type: "string"},
		{Name: "products[].model", Location: "body", Type: "string"},
		{Name: "products[].total_amount", Location: "body", Required: true, Type: "decimal", Note: "Product total including VAT, in the order currency."},
		{Name: "products[].vat_amount", Location: "body", Required: true, Type: "decimal", Note: "VAT included in the product total."},
		{Name: "products[].identifiers", Location: "body", Required: true, Type: "array", Note: "At least one identifier per product."},
		{Name: "products[].identifiers[].scheme", Location: "body", Required: true, Type: "string", Note: "Letters, digits, dot, underscore or hyphen; starts with a letter or digit."},
		{Name: "products[].identifiers[].value", Location: "body", Required: true, Type: "string"},
		{Name: "products[].identifiers[].qualifiers", Location: "body", Type: "object", Note: "String key/value pairs."},
		{Name: "products[].resources", Location: "body", Type: "array"},
		{Name: "products[].resources[].type", Location: "body", Required: true, Type: "string", Note: "DPP, MANUAL, WARRANTY, RECEIPT, REPAIR, CERTIFICATE or OTHER."},
		{Name: "products[].resources[].uri", Location: "body", Required: true, Type: "HTTPS URL"},
		{Name: "products[].resources[].access_level", Location: "body", Required: true, Type: "string", Note: "PUBLIC, OWNER or ROLE_RESTRICTED."},
		{Name: "products[].resources[].resolver_type", Location: "body", Required: true, Type: "string", Note: "GS1_DIGITAL_LINK, DIRECT_HTTPS, STATIC_MAPPING or VENDOR_API."},
		{Name: "products[].resources[].media_type", Location: "body", Type: "string"},
		{Name: "products[].resources[].language", Location: "body", Type: "string"},
	}
	productTemplate := func() []any {
		return []any{map[string]any{
			"manufacturer": "", "model": "",
			"total_amount": nil, "vat_amount": nil,
			"identifiers": []any{map[string]any{"scheme": "", "value": "", "qualifiers": map[string]string{}}},
			"resources":   []any{},
		}}
	}
	orderFields := []field{
		{Name: "order_ref", Location: "body", Required: true, Type: "string"},
		{Name: "occurred_at", Location: "body", Required: true, Type: "date-time", Note: "Prefilled with the current time; change it to the actual event time."},
		{Name: "currency", Location: "body", Required: true, Type: "string", Note: "Three-letter currency code, for example SEK."},
		{Name: "total_amount", Location: "body", Required: true, Type: "decimal", Note: "Order total including VAT."},
		{Name: "vat_amount", Location: "body", Required: true, Type: "decimal", Note: "VAT included in the order total."},
		{Name: "buyer", Location: "body", Required: true, Type: "object"},
		{Name: "buyer.external_id", Location: "body", Required: true, Type: "string"},
		{Name: "buyer.type", Location: "body", Required: true, Type: "string", Note: "PERSON, ORGANIZATION or UNKNOWN."},
		{Name: "seller", Location: "body", Required: true, Type: "object"},
		{Name: "seller.external_id", Location: "body", Required: true, Type: "string"},
		{Name: "seller.type", Location: "body", Required: true, Type: "string", Note: "PERSON, ORGANIZATION or UNKNOWN."},
		{Name: "products", Location: "body", Type: "array", Note: "May be omitted or empty."},
	}
	additionFields := []field{
		{Name: "order_ref", Location: "path", Required: true, Type: "string"},
		{Name: "event_ref", Location: "body", Required: true, Type: "string"},
		{Name: "products", Location: "body", Required: true, Type: "array", Note: "At least one product."},
	}
	correlationField := field{Name: "X-Correlation-ID", Location: "header", Type: "string", Note: "Generated by the API if omitted."}
	authField := field{Name: "Authorization", Location: "header", Required: true, Type: "Bearer token", Note: "Autofilled from the selected partner."}
	writeJSON(w, http.StatusOK, []endpoint{
		{Method: "POST", Path: "/partner/v1/orders", Summary: "Register an order", Auth: "Bearer partner API key", RequestBody: "application/json",
			Fields: append(append(orderFields, productFields...), authField, correlationField), Template: map[string]any{
				"order_ref": "", "occurred_at": time.Now().UTC().Format(time.RFC3339),
				"buyer":    map[string]string{"external_id": "", "type": ""},
				"seller":   map[string]string{"external_id": "", "type": ""},
				"currency": "", "total_amount": nil, "vat_amount": nil,
				"products": []any{},
			}, ProductTemplate: productTemplate()[0]},
		{Method: "POST", Path: "/partner/v1/orders/{order_ref}/products", Summary: "Attach products to an order", Auth: "Bearer partner API key", RequestBody: "application/json",
			Fields: append(append(additionFields, productFields...), authField, correlationField), Template: map[string]any{"event_ref": "", "products": productTemplate()}},
		{Method: "GET", Path: "/health", Summary: "Check process liveness", Auth: "None", Fields: []field{}},
		{Method: "GET", Path: "/ready", Summary: "Check database readiness", Auth: "None", Fields: []field{}},
	})
}

func (s *Server) partyLogin(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ExternalID string `json:"external_id"`
		PartnerID  string `json:"partner_id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&input); err != nil {
		writeProblem(w, http.StatusBadRequest, "Invalid request", "invalid login JSON")
		return
	}
	input.ExternalID = strings.TrimSpace(input.ExternalID)
	input.PartnerID = strings.TrimSpace(input.PartnerID)
	if input.ExternalID == "" {
		writeProblem(w, http.StatusBadRequest, "Invalid request", "external_id is required")
		return
	}
	var id, partnerID, partyType string
	var err error
	hash := s.partyHash(input.ExternalID)
	if input.PartnerID != "" {
		err = s.db.QueryRowContext(r.Context(), `SELECT id::text, partner_id, party_type FROM parties WHERE partner_id=$1 AND external_id_hash=$2`, input.PartnerID, hash).Scan(&id, &partnerID, &partyType)
	} else {
		err = s.db.QueryRowContext(r.Context(), `SELECT id::text, partner_id, party_type FROM parties WHERE external_id_hash=$1 LIMIT 1`, hash).Scan(&id, &partnerID, &partyType)
	}
	if errors.Is(err, sql.ErrNoRows) {
		writeProblem(w, http.StatusUnauthorized, "Unauthorized", "party not found")
		return
	}
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Internal server error", "could not verify party")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "external_id": input.ExternalID, "partner_id": partnerID, "party_type": partyType})
}

func (s *Server) partyPurchases(w http.ResponseWriter, r *http.Request) {
	partyID := r.PathValue("partyID")
	identities, err := s.db.QueryContext(r.Context(), `
		SELECT partners.partner_id, aliases.alias
		FROM party_profile_aliases aliases
		CROSS JOIN (SELECT DISTINCT partner_id FROM purchase_events) partners
		WHERE aliases.party_profile_id::text = $1`, partyID)
	if err != nil {
		s.logger.ErrorContext(r.Context(), "load purchase identities", slog.Any("error", err))
		writeProblem(w, http.StatusInternalServerError, "Internal server error", "could not load purchases")
		return
	}
	partnerIDs := []string{}
	hashes := [][]byte{}
	for identities.Next() {
		var partnerID, alias string
		if err := identities.Scan(&partnerID, &alias); err != nil {
			identities.Close()
			s.logger.ErrorContext(r.Context(), "read purchase identities", slog.Any("error", err))
			writeProblem(w, http.StatusInternalServerError, "Internal server error", "could not load purchases")
			return
		}
		mac := hmac.New(sha256.New, s.partyIdentityKey)
		_, _ = mac.Write([]byte(partnerID))
		_, _ = mac.Write([]byte{0})
		_, _ = mac.Write([]byte(alias))
		partnerIDs = append(partnerIDs, partnerID)
		hashes = append(hashes, mac.Sum(nil))
	}
	err = identities.Err()
	identities.Close()
	if err != nil {
		s.logger.ErrorContext(r.Context(), "read purchase identities", slog.Any("error", err))
		writeProblem(w, http.StatusInternalServerError, "Internal server error", "could not load purchases")
		return
	}
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT pe.id::text, pe.order_ref, pe.occurred_at, pe.created_at,
		       pe.currency, pe.total_amount::text, pe.vat_amount::text,
		       pi.id::text, pi.manufacturer, pi.model, pp.total_amount::text, pp.vat_amount::text
		FROM purchase_events pe
		LEFT JOIN purchase_products pp ON pp.purchase_event_id = pe.id
		LEFT JOIN product_instances pi ON pi.id = pp.product_instance_id
		WHERE pe.buyer_party_id IN (
			SELECT pa.id
			FROM parties pa
			JOIN unnest($1::text[], $2::bytea[]) AS identity(partner_id, external_id_hash)
			  ON identity.partner_id = pa.partner_id AND identity.external_id_hash = pa.external_id_hash
		)
		ORDER BY pe.occurred_at DESC, pe.created_at DESC`, partnerIDs, hashes)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Internal server error", "could not load purchases")
		return
	}
	defer rows.Close()

	type product struct {
		ID           string           `json:"id"`
		Manufacturer string           `json:"manufacturer"`
		Model        string           `json:"model"`
		Identifiers  []map[string]any `json:"identifiers"`
		Resources    []map[string]any `json:"resources"`
		TotalAmount  *string          `json:"total_amount"`
		VATAmount    *string          `json:"vat_amount"`
	}
	type purchase struct {
		ID          string    `json:"id"`
		OrderRef    string    `json:"order_ref"`
		OccurredAt  time.Time `json:"occurred_at"`
		CreatedAt   time.Time `json:"created_at"`
		Products    []product `json:"products"`
		Currency    *string   `json:"currency"`
		TotalAmount *string   `json:"total_amount"`
		VATAmount   *string   `json:"vat_amount"`
	}
	purchases := map[string]*purchase{}
	order := []string{}
	for rows.Next() {
		var purchaseID, orderRef string
		var productID, manufacturer, model, currency, totalAmount, vatAmount, productTotal, productVAT sql.NullString
		var occurredAt, createdAt time.Time
		if err := rows.Scan(&purchaseID, &orderRef, &occurredAt, &createdAt, &currency, &totalAmount, &vatAmount, &productID, &manufacturer, &model, &productTotal, &productVAT); err != nil {
			writeProblem(w, http.StatusInternalServerError, "Internal server error", "could not decode purchases")
			return
		}
		if _, exists := purchases[purchaseID]; !exists {
			purchases[purchaseID] = &purchase{ID: purchaseID, OrderRef: orderRef, OccurredAt: occurredAt, CreatedAt: createdAt, Products: []product{}, Currency: nullableString(currency), TotalAmount: nullableString(totalAmount), VATAmount: nullableString(vatAmount)}
			order = append(order, purchaseID)
		}
		if !productID.Valid {
			continue
		}
		p := purchases[purchaseID]
		item := product{ID: productID.String, Manufacturer: manufacturer.String, Model: model.String, Identifiers: []map[string]any{}, Resources: []map[string]any{}, TotalAmount: nullableString(productTotal), VATAmount: nullableString(productVAT)}
		identifierRows, err := s.db.QueryContext(r.Context(), `SELECT scheme, value, qualifiers FROM product_identifiers WHERE product_instance_id=$1`, productID.String)
		if err == nil {
			defer identifierRows.Close()
			for identifierRows.Next() {
				var scheme, value string
				var qualifiers []byte
				if err := identifierRows.Scan(&scheme, &value, &qualifiers); err == nil {
					var qualifierMap map[string]any
					_ = json.Unmarshal(qualifiers, &qualifierMap)
					item.Identifiers = append(item.Identifiers, map[string]any{"scheme": scheme, "value": value, "qualifiers": qualifierMap})
				}
			}
		}
		resourceRows, err := s.db.QueryContext(r.Context(), `SELECT resource_type, uri, media_type, language, access_level, resolver_type FROM external_resources WHERE product_instance_id=$1 AND active=true`, productID.String)
		if err == nil {
			defer resourceRows.Close()
			for resourceRows.Next() {
				var resourceType, uri, accessLevel, resolverType string
				var mediaType, language sql.NullString
				if err := resourceRows.Scan(&resourceType, &uri, &mediaType, &language, &accessLevel, &resolverType); err == nil {
					item.Resources = append(item.Resources, map[string]any{"type": resourceType, "uri": uri, "media_type": mediaType.String, "language": language.String, "access_level": accessLevel, "resolver_type": resolverType})
				}
			}
		}
		p.Products = append(p.Products, item)
	}
	result := make([]purchase, 0, len(order))
	for _, id := range order {
		result = append(result, *purchases[id])
	}
	writeJSON(w, http.StatusOK, result)
}

func nullableString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
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
		if s.db == nil && (s.apiKey == "" || s.partnerID == "") {
			writeProblem(w, http.StatusServiceUnavailable, "Partner API unavailable", "partner authentication is not configured")
			return
		}
		authorization := r.Header.Get("Authorization")
		if !strings.HasPrefix(authorization, "Bearer ") {
			writeProblem(w, http.StatusUnauthorized, "Unauthorized", "a valid partner bearer token is required")
			return
		}
		provided := strings.TrimPrefix(authorization, "Bearer ")
		if provided == "" || len(provided) > 4096 {
			writeProblem(w, http.StatusUnauthorized, "Unauthorized", "a valid partner bearer token is required")
			return
		}
		partnerID := ""
		if s.db != nil {
			rows, err := s.db.QueryContext(r.Context(), `SELECT partner_id FROM partners WHERE api_key = $1 LIMIT 2`, provided)
			if err != nil {
				s.logger.ErrorContext(r.Context(), "partner authentication lookup failed", slog.String("correlation_id", correlationID(r.Context())), slog.String("error_type", fmt.Sprintf("%T", err)))
				writeProblem(w, http.StatusServiceUnavailable, "Partner API unavailable", "partner authentication is unavailable")
				return
			}
			if rows.Next() {
				err = rows.Scan(&partnerID)
				if err == nil && rows.Next() {
					err = errors.New("partner key is assigned to multiple partners")
				}
			}
			if err == nil {
				err = rows.Err()
			}
			rows.Close()
			if err != nil {
				s.logger.ErrorContext(r.Context(), "partner authentication lookup failed", slog.String("correlation_id", correlationID(r.Context())), slog.String("error_type", fmt.Sprintf("%T", err)))
				writeProblem(w, http.StatusServiceUnavailable, "Partner API unavailable", "partner authentication is unavailable")
				return
			}
		}
		configuredMatch := s.apiKey != "" && s.partnerID != "" && len(provided) == len(s.apiKey) && subtle.ConstantTimeCompare([]byte(provided), []byte(s.apiKey)) == 1
		if configuredMatch {
			if partnerID != "" && partnerID != s.partnerID {
				s.logger.ErrorContext(r.Context(), "partner key has conflicting identities", slog.String("correlation_id", correlationID(r.Context())))
				writeProblem(w, http.StatusServiceUnavailable, "Partner API unavailable", "partner authentication is unavailable")
				return
			}
			partnerID = s.partnerID
		}
		if partnerID == "" {
			writeProblem(w, http.StatusUnauthorized, "Unauthorized", "a valid partner bearer token is required")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), partnerIDKey{}, partnerID)))
	})
}

type partnerIDKey struct{}

func authenticatedPartnerID(ctx context.Context) string {
	partnerID, _ := ctx.Value(partnerIDKey{}).(string)
	return partnerID
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
	response, err := s.orders.CreateOrder(r.Context(), authenticatedPartnerID(r.Context()), request, hash, correlationID(r.Context()))
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	s.logger.InfoContext(r.Context(), "order operation accepted",
		slog.String("operation", "purchase.registered"),
		slog.String("partner_id", authenticatedPartnerID(r.Context())),
		slog.String("event_id", response.EventID),
		slog.String("correlation_id", correlationID(r.Context())),
	)
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
		r.Context(), authenticatedPartnerID(r.Context()), r.PathValue("orderRef"), eventRef, products, hash, correlationID(r.Context()),
	)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	s.logger.InfoContext(r.Context(), "order operation accepted",
		slog.String("operation", "purchase.products_added"),
		slog.String("partner_id", authenticatedPartnerID(r.Context())),
		slog.String("event_id", response.EventID),
		slog.String("correlation_id", correlationID(r.Context())),
	)
	writeJSON(w, http.StatusAccepted, response)
}

func (s *Server) writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrIdempotencyConflict):
		writeProblem(w, http.StatusConflict, "Conflict", err.Error())
	case errors.Is(err, store.ErrIdentifierConflict):
		writeProblem(w, http.StatusConflict, "Conflict", err.Error())
	case errors.Is(err, store.ErrProductAlreadyOnOrder):
		writeProblem(w, http.StatusConflict, "Conflict", err.Error())
	case errors.Is(err, store.ErrInvalidAmounts):
		writeProblem(w, http.StatusBadRequest, "Invalid request", err.Error())
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
		request := r.WithContext(ctx)
		next.ServeHTTP(capture, request)
		route := request.Pattern
		if route == "" {
			route = "unmatched"
		}
		duration := time.Since(started)
		s.logger.InfoContext(ctx, "http request",
			slog.String("method", r.Method),
			slog.String("route", route),
			slog.Int("status", capture.status),
			slog.Duration("duration", duration),
			slog.Float64("duration_ms", float64(duration)/float64(time.Millisecond)),
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
