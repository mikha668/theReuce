package orders

import (
	"strings"
	"testing"
)

func TestOrderAmountValidationAndNormalization(t *testing.T) {
	base := `{"order_ref":"one","occurred_at":"2026-01-01T00:00:00Z","buyer":{"external_id":"b","type":"PERSON"},"seller":{"external_id":"s","type":"ORGANIZATION"},%s}`
	valid := `"currency":"sek","total_amount":100.00,"vat_amount":20.0000,"products":[]`
	request, hash, err := DecodeRequest([]byte(strings.Replace(base, "%s", valid, 1)))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateNewOrderAmounts(request); err != nil {
		t.Fatal(err)
	}
	_, sameHash, err := DecodeRequest([]byte(strings.Replace(base, "%s", `"currency":"SEK","total_amount":100,"vat_amount":20`, 1)))
	if err != nil || hash != sameHash {
		t.Fatalf("equivalent decimal amounts must have the same hash: %v", err)
	}
	for _, invalid := range []string{
		`"currency":"SEK","total_amount":-1,"vat_amount":0`,
		`"currency":"SEK","total_amount":"10","vat_amount":0`,
		`"currency":"SEK","total_amount":1.00001,"vat_amount":0`,
		`"currency":"SEK","total_amount":1e2,"vat_amount":0`,
		`"currency":"SE","total_amount":100,"vat_amount":20`,
		`"currency":"SEK","total_amount":100,"vat_amount":101`,
		`"currency":"SEK","total_amount":100,"vat_amount":20,"products":[{"total_amount":81,"vat_amount":0,"identifiers":[{"scheme":"serial","value":"1"}]}]`,
		`"currency":"SEK","total_amount":100,"vat_amount":20,"products":[{"total_amount":10,"identifiers":[{"scheme":"serial","value":"1"}]}]`,
	} {
		if _, _, err := DecodeRequest([]byte(strings.Replace(base, "%s", invalid, 1))); err == nil {
			t.Errorf("expected invalid amounts to be rejected: %s", invalid)
		}
	}
}
