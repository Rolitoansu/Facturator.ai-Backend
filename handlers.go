package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

type ProfileUpdateRequest struct {
	DisplayName       *string  `json:"displayName"`
	Currency          *string  `json:"currency"`
	Locale            *string  `json:"locale"`
	MonthlyBudgetGoal *float64 `json:"monthlyBudgetGoal"`
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// ─── Receipts ────────────────────────────────────────────

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

	// Check subscription limit
	sub, _ := GetSubscription(userID)
	if sub != nil && sub.ReceiptLimit > 0 {
		count, err := GetMonthlyReceiptCount(userID)
		if err == nil && count >= sub.ReceiptLimit {
			writeError(w, http.StatusForbidden, fmt.Sprintf(
				"Monthly receipt limit reached (%d/%d). Upgrade to Pro for more.",
				count, sub.ReceiptLimit,
			))
			return
		}
	}

	err := r.ParseMultipartForm(10 << 20) // 10 MB
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

	receiptID := NewUUID()
	ext := ".jpg"
	if dotIdx := strings.LastIndex(header.Filename, "."); dotIdx >= 0 {
		ext = header.Filename[dotIdx:]
	}
	fileName := fmt.Sprintf("%s%s", receiptID, ext)
	contentType := DetectContentType(header.Filename)
	fileSize := int(header.Size)

	var imageURL string
	var storagePath *string

	if Storage != nil {
		// Upload to Supabase Storage
		result, err := Storage.Upload(userID, fileName, contentType, file)
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to upload file: %v", err))
			return
		}
		imageURL = result.PublicURL
		storagePath = &result.Path
	} else {
		writeError(w, http.StatusServiceUnavailable, "File storage is not configured")
		return
	}

	receipt := Receipt{
		ID:          receiptID,
		UserID:      userID,
		ImageURL:    imageURL,
		StoragePath: storagePath,
		RawText:     "",
		Status:      "processing",
		FileSize:    &fileSize,
		MimeType:    &contentType,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	if err := CreateReceipt(receipt); err != nil {
		fmt.Printf("Error inserting receipt into database: %v\n", err)
		writeError(w, http.StatusInternalServerError, "Failed to record receipt in database")
		return
	}

	writeJSON(w, http.StatusAccepted, receipt)

	// Process asynchronously
	go func(rcpt Receipt) {
		time.Sleep(1 * time.Second)

		// For OCR, we need the file content. Since it's in Supabase Storage,
		// we download it or use the URL directly.
		ocrResult, rawText, err := ProcessReceiptImage(rcpt.ImageURL)
		if err != nil {
			fmt.Printf("Error processing receipt %s: %v\n", rcpt.ID, err)
			_ = UpdateReceiptStatus(rcpt.ID, "failed", err.Error())

			GlobalHub.SendToUser(rcpt.UserID, SocketEvent{
				Type: "RECEIPT_PROCESSED",
				Payload: ReceiptProcessedPayload{
					ReceiptID: rcpt.ID,
					Status:    "error",
				},
			})
			return
		}

		dbStatus := "completed"
		wsStatus := "done"

		if err := UpdateReceiptStatus(rcpt.ID, dbStatus, rawText); err != nil {
			fmt.Printf("Database error updating receipt status: %v\n", err)
			return
		}

		txnID := NewUUID()
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

		// Ensure there's a budget for this category covering at least this amount
		currentMonth := transaction.Date
		if len(currentMonth) > 7 {
			currentMonth = currentMonth[:7] + "-01" // e.g., "2026-06-14" -> "2026-06-01"
		} else if len(currentMonth) == 7 {
			currentMonth = currentMonth + "-01"
		}
		
		// Insert budget with limit = amount if it doesn't exist, ON CONFLICT DO NOTHING (or we could update if it's smaller, but the user requested "at least")
		// We'll create it directly in DB
		if transaction.Category != "" && transaction.Category != "otros" {
			_, err := DB.Exec(`
				INSERT INTO budgets (id, user_id, category, limit_amount, month)
				VALUES ($1, $2, $3, $4, $5)
				ON CONFLICT (user_id, category, month) 
				DO UPDATE SET limit_amount = GREATEST(budgets.limit_amount, EXCLUDED.limit_amount)
			`, NewUUID(), transaction.UserID, transaction.Category, transaction.Amount, currentMonth)
			if err != nil {
				fmt.Printf("Database error creating automatic budget: %v\n", err)
			}
		}

		fmt.Printf("Receipt %s processed: Transaction %s (%s, %.2f)\n", rcpt.ID, txnID, transaction.Merchant, transaction.Amount)

		GlobalHub.SendToUser(rcpt.UserID, SocketEvent{
			Type: "RECEIPT_PROCESSED",
			Payload: ReceiptProcessedPayload{
				ReceiptID: rcpt.ID,
				Status:    wsStatus,
				Updates: map[string]interface{}{
					"id":           transaction.ID,
					"receiptId":    transaction.ReceiptID,
					"userId":       transaction.UserID,
					"amount":       transaction.Amount,
					"merchant":     transaction.Merchant,
					"category":     transaction.Category,
					"date":         transaction.Date,
					"confidence":   ocrResult.Confidence,
					"needs_review": ocrResult.NeedsReview,
				},
			},
		})
	}(receipt)
}

// ─── Transactions ────────────────────────────────────────

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

func handleUpdateTransaction(w http.ResponseWriter, r *http.Request) {
	userID := GetUserID(r)
	pathParts := strings.Split(r.URL.Path, "/")
	if len(pathParts) < 4 {
		writeError(w, http.StatusBadRequest, "Invalid path")
		return
	}
	txnID := pathParts[3]

	var req Transaction
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	req.ID = txnID
	req.UserID = userID

	existingTxn, err := GetTransactionByID(txnID, userID)
	if err != nil {
		writeError(w, http.StatusNotFound, "Transaction not found")
		return
	}

	if err := UpdateTransaction(req); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Si el usuario corrigió la categoría y viene de un ticket, mandamos feedback a la IA
	if req.Category != "" && req.Category != existingTxn.Category {
		if existingTxn.ReceiptID != nil {
			rawText, err := GetReceiptRawText(*existingTxn.ReceiptID)
			if err == nil && rawText != "" {
				SendFeedbackToML(rawText, req.Category)
				// Marcamos el ticket como 'done' para limpiar el posible estado 'needs_review'
				UpdateReceiptStatus(*existingTxn.ReceiptID, "processed", rawText)
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

// ─── Budgets ─────────────────────────────────────────────

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
	} else if len(req.Month) == 7 {
		req.Month = req.Month + "-01"
	}

	budget := Budget{
		ID:          NewUUID(),
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

// ─── Categories ──────────────────────────────────────────

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

	slug := normalizeLabel(req.Label)

	category := Category{
		ID:     NewUUID(),
		UserID: &userID,
		Slug:   slug,
		Label:  req.Label,
		Color:  req.Color,
	}

	if err := CreateCategory(category); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, category)
}

// ─── Profile ─────────────────────────────────────────────

func handleGetProfile(w http.ResponseWriter, r *http.Request) {
	userID := GetUserID(r)
	profile, err := GetUserProfile(userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if profile == nil {
		writeError(w, http.StatusNotFound, "Profile not found")
		return
	}
	writeJSON(w, http.StatusOK, profile)
}

func handleUpdateProfile(w http.ResponseWriter, r *http.Request) {
	userID := GetUserID(r)

	body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil {
		writeError(w, http.StatusBadRequest, "Error reading request body")
		return
	}

	var req ProfileUpdateRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if err := UpdateUserProfile(userID, req.DisplayName, req.Currency, req.Locale, req.MonthlyBudgetGoal); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

// ─── Helpers ─────────────────────────────────────────────

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
		val = "custom_" + NewUUID()[:8]
	}

	return val
}
