package orders

import (
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
)

var amountPattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,14})(\.[0-9]{1,4})?$`)
var currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)

// Amount is a JSON decimal number, represented exactly rather than as a float.
type Amount string

func (a *Amount) UnmarshalJSON(data []byte) error {
	value := string(data)
	if !amountPattern.MatchString(value) {
		return errors.New("amount must be a non-negative decimal number with at most 15 integer digits and 4 decimal places")
	}
	if strings.Contains(value, ".") {
		value = strings.TrimRight(strings.TrimRight(value, "0"), ".")
	}
	*a = Amount(value)
	return nil
}

func (a Amount) MarshalJSON() ([]byte, error) {
	if !amountPattern.MatchString(string(a)) {
		return nil, errors.New("invalid decimal amount")
	}
	return []byte(a), nil
}

func amountValue(a *Amount) *big.Rat {
	value, _ := new(big.Rat).SetString(string(*a))
	return value
}

func ValidateNewOrderAmounts(request Request) error {
	if request.TotalAmount == nil || request.VATAmount == nil || !currencyPattern.MatchString(request.Currency) {
		return errors.New("total_amount, vat_amount and a three-letter currency are required")
	}
	if !amountPattern.MatchString(string(*request.TotalAmount)) || !amountPattern.MatchString(string(*request.VATAmount)) {
		return errors.New("invalid order amount")
	}
	if amountValue(request.VATAmount).Cmp(amountValue(request.TotalAmount)) > 0 {
		return errors.New("vat_amount must not exceed total_amount")
	}
	return ValidateProductTotals(request.TotalAmount, request.VATAmount, request.Products, "0", "0")
}

func ValidateProductTotals(total, vat *Amount, products []Product, previousTotal, previousVAT string) error {
	productTotal, ok := new(big.Rat).SetString(previousTotal)
	if !ok {
		return errors.New("invalid stored product total")
	}
	productVAT, ok := new(big.Rat).SetString(previousVAT)
	if !ok {
		return errors.New("invalid stored product VAT")
	}
	for i, product := range products {
		if product.TotalAmount == nil || product.VATAmount == nil {
			return fmt.Errorf("products[%d].total_amount and vat_amount are required", i)
		}
		if !amountPattern.MatchString(string(*product.TotalAmount)) || !amountPattern.MatchString(string(*product.VATAmount)) {
			return fmt.Errorf("products[%d] contains an invalid amount", i)
		}
		amount, tax := amountValue(product.TotalAmount), amountValue(product.VATAmount)
		if tax.Cmp(amount) > 0 {
			return fmt.Errorf("products[%d].vat_amount must not exceed total_amount", i)
		}
		productTotal.Add(productTotal, amount)
		productVAT.Add(productVAT, tax)
	}
	orderTotal, orderVAT := amountValue(total), amountValue(vat)
	orderNet := new(big.Rat).Sub(orderTotal, orderVAT)
	productNet := new(big.Rat).Sub(productTotal, productVAT)
	if productTotal.Cmp(orderTotal) > 0 || productVAT.Cmp(orderVAT) > 0 || productNet.Cmp(orderNet) > 0 {
		return errors.New("product totals, VAT, or amounts excluding VAT exceed the order amounts")
	}
	return nil
}
