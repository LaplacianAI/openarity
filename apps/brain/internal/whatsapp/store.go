package whatsapp

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// schema is where whatsmeow keeps its tables, apart from ours: its own
// migrations own them, and goose's never touch them.
const schema = "whatsmeow"

// OpenDevices is the device store on the brain's database, in its own schema,
// through database/sql with a small pool of its own. It runs whatsmeow's
// migrations, which are its library's and not this repository's.
func OpenDevices(ctx context.Context, dsn string, log waLog.Logger) (*sqlstore.Container, func() error, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		// Not the DSN itself, which may hold a password.
		return nil, nil, fmt.Errorf("the device store's database address does not parse")
	}
	cfg.RuntimeParams["search_path"] = schema
	sqlDB := stdlib.OpenDB(*cfg)
	sqlDB.SetMaxOpenConns(4)

	devices := sqlstore.NewWithDB(sqlDB, "pgx", log)
	if err := devices.Upgrade(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, nil, fmt.Errorf("preparing the WhatsApp device store: %w", err)
	}
	return devices, func() error { return closeAll(devices, sqlDB) }, nil
}

func closeAll(devices *sqlstore.Container, sqlDB *sql.DB) error {
	if err := devices.Close(); err != nil {
		return err
	}
	return sqlDB.Close()
}

// Present is how the linked device names itself to WhatsApp and on the
// phone's list of devices: a desktop Chrome, as WhatsApp Web is.
func Present() {
	store.SetOSInfo("Chrome (Linux)", [3]uint32{0, 1, 0})
}
