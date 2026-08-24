package database

import (
	"fmt"
	"log"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/mt-sense/backend-service/internal/auth"
	"github.com/mt-sense/backend-service/internal/models"
)

func Connect(dsn string) (*gorm.DB, error) {
	// PreferSimpleProtocol disables server-side prepared statements. Required against Neon's
	// pooled connection string (the "-pooler" host): PgBouncer-style transaction pooling can
	// hand a query a different backend connection than the one that cached its plan, and once
	// a migration changes a table's columns, any connection still holding the old plan fails
	// every subsequent "SELECT *" against that table with `cached plan must not change result
	// type` (SQLSTATE 0A000) — discovered while testing this migration end-to-end. Simple
	// protocol re-plans every query, which costs a little throughput but is the standard fix
	// for gorm/pgx behind a transaction-pooling proxy.
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true}), &gorm.Config{
		Logger:  logger.Default.LogMode(logger.Warn),
		NowFunc: func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		return nil, fmt.Errorf("connecting to postgres: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("acquiring sql.DB: %w", err)
	}
	sqlDB.SetMaxOpenConns(25)
	sqlDB.SetMaxIdleConns(5)
	sqlDB.SetConnMaxLifetime(time.Hour)

	return db, nil
}

// enumTypes mirrors the CREATE TYPE statements in the user-supplied schema. Each is created
// with a guarded DO block so booting against an already-migrated database is a no-op.
var enumTypes = []struct {
	name   string
	values []string
}{
	{"user_role", []string{"employee", "executive", "admin"}},
	{"sentiment_label", []string{"pos", "neg", "neu"}},
	{"alert_severity", []string{"info", "warning", "critical"}},
	{"alert_type", []string{
		"low_satisfaction_position",
		"low_satisfaction_department",
		"low_response_rate",
		"sentiment_drop",
	}},
	{"tenure_bucket", []string{"under_1y", "1_3y", "3_5y", "5y_plus"}},
}

// Bootstrap creates the pgcrypto extension and the Postgres enum types the schema depends
// on, ahead of AutoMigrate — GORM's AutoMigrate does not create custom enum types itself,
// so the columns that reference them (via gorm:"type:...") need the type to already exist.
func Bootstrap(db *gorm.DB) error {
	if err := db.Exec(`CREATE EXTENSION IF NOT EXISTS pgcrypto`).Error; err != nil {
		return fmt.Errorf("creating pgcrypto extension: %w", err)
	}

	for _, enum := range enumTypes {
		quoted := make([]string, len(enum.values))
		for i, v := range enum.values {
			quoted[i] = fmt.Sprintf("'%s'", v)
		}
		stmt := fmt.Sprintf(`
			DO $$ BEGIN
				CREATE TYPE %s AS ENUM (%s);
			EXCEPTION WHEN duplicate_object THEN NULL;
			END $$;
		`, enum.name, joinComma(quoted))
		if err := db.Exec(stmt).Error; err != nil {
			return fmt.Errorf("creating enum type %s: %w", enum.name, err)
		}
	}

	return nil
}

func joinComma(values []string) string {
	out := ""
	for i, v := range values {
		if i > 0 {
			out += ", "
		}
		out += v
	}
	return out
}

// DemoOrgID resolves the seeded demo organization's id (slug "mt-sense"). The app is now
// multi-tenant per-request (org resolved from JWT claim after login, from join code before
// it) — this function only remains for seed.go's self-healing check, not for request-time
// org resolution.
func DemoOrgID(db *gorm.DB) (string, error) {
	var id string
	err := db.Raw(`SELECT id FROM organizations WHERE slug = 'mt-sense' LIMIT 1`).Scan(&id).Error
	if err != nil {
		return "", fmt.Errorf("resolving demo organization: %w", err)
	}
	if id == "" {
		return "", fmt.Errorf("demo organization not found — has the database been seeded?")
	}
	return id, nil
}

// dropLegacyIndexes removes indexes from a prior schema revision that AutoMigrate will not
// drop on its own (it only adds/alters, never drops). uq_users_org_email was the per-org
// composite unique index on users(org_id,email); email uniqueness is now global so that
// login can resolve org purely from email with no company picker.
func dropLegacyIndexes(db *gorm.DB) error {
	if err := db.Exec(`DROP INDEX IF EXISTS uq_users_org_email`).Error; err != nil {
		return fmt.Errorf("dropping legacy uq_users_org_email index: %w", err)
	}
	return nil
}

func Migrate(db *gorm.DB) error {
	if err := Bootstrap(db); err != nil {
		return fmt.Errorf("bootstrapping schema: %w", err)
	}
	if err := dropLegacyIndexes(db); err != nil {
		return fmt.Errorf("dropping legacy indexes: %w", err)
	}
	if err := db.AutoMigrate(models.AllModels()...); err != nil {
		return fmt.Errorf("running migrations: %w", err)
	}
	if err := backfillJoinCodes(db); err != nil {
		return fmt.Errorf("backfilling join codes: %w", err)
	}
	if err := installTriggers(db); err != nil {
		return fmt.Errorf("installing triggers: %w", err)
	}
	log.Println("database: migrations applied")
	return nil
}

// backfillJoinCodes assigns a real join code to any organization left with the '' the
// JoinCode column's migration default produced (see the field's doc comment in models.go) —
// rows that existed before this column was added. The demo org gets the fixed "DEMO01" to
// match seed.go's convention; anything else gets a freshly generated code.
func backfillJoinCodes(db *gorm.DB) error {
	var orgs []models.Organization
	if err := db.Where("join_code = ''").Find(&orgs).Error; err != nil {
		return err
	}
	for _, org := range orgs {
		code := "DEMO01"
		if org.Slug != "mt-sense" {
			generated, err := auth.JoinCode()
			if err != nil {
				return err
			}
			code = generated
		}
		if err := db.Model(&models.Organization{}).Where("id = ?", org.ID).Update("join_code", code).Error; err != nil {
			return err
		}
		log.Printf("database: backfilled join code for org %q", org.Slug)
	}
	return nil
}

// installTriggers reuses the exact set_updated_at trigger function from the supplied schema,
// applied to the four core tables it targets. Idempotent via DROP+CREATE so re-running on
// boot is safe.
func installTriggers(db *gorm.DB) error {
	const fn = `
		CREATE OR REPLACE FUNCTION set_updated_at()
		RETURNS TRIGGER AS $$
		BEGIN
			NEW.updated_at = now();
			RETURN NEW;
		END;
		$$ LANGUAGE plpgsql;
	`
	if err := db.Exec(fn).Error; err != nil {
		return fmt.Errorf("creating set_updated_at function: %w", err)
	}

	tables := []string{"organizations", "departments", "positions", "users"}
	for _, table := range tables {
		trigger := fmt.Sprintf("trg_%s_updated_at", table)
		if err := db.Exec(fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON %s`, trigger, table)).Error; err != nil {
			return fmt.Errorf("dropping trigger on %s: %w", table, err)
		}
		stmt := fmt.Sprintf(`
			CREATE TRIGGER %s
				BEFORE UPDATE ON %s
				FOR EACH ROW EXECUTE FUNCTION set_updated_at();
		`, trigger, table)
		if err := db.Exec(stmt).Error; err != nil {
			return fmt.Errorf("creating trigger on %s: %w", table, err)
		}
	}
	return nil
}
