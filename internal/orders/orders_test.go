package orders

import (
	"strings"
	"testing"
)

func TestDecodeRequestAllowsEmptyProductsAndNormalizesEquivalentJSON(t *testing.T) {
	first := `{
		"order_ref":"  order-1 ",
		"occurred_at":"2026-11-21T12:00:00Z",
		"buyer":{"external_id":" buyer-1 ","type":"person"},
		"seller":{"external_id":"seller-1","type":"ORGANIZATION"}
	}`
	second := `{"seller":{"type":"ORGANIZATION","external_id":"seller-1"},"buyer":{"type":"PERSON","external_id":"buyer-1"},"occurred_at":"2026-11-21T12:00:00Z","order_ref":"order-1","products":[]}`

	request1, hash1, err := DecodeRequest([]byte(first))
	if err != nil {
		t.Fatalf("DecodeRequest(first): %v", err)
	}
	request2, hash2, err := DecodeRequest([]byte(second))
	if err != nil {
		t.Fatalf("DecodeRequest(second): %v", err)
	}
	if len(request1.Products) != 0 || len(request2.Products) != 0 {
		t.Fatalf("expected empty product lists, got %d and %d", len(request1.Products), len(request2.Products))
	}
	if hash1 != hash2 {
		t.Fatal("equivalent requests should have identical idempotency hashes")
	}
}

func TestDecodeRequestRejectsInvalidProductIdentifiersAndResourceURLs(t *testing.T) {
	base := `{"order_ref":"order-1","occurred_at":"2026-11-21T12:00:00Z","buyer":{"external_id":"b","type":"PERSON"},"seller":{"external_id":"s","type":"ORGANIZATION"},"products":[%s]}`
	tests := []struct {
		name    string
		product string
	}{
		{name: "missing identifier", product: `{"identifiers":[]}`},
		{name: "invalid scheme", product: `{"identifiers":[{"scheme":"bad scheme","value":"1"}]}`},
		{name: "http resource", product: `{"identifiers":[{"scheme":"serial","value":"1"}],"resources":[{"type":"DPP","uri":"http://example.test/dpp","access_level":"PUBLIC","resolver_type":"DIRECT_HTTPS"}]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := DecodeRequest([]byte(strings.Replace(base, "%s", test.product, 1)))
			if err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestDecodeRequestRejectsUnknownFieldsAndTrailingJSON(t *testing.T) {
	valid := `{"order_ref":"order-1","occurred_at":"2026-11-21T12:00:00Z","buyer":{"external_id":"b","type":"PERSON"},"seller":{"external_id":"s","type":"ORGANIZATION"}}`
	for _, body := range []string{
		strings.Replace(valid, `"order_ref":"order-1"`, `"order_ref":"order-1","unexpected":true`, 1),
		valid + `{}`,
	} {
		if _, _, err := DecodeRequest([]byte(body)); err == nil {
			t.Fatalf("expected error for %s", body)
		}
	}
}

func TestDecodeProductAdditionRequiresProductsAndStableEventRef(t *testing.T) {
	body := `{"event_ref":" add-1 ","products":[{"identifiers":[{"scheme":"serial","value":" 42 "}]}]}`
	eventRef, products, _, err := DecodeProductAddition([]byte(body))
	if err != nil {
		t.Fatalf("DecodeProductAddition: %v", err)
	}
	if eventRef != "add-1" || products[0].Identifiers[0].Value != "42" {
		t.Fatalf("unexpected normalized addition: %#v %#v", eventRef, products)
	}
	if _, _, _, err := DecodeProductAddition([]byte(`{"event_ref":"add-2","products":[]}`)); err == nil {
		t.Fatal("expected empty product additions to be rejected")
	}
}

func TestNormalizeAndValidateRequiresOccurredAt(t *testing.T) {
	_, _, err := NormalizeAndValidate(Request{
		OrderRef: "order-1",
		Buyer:    Party{ExternalID: "buyer", Type: "PERSON"},
		Seller:   Party{ExternalID: "seller", Type: "ORGANIZATION"},
	})
	if err == nil {
		t.Fatal("expected missing occurred_at error")
	}
}
