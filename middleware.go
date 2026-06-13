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

func CORSMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" || strings.HasPrefix(origin, "http://localhost:") || strings.HasPrefix(origin, "http://127.0.0.1:") {
			w.Header().Set("Access-Control-Allow-Origin", origin)
		} else {
			w.Header().Set("Access-Control-Allow-Origin", "http://localhost:5173")
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

func AuthMiddleware(supabaseURL string, supabaseAnonKey string, devMode bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/ws" {
				next.ServeHTTP(w, r)
				return
			}

			authHeader := r.Header.Get("Authorization")
			var token string

			if strings.HasPrefix(authHeader, "Bearer ") {
				token = strings.TrimPrefix(authHeader, "Bearer ")
			}

			if token == "" {
				if devMode {
					ctx := context.WithValue(r.Context(), UserIDKey, "user_9f2b7d6a")
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
				http.Error(w, "Unauthorized: Authorization token is required", http.StatusUnauthorized)
				return
			}

			userID, err := verifySupabaseToken(supabaseURL, supabaseAnonKey, token)
			if err != nil {
				if devMode {
					ctx := context.WithValue(r.Context(), UserIDKey, "user_9f2b7d6a")
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
				http.Error(w, fmt.Sprintf("Unauthorized: invalid token: %v", err), http.StatusUnauthorized)
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
