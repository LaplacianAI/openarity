package main

import (
	"log/slog"

	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
	"github.com/LaplacianAI/openarity/apps/brain/internal/objects"
	"github.com/LaplacianAI/openarity/apps/brain/internal/objects/filesystem"
	"github.com/LaplacianAI/openarity/apps/brain/internal/objects/inmemory"
	"github.com/LaplacianAI/openarity/apps/brain/internal/objects/s3"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
)

func newObjectStore(cfg *config.Config, logger *slog.Logger) (objects.Store, error) {
	switch cfg.ObjectsBackend {
	case config.ObjectsBackendS3:
		return s3.New(s3.Config{
			Endpoint:  cfg.ObjectsEndpoint,
			Region:    cfg.ObjectsRegion,
			Bucket:    cfg.ObjectsBucket,
			AccessKey: cfg.ObjectsAccessKey,
			SecretKey: cfg.ObjectsSecretKey,
		})

	case config.ObjectsBackendFilesystem:
		return filesystem.New(cfg.ObjectsPath)

	case config.ObjectsBackendMemory:
		logger.Warn("OBJECTS_BACKEND=memory: attachments are held in this " +
			"process and lost on restart, with no error at either end.")
		return inmemory.New(), nil
	}

	return inmemory.New(), nil
}

func newAttachmentStore(
	cfg *config.Config, secretStore secrets.Store, logger *slog.Logger,
) (*objects.Encrypted, error) {
	inner, err := newObjectStore(cfg, logger)
	if err != nil {
		return nil, err
	}

	keys, err := secrets.NewDataKeys(secretStore, objects.KeySize)
	if err != nil {
		return nil, err
	}

	return objects.NewEncrypted(inner, keys)
}
