package config

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

type FileProvider struct{}

func (FileProvider) Load(ctx context.Context, bootstrap BootstrapConfig) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	filename := "application-" + bootstrap.AppEnv + ".yaml"
	path := filepath.Join(bootstrap.ConfigDir, filename)
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return content, nil
}
