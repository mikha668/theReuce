package orders

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

type ProductAddition struct {
	EventRef string    `json:"event_ref"`
	Products []Product `json:"products"`
}

func DecodeProductAddition(data []byte) (string, []Product, [32]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var addition ProductAddition
	if err := decoder.Decode(&addition); err != nil {
		return "", nil, [32]byte{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return "", nil, [32]byte{}, errors.New("request must contain a single JSON object")
	}
	addition.EventRef = strings.TrimSpace(addition.EventRef)
	if addition.EventRef == "" || len(addition.EventRef) > 200 {
		return "", nil, [32]byte{}, errors.New("event_ref must contain 1 to 200 characters")
	}
	products, err := NormalizeProducts(addition.Products, false)
	if err != nil {
		return "", nil, [32]byte{}, err
	}
	addition.Products = products
	encoded, err := json.Marshal(addition)
	if err != nil {
		return "", nil, [32]byte{}, fmt.Errorf("encode normalized product addition: %w", err)
	}
	return addition.EventRef, addition.Products, sha256.Sum256(encoded), nil
}
