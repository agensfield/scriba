package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"

	"github.com/agensfield/scriba/internal/pricing"
)

func main() {
	path := "internal/pricing/catalog.json"
	data, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	canonical, err := pricing.CheckCatalog(data)
	if err != nil {
		panic(err)
	}
	again, err := pricing.CheckCatalog(canonical)
	if err != nil {
		panic(err)
	}
	if string(canonical) != string(again) {
		fmt.Fprintln(os.Stderr, "pricing catalog generation is not deterministic")
		os.Exit(1)
	}
	var binding struct {
		SourceReceipt       string `json:"source_receipt"`
		SourceReceiptSHA256 string `json:"source_receipt_sha256"`
	}
	if err := json.Unmarshal(data, &binding); err != nil {
		panic(err)
	}
	receipt, err := os.ReadFile(binding.SourceReceipt)
	if err != nil {
		panic(err)
	}
	digest := sha256.Sum256(receipt)
	if hex.EncodeToString(digest[:]) != binding.SourceReceiptSHA256 {
		fmt.Fprintln(os.Stderr, "pricing source receipt hash mismatch")
		os.Exit(1)
	}
	fmt.Printf("pricing catalog valid: %s\n", path)
}
