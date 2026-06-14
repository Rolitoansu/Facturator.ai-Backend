package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

type OCRResult struct {
	Merchant string  `json:"merchant"`
	Amount   float64 `json:"amount"`
	Category string  `json:"category"`
	Date     string  `json:"date"`
}

func ProcessReceiptImage(imageURL string) (OCRResult, string, error) {
	fmt.Printf("Processing receipt: %s\n", imageURL)

	result, err := callFacturatorMLService(imageURL)
	if err != nil {
		fmt.Printf("Facturator ML service failed: %v. Using fallback mock...\n", err)
		result = processMockOCR(imageURL)
		rawText := fmt.Sprintf("FALLBACK MOCK (ML SERVICE OFFLINE):\nComercio: %s\nTotal: %.2f EUR\nFecha: %s\nCategoría: %s",
			result.Merchant, result.Amount, result.Date, result.Category)
		return result, rawText, nil
	}

	rawText := fmt.Sprintf("FACTURATOR ML RESULTS:\nComercio: %s\nTotal: %.2f EUR\nFecha: %s\nCategoría: %s",
		result.Merchant, result.Amount, result.Date, result.Category)

	return result, rawText, nil
}

func callFacturatorMLService(imageURL string) (OCRResult, error) {
	mlURL := os.Getenv("FACTURATOR_ML_URL")
	if mlURL == "" {
		mlURL = "http://localhost:8000/predict" // Asumimos que correrá en el puerto 8000
	}

	// Enviamos la URL de la imagen en formato JSON
	requestBody, err := json.Marshal(map[string]string{
		"image_url": imageURL,
	})
	if err != nil {
		return OCRResult{}, fmt.Errorf("error marshalling json: %w", err)
	}

	req, err := http.NewRequest("POST", mlURL, bytes.NewBuffer(requestBody))
	if err != nil {
		return OCRResult{}, fmt.Errorf("error creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return OCRResult{}, fmt.Errorf("error calling Facturator ML service: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return OCRResult{}, fmt.Errorf("Facturator ML returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var result OCRResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return OCRResult{}, fmt.Errorf("error decoding Facturator ML response: %w", err)
	}

	return result, nil
}

func processMockOCR(imageURL string) OCRResult {
	// Mock fallback por si el servicio de ML no está levantado
	merchants := []string{"Lidl", "Carrefour", "Uber", "Zara", "Decathlon", "Burguer King", "Starbucks", "Gasolinera Repsol"}
	merchant := merchants[rand.Intn(len(merchants))]

	amounts := []float64{12.50, 18.90, 32.40, 54.20, 8.45, 120.00, 25.60, 45.00}
	amount := amounts[rand.Intn(len(amounts))]

	categories := []string{"alimentacion", "transporte", "ropa", "ocio", "suscripciones", "hogar"}
	category := categories[rand.Intn(len(categories))]

	date := time.Now().Format("2006-01-02")
	dateRegex := regexp.MustCompile(`\d{4}-\d{2}-\d{2}`)
	if match := dateRegex.FindString(imageURL); match != "" {
		date = match
	}

	return OCRResult{
		Merchant: merchant,
		Amount:   amount,
		Category: category,
		Date:     date,
	}
}
