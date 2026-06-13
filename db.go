package main

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/lib/pq"
)

type Receipt struct {
	ID        string    `json:"id"`
	UserID    string    `json:"userId"`
	ImageURL  string    `json:"imageUrl"`
	RawText   string    `json:"rawText"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
}

type Transaction struct {
	ID        string  `json:"id"`
	ReceiptID *string `json:"receiptId"`
	UserID    string  `json:"userId"`
	Amount    float64 `json:"amount"`
	Merchant  string  `json:"merchant"`
	Category  string  `json:"category"`
	Date      string  `json:"date"`
}

type Budget struct {
	ID          string  `json:"id"`
	UserID      string  `json:"userId"`
	Category    string  `json:"category"`
	LimitAmount float64 `json:"limitAmount"`
	Month       string  `json:"month"`
}

type Category struct {
	ID     string  `json:"id"`
	UserID *string `json:"userId"`
	Label  string  `json:"label"`
	Color  string  `json:"color"`
}

var DB *sql.DB

func InitDB(connStr string) error {
	var err error
	DB, err = sql.Open("postgres", connStr)
	if err != nil {
		return fmt.Errorf("error opening database: %w", err)
	}

	for i := 0; i < 5; i++ {
		err = DB.Ping()
		if err == nil {
			break
		}
		fmt.Printf("Database connection failed, retrying in 2s... (%d/5)\n", i+1)
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		return fmt.Errorf("could not connect to database after retries: %w", err)
	}

	fmt.Println("Database connection established.")

	if err := migrate(); err != nil {
		return fmt.Errorf("error migrating database: %w", err)
	}

	return nil
}

func migrate() error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS categories (
			id TEXT PRIMARY KEY,
			user_id TEXT,
			label TEXT NOT NULL,
			color TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS receipts (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			image_url TEXT NOT NULL,
			raw_text TEXT NOT NULL,
			status TEXT NOT NULL,
			created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE TABLE IF NOT EXISTS transactions (
			id TEXT PRIMARY KEY,
			receipt_id TEXT REFERENCES receipts(id) ON DELETE SET NULL,
			user_id TEXT NOT NULL,
			amount DOUBLE PRECISION NOT NULL,
			merchant TEXT NOT NULL,
			category TEXT NOT NULL,
			date TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS budgets (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			category TEXT NOT NULL,
			limit_amount DOUBLE PRECISION NOT NULL,
			month TEXT NOT NULL
		);`,
	}

	for _, query := range queries {
		if _, err := DB.Exec(query); err != nil {
			return err
		}
	}

	var count int
	err := DB.QueryRow("SELECT COUNT(*) FROM categories WHERE user_id IS NULL").Scan(&count)
	if err != nil {
		return err
	}

	if count == 0 {
		fmt.Println("Seeding categories...")
		defaultCategories := []Category{
			{ID: "alimentacion", Label: "alimentación", Color: "#4ade80"},
			{ID: "transporte", Label: "transporte", Color: "#22d3ee"},
			{ID: "ropa", Label: "ropa", Color: "#f87171"},
			{ID: "ocio", Label: "ocio", Color: "#f59e0b"},
			{ID: "salud", Label: "salud", Color: "#60a5fa"},
			{ID: "hogar", Label: "hogar", Color: "#a78bfa"},
			{ID: "suscripciones", Label: "suscripciones", Color: "#ff3e00"},
		}

		for _, cat := range defaultCategories {
			_, err := DB.Exec("INSERT INTO categories (id, user_id, label, color) VALUES ($1, NULL, $2, $3)", cat.ID, cat.Label, cat.Color)
			if err != nil {
				return fmt.Errorf("error seeding category %s: %w", cat.ID, err)
			}
		}
	}

	return nil
}

func GetReceipts(userID string) ([]Receipt, error) {
	rows, err := DB.Query("SELECT id, user_id, image_url, raw_text, status, created_at FROM receipts WHERE user_id = $1 ORDER BY created_at DESC", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var receipts []Receipt
	for rows.Next() {
		var r Receipt
		if err := rows.Scan(&r.ID, &r.UserID, &r.ImageURL, &r.RawText, &r.Status, &r.CreatedAt); err != nil {
			return nil, err
		}
		receipts = append(receipts, r)
	}
	return receipts, nil
}

func GetReceipt(id string) (*Receipt, error) {
	var r Receipt
	err := DB.QueryRow("SELECT id, user_id, image_url, raw_text, status, created_at FROM receipts WHERE id = $1", id).
		Scan(&r.ID, &r.UserID, &r.ImageURL, &r.RawText, &r.Status, &r.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	return &r, nil
}

func CreateReceipt(r Receipt) error {
	_, err := DB.Exec("INSERT INTO receipts (id, user_id, image_url, raw_text, status, created_at) VALUES ($1, $2, $3, $4, $5, $6)",
		r.ID, r.UserID, r.ImageURL, r.RawText, r.Status, r.CreatedAt)
	return err
}

func UpdateReceiptStatus(id string, status string, rawText string) error {
	_, err := DB.Exec("UPDATE receipts SET status = $1, raw_text = $2 WHERE id = $3", status, rawText, id)
	return err
}

func GetTransactions(userID string) ([]Transaction, error) {
	rows, err := DB.Query("SELECT id, receipt_id, user_id, amount, merchant, category, date FROM transactions WHERE user_id = $1 ORDER BY date DESC, id DESC", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var txns []Transaction
	for rows.Next() {
		var t Transaction
		if err := rows.Scan(&t.ID, &t.ReceiptID, &t.UserID, &t.Amount, &t.Merchant, &t.Category, &t.Date); err != nil {
			return nil, err
		}
		txns = append(txns, t)
	}
	return txns, nil
}

func CreateTransaction(t Transaction) error {
	_, err := DB.Exec("INSERT INTO transactions (id, receipt_id, user_id, amount, merchant, category, date) VALUES ($1, $2, $3, $4, $5, $6, $7)",
		t.ID, t.ReceiptID, t.UserID, t.Amount, t.Merchant, t.Category, t.Date)
	return err
}

func DeleteTransactionsByCategory(userID string, category string) error {
	_, err := DB.Exec("DELETE FROM transactions WHERE user_id = $1 AND category = $2", userID, category)
	return err
}

func ReassignTransactionsCategory(userID string, oldCategory, newCategory string) error {
	_, err := DB.Exec("UPDATE transactions SET category = $1 WHERE user_id = $2 AND category = $3", newCategory, userID, oldCategory)
	return err
}

func GetBudgets(userID string) ([]Budget, error) {
	rows, err := DB.Query("SELECT id, user_id, category, limit_amount, month FROM budgets WHERE user_id = $1", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var budgets []Budget
	for rows.Next() {
		var b Budget
		if err := rows.Scan(&b.ID, &b.UserID, &b.Category, &b.LimitAmount, &b.Month); err != nil {
			return nil, err
		}
		budgets = append(budgets, b)
	}
	return budgets, nil
}

func CreateBudget(b Budget) error {
	_, err := DB.Exec("INSERT INTO budgets (id, user_id, category, limit_amount, month) VALUES ($1, $2, $3, $4, $5) ON CONFLICT (id) DO UPDATE SET limit_amount = EXCLUDED.limit_amount",
		b.ID, b.UserID, b.Category, b.LimitAmount, b.Month)
	return err
}

func UpdateBudget(id string, limitAmount float64) error {
	_, err := DB.Exec("UPDATE budgets SET limit_amount = $1 WHERE id = $2", limitAmount, id)
	return err
}

func DeleteBudget(id string) error {
	_, err := DB.Exec("DELETE FROM budgets WHERE id = $1", id)
	return err
}

func GetCategories(userID string) ([]Category, error) {
	rows, err := DB.Query("SELECT id, user_id, label, color FROM categories WHERE user_id IS NULL OR user_id = $1 ORDER BY user_id NULLS FIRST, label ASC", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var categories []Category
	for rows.Next() {
		var c Category
		if err := rows.Scan(&c.ID, &c.UserID, &c.Label, &c.Color); err != nil {
			return nil, err
		}
		categories = append(categories, c)
	}
	return categories, nil
}

func CreateCategory(c Category) error {
	_, err := DB.Exec("INSERT INTO categories (id, user_id, label, color) VALUES ($1, $2, $3, $4)",
		c.ID, c.UserID, c.Label, c.Color)
	return err
}
