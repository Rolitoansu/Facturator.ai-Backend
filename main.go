package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"
)

func main() {
	port := getEnv("PORT", "8080")
	dbURL := getEnv("DATABASE_URL", "postgres://root:mysecretpassword@localhost:5432/local")

	supabaseURL := getEnv("PUBLIC_SUPABASE_URL", "https://nxtqsfvinckkqpdcaexm.supabase.co")
	supabaseAnonKey := getEnv("PUBLIC_SUPABASE_ANON_KEY", "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJpc3MiOiJzdXBhYmFzZSIsInJlZiI6Im54dHFzZnZpbmNra3FwZGNhZXhtIiwicm9sZSI6ImFub24iLCJpYXQiOjE3ODA4MzY3ODUsImV4cCI6MjA5NjQxMjc4NX0.H1p3K6-4mhG26d1zUTJzMzuSMxb4BKKz4hTqhR6wY6M")
	devModeStr := getEnv("DEV_MODE", "true")
	devMode := devModeStr == "true"

	GeminiAPIKey = getEnv("GEMINI_API_KEY", "")

	fmt.Printf("Starting backend (dev mode: %t)...\n", devMode)

	if err := InitDB(dbURL); err != nil {
		fmt.Printf("Database initialization failed: %v\n", err)
		os.Exit(1)
	}
	defer DB.Close()

	GlobalHub = NewHub()
	go GlobalHub.Run()

	mux := http.NewServeMux()

	uploadDir := "./uploads"
	if err := os.MkdirAll(uploadDir, os.ModePerm); err != nil {
		fmt.Printf("Failed to create upload dir: %v\n", err)
	}
	fs := http.FileServer(http.Dir(uploadDir))
	mux.Handle("/uploads/", http.StripPrefix("/uploads/", fs))

	mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("pong"))
	})

	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		ServeWs(GlobalHub, w, r)
	})

	apiHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		method := r.Method

		switch {
		case path == "/api/receipts" && method == "GET":
			handleGetReceipts(w, r)
		case path == "/api/receipts" && method == "POST":
			handleUploadReceipt(w, r)

		case path == "/api/transactions" && method == "GET":
			handleGetTransactions(w, r)
		case path == "/api/transactions" && method == "DELETE":
			handleDeleteTransactions(w, r)
		case path == "/api/transactions/reassign" && method == "PUT":
			handleReassignTransactions(w, r)

		case path == "/api/budgets" && method == "GET":
			handleGetBudgets(w, r)
		case path == "/api/budgets" && method == "POST":
			handleCreateBudget(w, r)
		case strings.HasPrefix(path, "/api/budgets/") && method == "PUT":
			handleUpdateBudget(w, r)
		case strings.HasPrefix(path, "/api/budgets/") && method == "DELETE":
			handleDeleteBudget(w, r)

		case path == "/api/categories" && method == "GET":
			handleGetCategories(w, r)
		case path == "/api/categories" && method == "POST":
			handleCreateCategory(w, r)

		default:
			http.NotFound(w, r)
		}
	})

	authMiddleware := AuthMiddleware(supabaseURL, supabaseAnonKey, devMode)
	mux.Handle("/api/", authMiddleware(apiHandler))

	serverAddr := ":" + port
	fmt.Printf("Server listening on port %s\n", port)
	err := http.ListenAndServe(serverAddr, CORSMiddleware(mux))
	if err != nil {
		fmt.Printf("Failed to start server: %v\n", err)
		os.Exit(1)
	}
}

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}
