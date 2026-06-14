package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/joho/godotenv"
)

var Storage *SupabaseStorage

func main() {
	godotenv.Load() // Loads .env file if it exists

	port := requireEnv("PORT", "8080")
	dbURL := requireEnv("DATABASE_URL", "")
	supabaseURL := requireEnv("SUPABASE_URL", "")
	supabaseAnonKey := requireEnv("SUPABASE_ANON_KEY", "")
	supabaseServiceKey := requireEnv("SUPABASE_SERVICE_ROLE_KEY", "")
	frontendOrigin := requireEnv("FRONTEND_ORIGIN", "http://localhost:5173")

	// Stripe config (optional – Stripe features disabled if not set)
	StripeSecretKey = requireEnv("STRIPE_SECRET_KEY", "")
	StripeWebhookSecret = requireEnv("STRIPE_WEBHOOK_SECRET", "")
	StripePriceProID = requireEnv("STRIPE_PRICE_PRO_ID", "")
	StripeSuccessURL = requireEnv("STRIPE_SUCCESS_URL", frontendOrigin+"/app/profile?upgrade=success")
	StripeCancelURL = requireEnv("STRIPE_CANCEL_URL", frontendOrigin+"/app/profile?upgrade=cancelled")

	if dbURL == "" {
		fmt.Println("FATAL: DATABASE_URL is required")
		os.Exit(1)
	}
	if supabaseURL == "" || supabaseAnonKey == "" {
		fmt.Println("FATAL: SUPABASE_URL and SUPABASE_ANON_KEY are required")
		os.Exit(1)
	}

	fmt.Println("Starting Facturator.ai backend...")

	// ── Database ─────────────────────────────────────────
	if err := InitDB(dbURL); err != nil {
		fmt.Printf("Database initialization failed: %v\n", err)
		os.Exit(1)
	}
	defer DB.Close()

	// ── Supabase Storage ─────────────────────────────────
	if supabaseServiceKey != "" {
		Storage = NewSupabaseStorage(supabaseURL, supabaseServiceKey, "receipts")
		fmt.Println("Supabase Storage configured (bucket: receipts)")
	} else {
		fmt.Println("WARNING: SUPABASE_SERVICE_ROLE_KEY not set — file uploads disabled")
	}

	// ── WebSocket Hub ────────────────────────────────────
	GlobalHub = NewHub()
	go GlobalHub.Run()

	// ── Routes ───────────────────────────────────────────
	mux := http.NewServeMux()

	// Health check
	mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("pong"))
	})

	// WebSocket (authenticated via query param, validated inside)
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		ServeWs(GlobalHub, w, r)
	})

	// Stripe webhook (no auth middleware — uses Stripe signature verification)
	mux.HandleFunc("/webhook/stripe", handleStripeWebhook)

	// API routes (all require auth)
	apiHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		method := r.Method

		switch {
		// Receipts
		case path == "/api/receipts" && method == "GET":
			handleGetReceipts(w, r)
		case path == "/api/receipts" && method == "POST":
			handleUploadReceipt(w, r)

		// Transactions
		case path == "/api/transactions" && method == "GET":
			handleGetTransactions(w, r)
		case path == "/api/transactions" && method == "DELETE":
			handleDeleteTransactions(w, r)
		case path == "/api/transactions/reassign" && method == "PUT":
			handleReassignTransactions(w, r)
		case strings.HasPrefix(path, "/api/transactions/") && method == "PUT":
			handleUpdateTransaction(w, r)

		// Budgets
		case path == "/api/budgets" && method == "GET":
			handleGetBudgets(w, r)
		case path == "/api/budgets" && method == "POST":
			handleCreateBudget(w, r)
		case strings.HasPrefix(path, "/api/budgets/") && method == "PUT":
			handleUpdateBudget(w, r)
		case strings.HasPrefix(path, "/api/budgets/") && method == "DELETE":
			handleDeleteBudget(w, r)

		// Categories
		case path == "/api/categories" && method == "GET":
			handleGetCategories(w, r)
		case path == "/api/categories" && method == "POST":
			handleCreateCategory(w, r)

		// Profile
		case path == "/api/profile" && method == "GET":
			handleGetProfile(w, r)
		case path == "/api/profile" && method == "PUT":
			handleUpdateProfile(w, r)

		// Subscription
		case path == "/api/subscription" && method == "GET":
			handleGetSubscription(w, r)
		case path == "/api/subscription/checkout" && method == "POST":
			handleCreateCheckoutSession(w, r)

		default:
			http.NotFound(w, r)
		}
	})

	authMiddleware := AuthMiddleware(supabaseURL, supabaseAnonKey)
	mux.Handle("/api/", authMiddleware(apiHandler))

	// ── Start Server ─────────────────────────────────────
	allowedOrigins := []string{frontendOrigin}
	serverAddr := ":" + port
	fmt.Printf("Server listening on port %s (CORS: %s)\n", port, frontendOrigin)

	err := http.ListenAndServe(serverAddr, CORSMiddleware(mux, allowedOrigins))
	if err != nil {
		fmt.Printf("Failed to start server: %v\n", err)
		os.Exit(1)
	}
}

func requireEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}
