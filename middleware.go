package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type contextKey string

const UserIDKey contextKey = "userID"

func GetUserID(r *http.Request) string {
	val := r.Context().Value(UserIDKey)
	if val == nil {
		return ""
	}
	return val.(string)
}

// CORSMiddleware handles CORS with explicit allowed origins.
func CORSMiddleware(next http.Handler, allowedOrigins []string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")

		allowed := false
		for _, o := range allowedOrigins {
			if origin == o {
				allowed = true
				break
			}
		}

		if allowed {
			w.Header().Set("Access-Control-Allow-Origin", origin)
		}

		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, apikey")
		w.Header().Set("Access-Control-Allow-Credentials", "true")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// AuthMiddleware validates Supabase JWT tokens. No dev-mode bypass.
func AuthMiddleware(supabaseURL string, supabaseAnonKey string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			var token string

			if strings.HasPrefix(authHeader, "Bearer ") {
				token = strings.TrimPrefix(authHeader, "Bearer ")
			}

			if token == "" {
				http.Error(w, `{"error":"Unauthorized: Authorization token is required"}`, http.StatusUnauthorized)
				return
			}

			userID, err := verifySupabaseToken(supabaseURL, supabaseAnonKey, token)
			if err != nil {
				http.Error(w, fmt.Sprintf(`{"error":"Unauthorized: %v"}`, err), http.StatusUnauthorized)
				return
			}

			ctx := context.WithValue(r.Context(), UserIDKey, userID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func verifySupabaseToken(supabaseURL string, supabaseAnonKey string, token string) (string, error) {
	if supabaseURL == "" {
		return "", fmt.Errorf("supabase URL is not configured")
	}

	url := fmt.Sprintf("%s/auth/v1/user", strings.TrimSuffix(supabaseURL, "/"))
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("error creating request: %w", err)
	}

	req.Header.Set("apikey", supabaseAnonKey)
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("error calling supabase auth: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("supabase returned status %d", resp.StatusCode)
	}

	var userResp struct {
		ID string `json:"id"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&userResp); err != nil {
		return "", fmt.Errorf("error parsing user response: %w", err)
	}

	if userResp.ID == "" {
		return "", fmt.Errorf("received empty user ID")
	}

	return userResp.ID, nil
}
