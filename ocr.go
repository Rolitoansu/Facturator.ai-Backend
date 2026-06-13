package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
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

func ProcessReceiptImage(imagePath string, apiKey string) (OCRResult, string, error) {
	fmt.Printf("Processing receipt: %s\n", imagePath)

	result, err := callPythonMLService(imagePath)
	if err != nil {
		fmt.Printf("Python ML service failed: %v. Using fallback mock...\n", err)
		result = processMockOCR(imagePath)
		rawText := fmt.Sprintf("FALLBACK MOCK (ML SERVICE OFFLINE):\nComercio: %s\nTotal: %.2f EUR\nFecha: %s\nCategoría: %s",
			result.Merchant, result.Amount, result.Date, result.Category)
		return result, rawText, nil
	}

	rawText := fmt.Sprintf("ML SERVICE RESULTS:\nComercio: %s\nTotal: %.2f EUR\nFecha: %s\nCategoría: %s",
		result.Merchant, result.Amount, result.Date, result.Category)

	return result, rawText, nil
}

func callPythonMLService(imagePath string) (OCRResult, error) {
	file, err := os.Open(imagePath)
	if err != nil {
		return OCRResult{}, fmt.Errorf("error opening file: %w", err)
	}
	defer file.Close()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", filepath.Base(imagePath))
	if err != nil {
		return OCRResult{}, fmt.Errorf("error creating form file: %w", err)
	}
	_, err = io.Copy(part, file)
	if err != nil {
		return OCRResult{}, fmt.Errorf("error copying file content: %w", err)
	}
	err = writer.Close()
	if err != nil {
		return OCRResult{}, fmt.Errorf("error closing writer: %w", err)
	}

	mlURL := os.Getenv("ML_SERVER_URL")
	if mlURL == "" {
		mlURL = "http://localhost:5000/predict"
	}

	req, err := http.NewRequest("POST", mlURL, body)
	if err != nil {
		return OCRResult{}, fmt.Errorf("error creating request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return OCRResult{}, fmt.Errorf("error calling Python ML service: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return OCRResult{}, fmt.Errorf("Python ML service returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var result OCRResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return OCRResult{}, fmt.Errorf("error decoding Python ML service response: %w", err)
	}

	return result, nil
}

func processMockOCR(imagePath string) OCRResult {
	filename := strings.ToLower(filepath.Base(imagePath))

	var merchant string
	var amount float64
	var category string

	if strings.Contains(filename, "mercadona") {
		merchant = "Mercadona"
		amount = 67.40
		category = "alimentacion"
	} else if strings.Contains(filename, "renfe") || strings.Contains(filename, "ave") {
		merchant = "Renfe AVE"
		amount = 43.50
		category = "transporte"
	} else if strings.Contains(filename, "el-corte-ingles") || strings.Contains(filename, "corte") {
		merchant = "El Corte Ingles"
		amount = 129.00
		category = "ropa"
	} else if strings.Contains(filename, "spotify") {
		merchant = "Spotify"
		amount = 22.90
		category = "suscripciones"
	} else {
		merchants := []string{"Lidl", "Carrefour", "Uber", "Zara", "Decathlon", "Burguer King", "Starbucks", "Gasolinera Repsol"}
		merchant = merchants[rand.Intn(len(merchants))]

		amounts := []float64{12.50, 18.90, 32.40, 54.20, 8.45, 120.00, 25.60, 45.00}
		amount = amounts[rand.Intn(len(amounts))]

		categories := []string{"alimentacion", "transporte", "ropa", "ocio", "suscripciones", "hogar"}
		category = categories[rand.Intn(len(categories))]
	}

	date := time.Now().Format("2006-01-02")
	dateRegex := regexp.MustCompile(`\d{4}-\d{2}-\d{2}`)
	if match := dateRegex.FindString(filename); match != "" {
		date = match
	}

	return OCRResult{
		Merchant: merchant,
		Amount:   amount,
		Category: category,
		Date:     date,
	}
}
