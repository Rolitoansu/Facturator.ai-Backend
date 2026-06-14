package main

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

// ─── Models ──────────────────────────────────────────────

type Receipt struct {
	ID          string    `json:"id"`
	UserID      string    `json:"userId"`
	ImageURL    string    `json:"imageUrl"`
	StoragePath *string   `json:"storagePath,omitempty"`
	RawText     string    `json:"rawText"`
	Status      string    `json:"status"`
	FileSize    *int      `json:"fileSize,omitempty"`
	MimeType    *string   `json:"mimeType,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type Transaction struct {
	ID          string   `json:"id"`
	ReceiptID   *string  `json:"receiptId"`
	UserID      string   `json:"userId"`
	Amount      float64  `json:"amount"`
	Merchant    string   `json:"merchant"`
	Category    string   `json:"category"`
	Description *string  `json:"description,omitempty"`
	Date        string   `json:"date"`
	CreatedAt   string   `json:"createdAt,omitempty"`
	UpdatedAt   string   `json:"updatedAt,omitempty"`
}

type Budget struct {
	ID          string  `json:"id"`
	UserID      string  `json:"userId"`
	Category    string  `json:"category"`
	LimitAmount float64 `json:"limitAmount"`
	Month       string  `json:"month"`
	CreatedAt   string  `json:"createdAt,omitempty"`
	UpdatedAt   string  `json:"updatedAt,omitempty"`
}

type Category struct {
	ID        string  `json:"id"`
	UserID    *string `json:"userId"`
	Slug      string  `json:"slug"`
	Label     string  `json:"label"`
	Color     string  `json:"color"`
	CreatedAt string  `json:"createdAt,omitempty"`
}

type UserProfile struct {
	ID                   string   `json:"id"`
	DisplayName          *string  `json:"displayName"`
	AvatarURL            *string  `json:"avatarUrl"`
	Currency             string   `json:"currency"`
	Locale               string   `json:"locale"`
	MonthlyBudgetGoal    *float64 `json:"monthlyBudgetGoal"`
	OnboardingCompleted  bool     `json:"onboardingCompleted"`
	CreatedAt            string   `json:"createdAt,omitempty"`
	UpdatedAt            string   `json:"updatedAt,omitempty"`
}

type Subscription struct {
	ID                    string  `json:"id"`
	UserID                string  `json:"userId"`
	Plan                  string  `json:"plan"`
	Status                string  `json:"status"`
	StripeCustomerID      *string `json:"stripeCustomerId,omitempty"`
	StripeSubscriptionID  *string `json:"stripeSubscriptionId,omitempty"`
	StripePriceID         *string `json:"stripePriceId,omitempty"`
	CurrentPeriodStart    *string `json:"currentPeriodStart,omitempty"`
	CurrentPeriodEnd      *string `json:"currentPeriodEnd,omitempty"`
	CancelAtPeriodEnd     bool    `json:"cancelAtPeriodEnd"`
	ReceiptLimit          int     `json:"receiptLimit"`
	CreatedAt             string  `json:"createdAt,omitempty"`
	UpdatedAt             string  `json:"updatedAt,omitempty"`
}

// ─── Database ────────────────────────────────────────────

var DB *sql.DB

func NewUUID() string {
	return uuid.New().String()
}

func InitDB(connStr string) error {
	var err error
	DB, err = sql.Open("postgres", connStr)
	if err != nil {
		return fmt.Errorf("error opening database: %w", err)
	}

	// Connection pool settings for Supabase
	DB.SetMaxOpenConns(10)
	DB.SetMaxIdleConns(5)
	DB.SetConnMaxLifetime(5 * time.Minute)

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

	fmt.Println("Connected to Supabase PostgreSQL.")
	return nil
}

// ─── Receipts ────────────────────────────────────────────

func GetReceipts(userID string) ([]Receipt, error) {
	rows, err := DB.Query(`
		SELECT id, user_id, image_url, COALESCE(storage_path, ''), raw_text, status, created_at, updated_at
		FROM receipts
		WHERE user_id = $1
		ORDER BY created_at DESC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var receipts []Receipt
	for rows.Next() {
		var r Receipt
		var storagePath string
		if err := rows.Scan(&r.ID, &r.UserID, &r.ImageURL, &storagePath, &r.RawText, &r.Status, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		if storagePath != "" {
			r.StoragePath = &storagePath
		}
		receipts = append(receipts, r)
	}
	return receipts, nil
}

func GetReceipt(id string) (*Receipt, error) {
	var r Receipt
	var storagePath sql.NullString
	err := DB.QueryRow(`
		SELECT id, user_id, image_url, storage_path, raw_text, status, created_at, updated_at
		FROM receipts WHERE id = $1
	`, id).Scan(&r.ID, &r.UserID, &r.ImageURL, &storagePath, &r.RawText, &r.Status, &r.CreatedAt, &r.UpdatedAt)

	if err == sql.ErrNoRows {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	if storagePath.Valid {
		r.StoragePath = &storagePath.String
	}
	return &r, nil
}

func CreateReceipt(r Receipt) error {
	_, err := DB.Exec(`
		INSERT INTO receipts (id, user_id, image_url, storage_path, raw_text, status, file_size, mime_type, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, r.ID, r.UserID, r.ImageURL, r.StoragePath, r.RawText, r.Status, r.FileSize, r.MimeType, r.CreatedAt)
	return err
}

func UpdateReceiptStatus(id string, status string, rawText string) error {
	_, err := DB.Exec(`UPDATE receipts SET status = $1, raw_text = $2 WHERE id = $3`, status, rawText, id)
	return err
}

// ─── Transactions ────────────────────────────────────────

func GetTransactions(userID string) ([]Transaction, error) {
	rows, err := DB.Query(`
		SELECT id, receipt_id, user_id, amount, merchant, category, COALESCE(description, ''), date
		FROM transactions
		WHERE user_id = $1
		ORDER BY date DESC, created_at DESC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var txns []Transaction
	for rows.Next() {
		var t Transaction
		var desc string
		if err := rows.Scan(&t.ID, &t.ReceiptID, &t.UserID, &t.Amount, &t.Merchant, &t.Category, &desc, &t.Date); err != nil {
			return nil, err
		}
		if desc != "" {
			t.Description = &desc
		}
		txns = append(txns, t)
	}
	return txns, nil
}

func CreateTransaction(t Transaction) error {
	_, err := DB.Exec(`
		INSERT INTO transactions (id, receipt_id, user_id, amount, merchant, category, description, date)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, t.ID, t.ReceiptID, t.UserID, t.Amount, t.Merchant, t.Category, t.Description, t.Date)
	return err
}

func DeleteTransactionsByCategory(userID string, category string) error {
	_, err := DB.Exec(`DELETE FROM transactions WHERE user_id = $1 AND category = $2`, userID, category)
	return err
}

func ReassignTransactionsCategory(userID string, oldCategory, newCategory string) error {
	_, err := DB.Exec(`UPDATE transactions SET category = $1 WHERE user_id = $2 AND category = $3`, newCategory, userID, oldCategory)
	return err
}

// ─── Budgets ─────────────────────────────────────────────

func GetBudgets(userID string) ([]Budget, error) {
	rows, err := DB.Query(`
		SELECT id, user_id, category, limit_amount, month
		FROM budgets
		WHERE user_id = $1
	`, userID)
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
	_, err := DB.Exec(`
		INSERT INTO budgets (id, user_id, category, limit_amount, month)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (user_id, category, month) DO UPDATE SET limit_amount = EXCLUDED.limit_amount
	`, b.ID, b.UserID, b.Category, b.LimitAmount, b.Month)
	return err
}

func UpdateBudget(id string, limitAmount float64) error {
	_, err := DB.Exec(`UPDATE budgets SET limit_amount = $1 WHERE id = $2`, limitAmount, id)
	return err
}

func DeleteBudget(id string) error {
	_, err := DB.Exec(`DELETE FROM budgets WHERE id = $1`, id)
	return err
}

// ─── Categories ──────────────────────────────────────────

func GetCategories(userID string) ([]Category, error) {
	rows, err := DB.Query(`
		SELECT id, user_id, slug, label, color
		FROM categories
		WHERE user_id IS NULL OR user_id = $1
		ORDER BY user_id NULLS FIRST, label ASC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var categories []Category
	for rows.Next() {
		var c Category
		if err := rows.Scan(&c.ID, &c.UserID, &c.Slug, &c.Label, &c.Color); err != nil {
			return nil, err
		}
		categories = append(categories, c)
	}
	return categories, nil
}

func CreateCategory(c Category) error {
	_, err := DB.Exec(`
		INSERT INTO categories (id, user_id, slug, label, color)
		VALUES ($1, $2, $3, $4, $5)
	`, c.ID, c.UserID, c.Slug, c.Label, c.Color)
	return err
}

// ─── User Profile ────────────────────────────────────────

func GetUserProfile(userID string) (*UserProfile, error) {
	var p UserProfile
	err := DB.QueryRow(`
		SELECT id, display_name, avatar_url, currency, locale, monthly_budget_goal, onboarding_completed
		FROM user_profiles
		WHERE id = $1
	`, userID).Scan(&p.ID, &p.DisplayName, &p.AvatarURL, &p.Currency, &p.Locale, &p.MonthlyBudgetGoal, &p.OnboardingCompleted)

	if err == sql.ErrNoRows {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	return &p, nil
}

func UpdateUserProfile(userID string, displayName *string, currency *string, locale *string, monthlyBudgetGoal *float64) error {
	_, err := DB.Exec(`
		UPDATE user_profiles SET
			display_name = COALESCE($2, display_name),
			currency = COALESCE($3, currency),
			locale = COALESCE($4, locale),
			monthly_budget_goal = COALESCE($5, monthly_budget_goal)
		WHERE id = $1
	`, userID, displayName, currency, locale, monthlyBudgetGoal)
	return err
}

// ─── Subscriptions ───────────────────────────────────────

func GetSubscription(userID string) (*Subscription, error) {
	var s Subscription
	err := DB.QueryRow(`
		SELECT id, user_id, plan, status, stripe_customer_id, stripe_subscription_id,
		       stripe_price_id, current_period_start, current_period_end,
		       cancel_at_period_end, receipt_limit
		FROM subscriptions
		WHERE user_id = $1
	`, userID).Scan(
		&s.ID, &s.UserID, &s.Plan, &s.Status,
		&s.StripeCustomerID, &s.StripeSubscriptionID, &s.StripePriceID,
		&s.CurrentPeriodStart, &s.CurrentPeriodEnd,
		&s.CancelAtPeriodEnd, &s.ReceiptLimit,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	return &s, nil
}

func UpdateSubscription(userID string, plan string, status string, stripeSubID *string, stripePriceID *string, periodStart *time.Time, periodEnd *time.Time, cancelAtEnd bool, receiptLimit int) error {
	_, err := DB.Exec(`
		UPDATE subscriptions SET
			plan = $2, status = $3, stripe_subscription_id = $4, stripe_price_id = $5,
			current_period_start = $6, current_period_end = $7,
			cancel_at_period_end = $8, receipt_limit = $9
		WHERE user_id = $1
	`, userID, plan, status, stripeSubID, stripePriceID, periodStart, periodEnd, cancelAtEnd, receiptLimit)
	return err
}

func UpsertStripeCustomer(userID string, stripeCustomerID string) error {
	_, err := DB.Exec(`
		UPDATE subscriptions SET stripe_customer_id = $2 WHERE user_id = $1
	`, userID, stripeCustomerID)
	return err
}

// ─── Usage Check ─────────────────────────────────────────

func GetMonthlyReceiptCount(userID string) (int, error) {
	var count int
	err := DB.QueryRow(`
		SELECT COUNT(*)
		FROM receipts
		WHERE user_id = $1
		AND created_at >= date_trunc('month', now())
		AND created_at < date_trunc('month', now()) + interval '1 month'
	`, userID).Scan(&count)
	return count, err
}
