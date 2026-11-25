package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/google/uuid"
	"go.mau.fi/whatsmeow"
	wastore "go.mau.fi/whatsmeow/store"

	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	gwa "github.com/LaplacianAI/openarity/apps/brain/internal/gateway/whatsapp"
	"github.com/LaplacianAI/openarity/apps/brain/internal/runtime"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
	"github.com/LaplacianAI/openarity/apps/brain/internal/whatsapp"
	"github.com/LaplacianAI/openarity/apps/brain/internal/worker"
)

// runWhatsApp holds the connections of the WhatsApp channels it leases: their
// messages handed to the gateway as a webhook's are, their replies and
// notices sent through the socket. Any number may run; each channel is held
// by one.
func runWhatsApp(ctx context.Context, cfg *config.Config, logger *slog.Logger, dbStore *store.Store) error {
	secretStore := openSecretStore(cfg, logger)
	if err := checkSecretStore(ctx, secretStore); err != nil {
		return err
	}
	registry, err := newRegistry()
	if err != nil {
		return err
	}
	provider, ok := registry.Get(gwa.Name)
	if !ok {
		return fmt.Errorf("the %s provider is not registered", gwa.Name)
	}
	attachments, err := newAttachmentStore(cfg, secretStore, logger)
	if err != nil {
		return err
	}

	runQueue, err := dbos.NewClient(ctx, dbos.ClientConfig{
		DatabaseURL: cfg.PostgresDSN, AppName: worker.AppName, Logger: logger,
	})
	if err != nil {
		return fmt.Errorf("connect to the run queue: %w", err)
	}
	defer func() { _ = dbos.Shutdown(runQueue, runQueueShutdown) }()

	sink := gateway.NewInbox(inbox{dbStore}, runtime.NewEnqueuer(runQueue), logger)
	// Files are read from the connection, never downloaded by URL, so the
	// ingest has no client. A notice is not kicked: this role sends its own
	// channels' notices, every two seconds.
	ingest := gateway.NewIngest(attachments, dbStore, nil, logger)
	receiver := gateway.NewReceiver(logger, dbStore, registry, sink, nil)

	waLogger := whatsapp.Logger(logger.With("component", "whatsmeow"))
	whatsapp.Present()
	devices, closeDevices, err := whatsapp.OpenDevices(ctx, cfg.PostgresDSN, waLogger)
	if err != nil {
		return err
	}
	defer func() { _ = closeDevices() }()

	holder := holderName()
	logger.Info("Holding WhatsApp channels", "holder", holder)
	accounts := whatsapp.New(whatsapp.Config{
		Holder: holder, Store: dbStore, Devices: devices,
		NewClient: func(d *wastore.Device) whatsapp.Client { return whatsmeow.NewClient(d, waLogger) },
		Provider:  provider, Receiver: receiver, Ingest: ingest, Logger: logger,
	})
	sender := gateway.NewSender(dbStore, secretStore, registry, accounts, gateway.Scope{Held: accounts.Held}, logger).
		WithFiles(attachments)
	return accounts.Run(ctx, sender.Drain)
}

// holderName names this process in the leases: its host, so a person reading
// the table can tell which container holds a channel, and enough more that
// two on one host never share it.
func holderName() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "brain"
	}
	return host + "/" + uuid.NewString()[:8]
}
