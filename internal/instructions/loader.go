package instructions

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

type Loader struct {
	DefaultPath    string
	AdditionalPath string
}

func (l Loader) Load() (string, error) {
	base, err := os.ReadFile(l.DefaultPath)
	if err != nil {
		return "", fmt.Errorf("read default instructions %q: %w", l.DefaultPath, err)
	}
	result := strings.TrimSpace(string(base))
	if result == "" {
		return "", errors.New("default instruction file is empty")
	}

	if strings.TrimSpace(l.AdditionalPath) == "" {
		return result, nil
	}
	additional, err := os.ReadFile(l.AdditionalPath)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return "", fmt.Errorf("read additional instructions %q: %w", l.AdditionalPath, err)
	}
	if extra := strings.TrimSpace(string(additional)); extra != "" {
		result += "\n\n--- ADDITIONAL DEPLOYMENT INSTRUCTIONS ---\n\n" + extra
	}
	return result, nil
}
