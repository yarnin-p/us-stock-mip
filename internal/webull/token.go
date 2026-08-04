package webull

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func ReadTokenFile(path string) (AccessToken, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return AccessToken{}, nil
		}
		return AccessToken{}, fmt.Errorf("reading Webull token file: %w", err)
	}
	lines := strings.Split(string(content), "\n")
	if len(lines) < 3 {
		return AccessToken{}, errors.New("invalid Webull token file")
	}
	expires, err := strconv.ParseInt(strings.TrimSpace(lines[1]), 10, 64)
	if err != nil {
		return AccessToken{}, errors.New("invalid Webull token expiry")
	}
	return AccessToken{
		Token: strings.TrimSpace(lines[0]), Expires: expires,
		Status: strings.TrimSpace(lines[2]),
	}, nil
}

func WriteTokenFile(path string, token AccessToken) error {
	if strings.TrimSpace(path) == "" || strings.TrimSpace(token.Token) == "" ||
		token.Expires <= 0 || token.Status != "NORMAL" {
		return errors.New("valid token and token-file path are required")
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("creating Webull token directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".token-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temporary Webull token file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("securing temporary Webull token file: %w", err)
	}
	_, writeErr := fmt.Fprintf(
		temporary, "%s\n%d\n%s\n", token.Token, token.Expires, token.Status,
	)
	closeErr := temporary.Close()
	if writeErr != nil {
		return fmt.Errorf("writing Webull token file: %w", writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("closing Webull token file: %w", closeErr)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("installing Webull token file: %w", err)
	}
	return nil
}

func (client *Client) EnsureToken(
	ctx context.Context,
	current string,
	checkInterval, maximumWait time.Duration,
) (AccessToken, error) {
	token, err := client.CreateToken(ctx, current)
	if err != nil {
		return AccessToken{}, err
	}
	if token.Status == "NORMAL" {
		return token, nil
	}
	if token.Status != "PENDING" {
		return AccessToken{}, fmt.Errorf("webull token status is %s", token.Status)
	}
	if checkInterval <= 0 || maximumWait <= 0 {
		return AccessToken{}, errors.New("token polling durations must be positive")
	}
	timer := time.NewTimer(maximumWait)
	defer timer.Stop()
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return AccessToken{}, context.Cause(ctx)
		case <-timer.C:
			return AccessToken{}, errors.New("timed out waiting for Webull 2FA approval")
		case <-ticker.C:
			checked, err := client.CheckToken(ctx, token.Token)
			if err != nil {
				return AccessToken{}, err
			}
			switch checked.Status {
			case "NORMAL":
				return checked, nil
			case "PENDING":
			default:
				return AccessToken{}, fmt.Errorf(
					"webull token status is %s", checked.Status,
				)
			}
		}
	}
}
