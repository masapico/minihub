package authstorage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type storageMeta struct {
	Type string `json:"type"`
}

func Open(dataDir, storageType string) (Storage, error) {
	if dataDir == "" {
		return nil, errors.New("data directory is required")
	}
	if storageType != "file" && storageType != "sqlite" {
		return nil, fmt.Errorf("unsupported storage type: %s", storageType)
	}

	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}

	// Validate or create storage.json
	metaPath := filepath.Join(dataDir, "storage.json")
	if data, err := os.ReadFile(metaPath); err == nil {
		var meta storageMeta
		if err := json.Unmarshal(data, &meta); err != nil {
			return nil, fmt.Errorf("read storage.json: %w", err)
		}
		if meta.Type != storageType {
			return nil, fmt.Errorf("storage mismatch: config specifies %s but directory was initialized with %s", storageType, meta.Type)
		}
	} else if errors.Is(err, os.ErrNotExist) {
		meta := storageMeta{Type: storageType}
		bytes, _ := json.MarshalIndent(meta, "", "  ")
		if err := os.WriteFile(metaPath, bytes, 0o600); err != nil {
			return nil, fmt.Errorf("write storage.json: %w", err)
		}
	} else {
		return nil, err
	}

	switch storageType {
	case "file":
		return NewFileStorage(dataDir)
	case "sqlite":
		dbPath := filepath.Join(dataDir, "miniauth.db")
		return NewSQLiteStorage(dbPath)
	default:
		return nil, fmt.Errorf("unsupported storage type: %s", storageType)
	}
}

