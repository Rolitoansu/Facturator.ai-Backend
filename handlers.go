package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type BudgetCreateRequest struct {
	Category    string  `json:"category"`
	LimitAmount float64 `json:"limitAmount"`
	Month       string  `json:"month"`
}

type BudgetUpdateRequest struct {
	LimitAmount float64 `json:"limitAmount"`
}

type CategoryCreateRequest struct {
	Label string `json:"label"`
	Color string `json:"color"`
}

var GeminiAPIKey string

func generateID(prefix string) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s_%x", prefix, b)
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func handleGetReceipts(w http.ResponseWriter, r *http.Request) {
	userID := GetUserID(r)
	receipts, err := GetReceipts(userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, receipts)
}

func handleUploadReceipt(w http.ResponseWriter, r *http.Request) {
	userID := GetUserID(r)

	err := r.ParseMultipartForm(10 << 20)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Failed to parse multipart form")
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "File parameter is required")
		return
	}
	defer file.Close()

	uploadDir := "./uploads/receipts"
	if err := os.MkdirAll(uploadDir, os.ModePerm); err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to create uploads directory")
		return
	}

	ext := filepath.Ext(header.Filename)
	uniqueName := fmt.Sprintf("%d%s", time.Now().UnixNano(), ext)
	filePath := filepath.Join(uploadDir, uniqueName)

	out, err := os.Create(filePath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to save file on server")
		return
	}
	defer out.Close()

	_, err = io.Copy(out, file)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to write file content")
		return
	}

	receiptID := generateID("rcpt")
	imageURL := fmt.Sprintf("/uploads/receipts/%s", uniqueName)

	receipt := Receipt{
		ID:        receiptID,
		UserID:    userID,
		ImageURL:  imageURL,
		RawText:   "",
		Status:    "processing",
		CreatedAt: time.Now(),
	}

	if err := CreateReceipt(receipt); err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to record receipt in database")
		return
	}

	writeJSON(w, http.StatusAccepted, receipt)

	go func(rcpt Receipt, fileP string) {
		time.Sleep(1 * time.Second)

		ocrResult, rawText, err := ProcessReceiptImage(fileP, GeminiAPIKey)
		if err != nil {
			fmt.Printf("Error processing receipt %s: %v\n", rcpt.ID, err)
			_ = UpdateReceiptStatus(rcpt.ID, "error", err.Error())

			GlobalHub.SendToUser(rcpt.UserID, SocketEvent{
				Type: "RECEIPT_PROCESSED",
				Payload: ReceiptProcessedPayload{
					ReceiptID: rcpt.ID,
					Status:    "error",
				},
			})
			return
		}

		if err := UpdateReceiptStatus(rcpt.ID, "done", rawText); err != nil {
			fmt.Printf("Database error updating receipt status: %v\n", err)
			return
		}

		txnID := generateID("txn")
		transaction := Transaction{
			ID:        txnID,
			ReceiptID: &rcpt.ID,
			UserID:    rcpt.UserID,
			Amount:    ocrResult.Amount,
			Merchant:  ocrResult.Merchant,
			Category:  ocrResult.Category,
			Date:      ocrResult.Date,
		}

		if err := CreateTransaction(transaction); err != nil {
			fmt.Printf("Database error creating transaction: %v\n", err)
			return
		}

		fmt.Printf("Receipt %s processed: Transaction %s (%s, %.2f)\n", rcpt.ID, txnID, transaction.Merchant, transaction.Amount)

		GlobalHub.SendToUser(rcpt.UserID, SocketEvent{
			Type: "RECEIPT_PROCESSED",
			Payload: ReceiptProcessedPayload{
				ReceiptID: rcpt.ID,
				Status:    "done",
				Updates: map[string]interface{}{
					"id":        transaction.ID,
					"receiptId": transaction.ReceiptID,
					"userId":    transaction.UserID,
					"amount":    transaction.Amount,
					"merchant":  transaction.Merchant,
					"category":  transaction.Category,
					"date":      transaction.Date,
				},
			},
		})
	}(receipt, filePath)
}

func handleGetTransactions(w http.ResponseWriter, r *http.Request) {
	userID := GetUserID(r)
	txns, err := GetTransactions(userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, txns)
}

func handleDeleteTransactions(w http.ResponseWriter, r *http.Request) {
	userID := GetUserID(r)
	category := r.URL.Query().Get("category")

	if category == "" {
		writeError(w, http.StatusBadRequest, "Category parameter is required")
		return
	}

	err := DeleteTransactionsByCategory(userID, category)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func handleReassignTransactions(w http.ResponseWriter, r *http.Request) {
	userID := GetUserID(r)
	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")

	if from == "" || to == "" {
		writeError(w, http.StatusBadRequest, "Both 'from' and 'to' parameters are required")
		return
	}

	err := ReassignTransactionsCategory(userID, from, to)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func handleGetBudgets(w http.ResponseWriter, r *http.Request) {
	userID := GetUserID(r)
	budgets, err := GetBudgets(userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, budgets)
}

func handleCreateBudget(w http.ResponseWriter, r *http.Request) {
	userID := GetUserID(r)

	var req BudgetCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if req.Category == "" || req.LimitAmount <= 0 {
		writeError(w, http.StatusBadRequest, "Category and valid limit amount are required")
		return
	}

	if req.Month == "" {
		req.Month = time.Now().Format("2006-01-02")
	}

	budget := Budget{
		ID:          generateID("bdg"),
		UserID:      userID,
		Category:    req.Category,
		LimitAmount: req.LimitAmount,
		Month:       req.Month,
	}

	if err := CreateBudget(budget); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, budget)
}

func handleUpdateBudget(w http.ResponseWriter, r *http.Request) {
	pathParts := strings.Split(r.URL.Path, "/")
	if len(pathParts) < 4 {
		writeError(w, http.StatusBadRequest, "Invalid path")
		return
	}
	budgetID := pathParts[3]

	var req BudgetUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if req.LimitAmount <= 0 {
		writeError(w, http.StatusBadRequest, "Valid limit amount is required")
		return
	}

	if err := UpdateBudget(budgetID, req.LimitAmount); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func handleDeleteBudget(w http.ResponseWriter, r *http.Request) {
	pathParts := strings.Split(r.URL.Path, "/")
	if len(pathParts) < 4 {
		writeError(w, http.StatusBadRequest, "Invalid path")
		return
	}
	budgetID := pathParts[3]

	if err := DeleteBudget(budgetID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func handleGetCategories(w http.ResponseWriter, r *http.Request) {
	userID := GetUserID(r)
	categories, err := GetCategories(userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, categories)
}

func handleCreateCategory(w http.ResponseWriter, r *http.Request) {
	userID := GetUserID(r)

	var req CategoryCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if req.Label == "" || req.Color == "" {
		writeError(w, http.StatusBadRequest, "Label and color are required")
		return
	}

	normalizedID := normalizeLabel(req.Label)

	category := Category{
		ID:     normalizedID,
		UserID: &userID,
		Label:  req.Label,
		Color:  req.Color,
	}

	if err := CreateCategory(category); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, category)
}

func normalizeLabel(label string) string {
	val := strings.ToLower(label)
	val = strings.TrimSpace(val)

	replacements := map[string]string{
		"á": "a", "é": "e", "í": "i", "ó": "o", "ú": "u",
		"ñ": "n", "ü": "u",
	}
	for oldChar, newChar := range replacements {
		val = strings.ReplaceAll(val, oldChar, newChar)
	}

	reg := regexp.MustCompile("[^a-z0-9]+")
	val = reg.ReplaceAllString(val, "_")
	val = strings.Trim(val, "_")

	if val == "" {
		nBig, _ := rand.Int(rand.Reader, big.NewInt(10000))
		val = fmt.Sprintf("cat_%d", nBig.Int64())
	}

	return val
}
