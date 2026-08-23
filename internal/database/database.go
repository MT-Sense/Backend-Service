package database

import (
	"fmt"
	"log"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/mt-sense/backend-service/internal/models"
)

func Connect(dsn string) (*gorm.DB, error) {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
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

// DefaultOrgID resolves the single seeded organization's id. The app is single-tenant for
// now (see README) — every handler is already scoped by org_id, so this is the one place a
// future multi-tenant login flow would change to resolve per-request instead.
func DefaultOrgID(db *gorm.DB) (string, error) {
	var id string
	err := db.Raw(`SELECT id FROM organizations ORDER BY created_at LIMIT 1`).Scan(&id).Error
	if err != nil {
		return "", fmt.Errorf("resolving default organization: %w", err)
	}
	if id == "" {
		return "", fmt.Errorf("no organization found — has the database been seeded?")
	}
	return id, nil
}

func Migrate(db *gorm.DB) error {
	if err := Bootstrap(db); err != nil {
		return fmt.Errorf("bootstrapping schema: %w", err)
	}
	if err := db.AutoMigrate(models.AllModels()...); err != nil {
		return fmt.Errorf("running migrations: %w", err)
	}
	if err := installTriggers(db); err != nil {
		return fmt.Errorf("installing triggers: %w", err)
	}
	log.Println("database: migrations applied")
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
