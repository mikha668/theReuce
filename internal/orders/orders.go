package orders

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const MaxRequestBytes = 1 << 20

var identifierSchemePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

type Party struct {
	ExternalID string `json:"external_id"`
	Type       string `json:"type"`
}

type Identifier struct {
	Scheme     string            `json:"scheme"`
	Value      string            `json:"value"`
	Qualifiers map[string]string `json:"qualifiers"`
}

type Resource struct {
	Type        string `json:"type"`
	URI         string `json:"uri"`
	MediaType   string `json:"media_type,omitempty"`
	Language    string `json:"language,omitempty"`
	AccessLevel string `json:"access_level"`
	Resolver    string `json:"resolver_type"`
}

type Product struct {
	Manufacturer string       `json:"manufacturer,omitempty"`
	Model        string       `json:"model,omitempty"`
	Identifiers  []Identifier `json:"identifiers"`
	Resources    []Resource   `json:"resources,omitempty"`
	TotalAmount  *Amount      `json:"total_amount,omitempty"`
	VATAmount    *Amount      `json:"vat_amount,omitempty"`
}

type Request struct {
	OrderRef    string    `json:"order_ref"`
	OccurredAt  time.Time `json:"occurred_at"`
	Buyer       Party     `json:"buyer"`
	Seller      Party     `json:"seller"`
	Products    []Product `json:"products"`
	Currency    string    `json:"currency,omitempty"`
	TotalAmount *Amount   `json:"total_amount,omitempty"`
	VATAmount   *Amount   `json:"vat_amount,omitempty"`
}

type ProductResult struct {
	ID string `json:"id"`
}

type Response struct {
	EventID    string          `json:"event_id"`
	Status     string          `json:"status"`
	OrderRef   string          `json:"order_ref"`
	ReceivedAt time.Time       `json:"received_at"`
	Products   []ProductResult `json:"products"`
}

func NormalizeAndValidate(request Request) (Request, [32]byte, error) {
	request.OrderRef = strings.TrimSpace(request.OrderRef)
	request.Buyer.ExternalID = strings.TrimSpace(request.Buyer.ExternalID)
	request.Buyer.Type = strings.ToUpper(strings.TrimSpace(request.Buyer.Type))
	request.Seller.ExternalID = strings.TrimSpace(request.Seller.ExternalID)
	request.Seller.Type = strings.ToUpper(strings.TrimSpace(request.Seller.Type))
	request.Currency = strings.ToUpper(strings.TrimSpace(request.Currency))

	if request.OrderRef == "" || len(request.OrderRef) > 200 {
		return Request{}, [32]byte{}, errors.New("order_ref must contain 1 to 200 characters")
	}
	if request.OccurredAt.IsZero() {
		return Request{}, [32]byte{}, errors.New("occurred_at is required")
	}
	if err := validateParty("buyer", request.Buyer); err != nil {
		return Request{}, [32]byte{}, err
	}
	if err := validateParty("seller", request.Seller); err != nil {
		return Request{}, [32]byte{}, err
	}
	products, err := NormalizeProducts(request.Products, true)
	if err != nil {
		return Request{}, [32]byte{}, err
	}
	request.Products = products
	if request.Currency != "" || request.TotalAmount != nil || request.VATAmount != nil {
		if err := ValidateNewOrderAmounts(request); err != nil {
			return Request{}, [32]byte{}, err
		}
	}

	encoded, err := json.Marshal(request)
	if err != nil {
		return Request{}, [32]byte{}, fmt.Errorf("encode normalized order: %w", err)
	}
	return request, sha256.Sum256(encoded), nil
}

func NormalizeProducts(products []Product, allowEmpty bool) ([]Product, error) {
	if products == nil {
		products = []Product{}
	}
	if !allowEmpty && len(products) == 0 {
		return nil, errors.New("products must contain at least one product")
	}
	for i := range products {
		product := &products[i]
		product.Manufacturer = strings.TrimSpace(product.Manufacturer)
		product.Model = strings.TrimSpace(product.Model)
		if (product.TotalAmount == nil) != (product.VATAmount == nil) {
			return nil, fmt.Errorf("products[%d].total_amount and vat_amount must be supplied together", i)
		}
		if product.TotalAmount != nil && (!amountPattern.MatchString(string(*product.TotalAmount)) || !amountPattern.MatchString(string(*product.VATAmount))) {
			return nil, fmt.Errorf("products[%d] contains an invalid amount", i)
		}
		if product.TotalAmount != nil && amountValue(product.VATAmount).Cmp(amountValue(product.TotalAmount)) > 0 {
			return nil, fmt.Errorf("products[%d].vat_amount must not exceed total_amount", i)
		}
		if len(product.Identifiers) == 0 {
			return nil, fmt.Errorf("products[%d] must have at least one identifier", i)
		}
		for j := range product.Identifiers {
			identifier := &product.Identifiers[j]
			identifier.Scheme = strings.TrimSpace(identifier.Scheme)
			identifier.Value = strings.TrimSpace(identifier.Value)
			if !identifierSchemePattern.MatchString(identifier.Scheme) {
				return nil, fmt.Errorf("products[%d].identifiers[%d].scheme is invalid", i, j)
			}
			if identifier.Value == "" || len(identifier.Value) > 512 {
				return nil, fmt.Errorf("products[%d].identifiers[%d].value must contain 1 to 512 characters", i, j)
			}
			if identifier.Qualifiers == nil {
				identifier.Qualifiers = map[string]string{}
			}
			for key, value := range identifier.Qualifiers {
				if strings.TrimSpace(key) == "" || len(key) > 64 || len(value) > 512 {
					return nil, fmt.Errorf("products[%d].identifiers[%d].qualifiers contains an invalid entry", i, j)
				}
			}
		}
		for j := range product.Resources {
			resource := &product.Resources[j]
			resource.URI = strings.TrimSpace(resource.URI)
			resource.Type = strings.ToUpper(strings.TrimSpace(resource.Type))
			resource.AccessLevel = strings.ToUpper(strings.TrimSpace(resource.AccessLevel))
			resource.Resolver = strings.ToUpper(strings.TrimSpace(resource.Resolver))
			parsed, err := url.ParseRequestURI(resource.URI)
			if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
				return nil, fmt.Errorf("products[%d].resources[%d].uri must be an HTTPS URL without user information", i, j)
			}
			if !oneOf(resource.Type, "DPP", "MANUAL", "WARRANTY", "RECEIPT", "REPAIR", "CERTIFICATE", "OTHER") {
				return nil, fmt.Errorf("products[%d].resources[%d].type is invalid", i, j)
			}
			if !oneOf(resource.AccessLevel, "PUBLIC", "OWNER", "ROLE_RESTRICTED") {
				return nil, fmt.Errorf("products[%d].resources[%d].access_level is invalid", i, j)
			}
			if !oneOf(resource.Resolver, "GS1_DIGITAL_LINK", "DIRECT_HTTPS", "STATIC_MAPPING", "VENDOR_API") {
				return nil, fmt.Errorf("products[%d].resources[%d].resolver_type is invalid", i, j)
			}
		}
	}
	return products, nil
}

func DecodeRequest(data []byte) (Request, [32]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var request Request
	if err := decoder.Decode(&request); err != nil {
		return Request{}, [32]byte{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Request{}, [32]byte{}, errors.New("request must contain a single JSON object")
	}
	return NormalizeAndValidate(request)
}

func validateParty(name string, party Party) error {
	if party.ExternalID == "" || len(party.ExternalID) > 200 {
		return fmt.Errorf("%s.external_id must contain 1 to 200 characters", name)
	}
	if !oneOf(party.Type, "PERSON", "ORGANIZATION", "UNKNOWN") {
		return fmt.Errorf("%s.type must be PERSON, ORGANIZATION, or UNKNOWN", name)
	}
	return nil
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}
