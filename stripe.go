package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/stripe/stripe-go/v85"
	"github.com/stripe/stripe-go/v85/checkout/session"
	"github.com/stripe/stripe-go/v85/customer"
	"github.com/stripe/stripe-go/v85/webhook"
)

// ─── Config ──────────────────────────────────────────────

var (
	StripeSecretKey     string
	StripeWebhookSecret string
	StripePriceProID    string // Price ID for Pro plan
	StripeSuccessURL    string
	StripeCancelURL     string
)

// PlanConfig maps plan names to receipt limits.
var PlanConfig = map[string]int{
	"free":       10,
	"pro":        1000,
	"enterprise": -1, // unlimited
}

// ─── Handlers ────────────────────────────────────────────

// handleCreateCheckoutSession creates a Stripe Checkout session for upgrading.
func handleCreateCheckoutSession(w http.ResponseWriter, r *http.Request) {
	userID := GetUserID(r)

	if StripeSecretKey == "" {
		writeError(w, http.StatusServiceUnavailable, "Stripe is not configured")
		return
	}

	stripe.Key = StripeSecretKey

	// Get or create Stripe customer
	sub, err := GetSubscription(userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Error fetching subscription")
		return
	}

	var stripeCustomerID string
	if sub != nil && sub.StripeCustomerID != nil {
		stripeCustomerID = *sub.StripeCustomerID
	} else {
		// Create new Stripe customer
		profile, _ := GetUserProfile(userID)
		displayName := "Facturator User"
		if profile != nil && profile.DisplayName != nil {
			displayName = *profile.DisplayName
		}

		params := &stripe.CustomerParams{
			Name: stripe.String(displayName),
			Metadata: map[string]string{
				"supabase_user_id": userID,
			},
		}
		cust, err := customer.New(params)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Error creating Stripe customer")
			return
		}
		stripeCustomerID = cust.ID
		_ = UpsertStripeCustomer(userID, stripeCustomerID)
	}

	// Create checkout session
	params := &stripe.CheckoutSessionParams{
		Customer: stripe.String(stripeCustomerID),
		Mode:     stripe.String(string(stripe.CheckoutSessionModeSubscription)),
		LineItems: []*stripe.CheckoutSessionLineItemParams{
			{
				Price:    stripe.String(StripePriceProID),
				Quantity: stripe.Int64(1),
			},
		},
		SuccessURL: stripe.String(StripeSuccessURL),
		CancelURL:  stripe.String(StripeCancelURL),
		Metadata: map[string]string{
			"supabase_user_id": userID,
		},
	}

	sess, err := session.New(params)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("Error creating checkout session: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"checkoutUrl": sess.URL,
		"sessionId":   sess.ID,
	})
}

// handleGetSubscription returns the current user's subscription.
func handleGetSubscription(w http.ResponseWriter, r *http.Request) {
	userID := GetUserID(r)

	sub, err := GetSubscription(userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Error fetching subscription")
		return
	}

	if sub == nil {
		// Return default free subscription
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"plan":         "free",
			"status":       "active",
			"receiptLimit": PlanConfig["free"],
		})
		return
	}

	writeJSON(w, http.StatusOK, sub)
}

// handleStripeWebhook processes Stripe webhook events.
func handleStripeWebhook(w http.ResponseWriter, r *http.Request) {
	if StripeWebhookSecret == "" {
		http.Error(w, "Webhook not configured", http.StatusServiceUnavailable)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 65536))
	if err != nil {
		http.Error(w, "Error reading body", http.StatusBadRequest)
		return
	}

	event, err := webhook.ConstructEvent(body, r.Header.Get("Stripe-Signature"), StripeWebhookSecret)
	if err != nil {
		fmt.Printf("Stripe webhook signature verification failed: %v\n", err)
		http.Error(w, "Invalid signature", http.StatusBadRequest)
		return
	}

	fmt.Printf("Stripe webhook: %s\n", event.Type)

	switch event.Type {
	case "checkout.session.completed":
		handleCheckoutCompleted(event)

	case "customer.subscription.updated":
		handleSubscriptionUpdated(event)

	case "customer.subscription.deleted":
		handleSubscriptionDeleted(event)

	case "invoice.payment_failed":
		handlePaymentFailed(event)
	}

	w.WriteHeader(http.StatusOK)
}

// ─── Event Handlers ──────────────────────────────────────

func handleCheckoutCompleted(event stripe.Event) {
	var sess stripe.CheckoutSession
	if err := json.Unmarshal(event.Data.Raw, &sess); err != nil {
		fmt.Printf("Error parsing checkout session: %v\n", err)
		return
	}

	userID, ok := sess.Metadata["supabase_user_id"]
	if !ok {
		fmt.Println("No supabase_user_id in checkout session metadata")
		return
	}

	subID := ""
	if sess.Subscription != nil {
		subID = sess.Subscription.ID
	}

	now := time.Now()
	endOfMonth := time.Date(now.Year(), now.Month()+1, 0, 23, 59, 59, 0, time.UTC)

	err := UpdateSubscription(
		userID,
		"pro",
		"active",
		&subID,
		&StripePriceProID,
		&now,
		&endOfMonth,
		false,
		PlanConfig["pro"],
	)
	if err != nil {
		fmt.Printf("Error updating subscription for user %s: %v\n", userID, err)
	} else {
		fmt.Printf("User %s upgraded to Pro plan\n", userID)
	}
}

func handleSubscriptionUpdated(event stripe.Event) {
	var sub stripe.Subscription
	if err := json.Unmarshal(event.Data.Raw, &sub); err != nil {
		fmt.Printf("Error parsing subscription update: %v\n", err)
		return
	}

	// Find user by Stripe customer ID
	userID := findUserByStripeCustomer(sub.Customer.ID)
	if userID == "" {
		fmt.Printf("No user found for Stripe customer %s\n", sub.Customer.ID)
		return
	}

	status := string(sub.Status)
	cancelAtEnd := sub.CancelAtPeriodEnd
	periodStart := time.Unix(sub.CurrentPeriodStart, 0)
	periodEnd := time.Unix(sub.CurrentPeriodEnd, 0)

	plan := "pro"
	if status == "canceled" || status == "unpaid" {
		plan = "free"
	}

	subID := sub.ID
	err := UpdateSubscription(
		userID,
		plan,
		status,
		&subID,
		nil,
		&periodStart,
		&periodEnd,
		cancelAtEnd,
		PlanConfig[plan],
	)
	if err != nil {
		fmt.Printf("Error updating subscription for user %s: %v\n", userID, err)
	}
}

func handleSubscriptionDeleted(event stripe.Event) {
	var sub stripe.Subscription
	if err := json.Unmarshal(event.Data.Raw, &sub); err != nil {
		fmt.Printf("Error parsing subscription deletion: %v\n", err)
		return
	}

	userID := findUserByStripeCustomer(sub.Customer.ID)
	if userID == "" {
		return
	}

	err := UpdateSubscription(
		userID,
		"free",
		"cancelled",
		nil, nil, nil, nil,
		false,
		PlanConfig["free"],
	)
	if err != nil {
		fmt.Printf("Error downgrading user %s: %v\n", userID, err)
	} else {
		fmt.Printf("User %s downgraded to free plan\n", userID)
	}
}

func handlePaymentFailed(event stripe.Event) {
	var invoice stripe.Invoice
	if err := json.Unmarshal(event.Data.Raw, &invoice); err != nil {
		fmt.Printf("Error parsing invoice: %v\n", err)
		return
	}

	if invoice.Customer == nil {
		return
	}

	userID := findUserByStripeCustomer(invoice.Customer.ID)
	if userID == "" {
		return
	}

	fmt.Printf("Payment failed for user %s\n", userID)
	// Could send notification, downgrade, etc.
}

// ─── Helpers ─────────────────────────────────────────────

func findUserByStripeCustomer(stripeCustomerID string) string {
	var userID string
	err := DB.QueryRow(`
		SELECT user_id FROM subscriptions WHERE stripe_customer_id = $1
	`, stripeCustomerID).Scan(&userID)
	if err != nil {
		return ""
	}
	return userID
}
