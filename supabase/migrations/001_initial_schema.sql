-- ============================================================================
-- Facturator.ai — Supabase Migration 001: Initial Schema
-- ============================================================================
-- Run this in the Supabase SQL Editor (Dashboard → SQL Editor → New Query)
-- ============================================================================

-- ─────────────────────────────────────────────────────────
-- 1. ENUM TYPES
-- ─────────────────────────────────────────────────────────

-- receipt_status may already exist in Supabase; CREATE IF NOT EXISTS for enums
-- requires a DO block.
DO $$ BEGIN
    CREATE TYPE receipt_status AS ENUM ('pending', 'processing', 'done', 'error');
EXCEPTION
    WHEN duplicate_object THEN NULL;
END $$;

DO $$ BEGIN
    CREATE TYPE subscription_plan AS ENUM ('free', 'pro', 'enterprise');
EXCEPTION
    WHEN duplicate_object THEN NULL;
END $$;

DO $$ BEGIN
    CREATE TYPE subscription_status AS ENUM ('active', 'trialing', 'past_due', 'cancelled', 'incomplete');
EXCEPTION
    WHEN duplicate_object THEN NULL;
END $$;


-- ─────────────────────────────────────────────────────────
-- 2. TABLES
-- ─────────────────────────────────────────────────────────

-- ── user_profiles ───────────────────────────────────────
CREATE TABLE IF NOT EXISTS public.user_profiles (
    id          UUID PRIMARY KEY REFERENCES auth.users(id) ON DELETE CASCADE,
    display_name TEXT,
    avatar_url  TEXT,
    currency    TEXT NOT NULL DEFAULT 'EUR',
    locale      TEXT NOT NULL DEFAULT 'es-ES',
    monthly_budget_goal NUMERIC(12,2),
    onboarding_completed BOOLEAN NOT NULL DEFAULT FALSE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON TABLE public.user_profiles IS 'Extended user profile data beyond Supabase auth.users';

-- ── categories ──────────────────────────────────────────
CREATE TABLE IF NOT EXISTS public.categories (
    id          UUID DEFAULT gen_random_uuid() PRIMARY KEY,
    user_id     UUID REFERENCES auth.users(id) ON DELETE CASCADE,  -- NULL = global/default
    slug        TEXT NOT NULL,
    label       TEXT NOT NULL,
    color       TEXT NOT NULL DEFAULT '#888888',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON TABLE public.categories IS 'Expense categories. Rows with user_id=NULL are global defaults.';

-- Unique constraint: one slug per user (or one global per slug)
CREATE UNIQUE INDEX IF NOT EXISTS idx_categories_user_slug
    ON public.categories (COALESCE(user_id, '00000000-0000-0000-0000-000000000000'::UUID), slug);

-- ── receipts ────────────────────────────────────────────
-- This table may already exist from the Supabase dashboard. We use IF NOT EXISTS.
CREATE TABLE IF NOT EXISTS public.receipts (
    id          UUID DEFAULT gen_random_uuid() PRIMARY KEY,
    user_id     UUID NOT NULL REFERENCES auth.users(id) ON DELETE CASCADE,
    image_url   TEXT NOT NULL,
    storage_path TEXT,              -- Supabase Storage object path
    raw_text    TEXT NOT NULL DEFAULT '',
    status      receipt_status NOT NULL DEFAULT 'pending',
    file_size   INTEGER,            -- bytes
    mime_type   TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ── transactions ────────────────────────────────────────
CREATE TABLE IF NOT EXISTS public.transactions (
    id          UUID DEFAULT gen_random_uuid() PRIMARY KEY,
    receipt_id  UUID REFERENCES public.receipts(id) ON DELETE SET NULL,
    user_id     UUID NOT NULL REFERENCES auth.users(id) ON DELETE CASCADE,
    amount      NUMERIC(12,2) NOT NULL,
    merchant    TEXT NOT NULL,
    category    TEXT NOT NULL,        -- slug referencing categories.slug
    description TEXT,                 -- optional notes
    date        DATE NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ── budgets ─────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS public.budgets (
    id            UUID DEFAULT gen_random_uuid() PRIMARY KEY,
    user_id       UUID NOT NULL REFERENCES auth.users(id) ON DELETE CASCADE,
    category      TEXT NOT NULL,      -- slug referencing categories.slug
    limit_amount  NUMERIC(12,2) NOT NULL,
    month         DATE NOT NULL,      -- first day of the month, e.g. 2026-06-01
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One budget per user per category per month
CREATE UNIQUE INDEX IF NOT EXISTS idx_budgets_user_category_month
    ON public.budgets (user_id, category, month);

-- ── subscriptions ───────────────────────────────────────
CREATE TABLE IF NOT EXISTS public.subscriptions (
    id                      UUID DEFAULT gen_random_uuid() PRIMARY KEY,
    user_id                 UUID NOT NULL REFERENCES auth.users(id) ON DELETE CASCADE,
    plan                    subscription_plan NOT NULL DEFAULT 'free',
    status                  subscription_status NOT NULL DEFAULT 'active',
    stripe_customer_id      TEXT,
    stripe_subscription_id  TEXT,
    stripe_price_id         TEXT,
    current_period_start    TIMESTAMPTZ,
    current_period_end      TIMESTAMPTZ,
    cancel_at_period_end    BOOLEAN NOT NULL DEFAULT FALSE,
    receipt_limit           INTEGER NOT NULL DEFAULT 10,  -- monthly limit for free tier
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(user_id)
);

COMMENT ON TABLE public.subscriptions IS 'User subscription/plan info, integrated with Stripe.';


-- ─────────────────────────────────────────────────────────
-- 3. INDEXES
-- ─────────────────────────────────────────────────────────

CREATE INDEX IF NOT EXISTS idx_receipts_user      ON public.receipts(user_id);
CREATE INDEX IF NOT EXISTS idx_receipts_status     ON public.receipts(status);
CREATE INDEX IF NOT EXISTS idx_transactions_user   ON public.transactions(user_id);
CREATE INDEX IF NOT EXISTS idx_transactions_date   ON public.transactions(date DESC);
CREATE INDEX IF NOT EXISTS idx_transactions_receipt ON public.transactions(receipt_id);
CREATE INDEX IF NOT EXISTS idx_budgets_user        ON public.budgets(user_id);
CREATE INDEX IF NOT EXISTS idx_categories_user     ON public.categories(user_id);
CREATE INDEX IF NOT EXISTS idx_subscriptions_stripe ON public.subscriptions(stripe_customer_id);


-- ─────────────────────────────────────────────────────────
-- 4. UPDATED_AT TRIGGER
-- ─────────────────────────────────────────────────────────

CREATE OR REPLACE FUNCTION public.set_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- Apply to all tables with updated_at
DO $$ 
DECLARE
    tbl TEXT;
BEGIN
    FOREACH tbl IN ARRAY ARRAY['user_profiles', 'receipts', 'transactions', 'budgets', 'subscriptions']
    LOOP
        EXECUTE format(
            'DROP TRIGGER IF EXISTS trg_set_updated_at ON public.%I; 
             CREATE TRIGGER trg_set_updated_at BEFORE UPDATE ON public.%I 
             FOR EACH ROW EXECUTE FUNCTION public.set_updated_at();',
            tbl, tbl
        );
    END LOOP;
END $$;


-- ─────────────────────────────────────────────────────────
-- 5. AUTO-CREATE PROFILE + FREE SUBSCRIPTION ON SIGNUP
-- ─────────────────────────────────────────────────────────

CREATE OR REPLACE FUNCTION public.handle_new_user()
RETURNS TRIGGER AS $$
BEGIN
    INSERT INTO public.user_profiles (id, display_name, avatar_url)
    VALUES (
        NEW.id,
        COALESCE(NEW.raw_user_meta_data->>'full_name', NEW.raw_user_meta_data->>'name', split_part(NEW.email, '@', 1)),
        NEW.raw_user_meta_data->>'avatar_url'
    );

    INSERT INTO public.subscriptions (user_id, plan, status, receipt_limit)
    VALUES (NEW.id, 'free', 'active', 10);

    RETURN NEW;
END;
$$ LANGUAGE plpgsql SECURITY DEFINER;

-- Trigger on auth.users insert
DROP TRIGGER IF EXISTS on_auth_user_created ON auth.users;
CREATE TRIGGER on_auth_user_created
    AFTER INSERT ON auth.users
    FOR EACH ROW EXECUTE FUNCTION public.handle_new_user();


-- ─────────────────────────────────────────────────────────
-- 6. ROW LEVEL SECURITY (RLS)
-- ─────────────────────────────────────────────────────────

-- ── user_profiles ───────────────────────────────────────
ALTER TABLE public.user_profiles ENABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS "Users can view own profile" ON public.user_profiles;
CREATE POLICY "Users can view own profile"
    ON public.user_profiles FOR SELECT
    USING (auth.uid() = id);

DROP POLICY IF EXISTS "Users can update own profile" ON public.user_profiles;
CREATE POLICY "Users can update own profile"
    ON public.user_profiles FOR UPDATE
    USING (auth.uid() = id)
    WITH CHECK (auth.uid() = id);

-- ── categories ──────────────────────────────────────────
ALTER TABLE public.categories ENABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS "Anyone can view global categories" ON public.categories;
CREATE POLICY "Anyone can view global categories"
    ON public.categories FOR SELECT
    USING (user_id IS NULL OR auth.uid() = user_id);

DROP POLICY IF EXISTS "Users can insert own categories" ON public.categories;
CREATE POLICY "Users can insert own categories"
    ON public.categories FOR INSERT
    WITH CHECK (auth.uid() = user_id);

DROP POLICY IF EXISTS "Users can delete own categories" ON public.categories;
CREATE POLICY "Users can delete own categories"
    ON public.categories FOR DELETE
    USING (auth.uid() = user_id AND user_id IS NOT NULL);

-- ── receipts ────────────────────────────────────────────
ALTER TABLE public.receipts ENABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS "Users can view own receipts" ON public.receipts;
CREATE POLICY "Users can view own receipts"
    ON public.receipts FOR SELECT
    USING (auth.uid() = user_id);

DROP POLICY IF EXISTS "Users can insert own receipts" ON public.receipts;
CREATE POLICY "Users can insert own receipts"
    ON public.receipts FOR INSERT
    WITH CHECK (auth.uid() = user_id);

DROP POLICY IF EXISTS "Users can update own receipts" ON public.receipts;
CREATE POLICY "Users can update own receipts"
    ON public.receipts FOR UPDATE
    USING (auth.uid() = user_id)
    WITH CHECK (auth.uid() = user_id);

-- ── transactions ────────────────────────────────────────
ALTER TABLE public.transactions ENABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS "Users can view own transactions" ON public.transactions;
CREATE POLICY "Users can view own transactions"
    ON public.transactions FOR SELECT
    USING (auth.uid() = user_id);

DROP POLICY IF EXISTS "Users can insert own transactions" ON public.transactions;
CREATE POLICY "Users can insert own transactions"
    ON public.transactions FOR INSERT
    WITH CHECK (auth.uid() = user_id);

DROP POLICY IF EXISTS "Users can update own transactions" ON public.transactions;
CREATE POLICY "Users can update own transactions"
    ON public.transactions FOR UPDATE
    USING (auth.uid() = user_id)
    WITH CHECK (auth.uid() = user_id);

DROP POLICY IF EXISTS "Users can delete own transactions" ON public.transactions;
CREATE POLICY "Users can delete own transactions"
    ON public.transactions FOR DELETE
    USING (auth.uid() = user_id);

-- ── budgets ─────────────────────────────────────────────
ALTER TABLE public.budgets ENABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS "Users can view own budgets" ON public.budgets;
CREATE POLICY "Users can view own budgets"
    ON public.budgets FOR SELECT
    USING (auth.uid() = user_id);

DROP POLICY IF EXISTS "Users can insert own budgets" ON public.budgets;
CREATE POLICY "Users can insert own budgets"
    ON public.budgets FOR INSERT
    WITH CHECK (auth.uid() = user_id);

DROP POLICY IF EXISTS "Users can update own budgets" ON public.budgets;
CREATE POLICY "Users can update own budgets"
    ON public.budgets FOR UPDATE
    USING (auth.uid() = user_id)
    WITH CHECK (auth.uid() = user_id);

DROP POLICY IF EXISTS "Users can delete own budgets" ON public.budgets;
CREATE POLICY "Users can delete own budgets"
    ON public.budgets FOR DELETE
    USING (auth.uid() = user_id);

-- ── subscriptions ───────────────────────────────────────
ALTER TABLE public.subscriptions ENABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS "Users can view own subscription" ON public.subscriptions;
CREATE POLICY "Users can view own subscription"
    ON public.subscriptions FOR SELECT
    USING (auth.uid() = user_id);

-- Only the backend (service_role) can modify subscriptions via Stripe webhooks.
-- No INSERT/UPDATE/DELETE policies for anon/authenticated users.


-- ─────────────────────────────────────────────────────────
-- 7. SEED DEFAULT CATEGORIES
-- ─────────────────────────────────────────────────────────

INSERT INTO public.categories (user_id, slug, label, color) VALUES
    (NULL, 'alimentacion', 'alimentación', '#4ade80'),
    (NULL, 'transporte',   'transporte',   '#22d3ee'),
    (NULL, 'ropa',         'ropa',         '#f87171'),
    (NULL, 'ocio',         'ocio',         '#f59e0b'),
    (NULL, 'salud',        'salud',        '#60a5fa'),
    (NULL, 'hogar',        'hogar',        '#a78bfa'),
    (NULL, 'suscripciones','suscripciones','#ff3e00'),
    (NULL, 'otros',        'otros',        '#6b7280')
ON CONFLICT DO NOTHING;


-- ─────────────────────────────────────────────────────────
-- 8. SUPABASE STORAGE BUCKET FOR RECEIPTS
-- ─────────────────────────────────────────────────────────

-- Create the bucket (must be done via Supabase dashboard or the API).
-- Here we document the expected configuration:
--
-- Bucket name: receipts
-- Public:      false (private, requires auth)
-- File size:   10MB max
-- Allowed MIME: image/jpeg, image/png, image/webp, application/pdf
--
-- Storage policies (set in Dashboard → Storage → receipts → Policies):
--
--   SELECT: auth.uid() = (storage.foldername(name))[1]::uuid
--   INSERT: auth.uid() = (storage.foldername(name))[1]::uuid
--   DELETE: auth.uid() = (storage.foldername(name))[1]::uuid
--
-- File path convention: {user_id}/{receipt_id}.{ext}
-- Example: 550e8400-e29b-41d4-a716-446655440000/receipt_abc123.jpg
