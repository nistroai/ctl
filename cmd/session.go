package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func sessionPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".nistro", "sessions.json"), nil
}

func readSessions() (map[string]string, error) {
	path, err := sessionPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return make(map[string]string), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read saved sessions: %w", err)
	}
	sessions := make(map[string]string)
	if err := json.Unmarshal(data, &sessions); err != nil {
		return nil, fmt.Errorf("decode saved sessions: %w", err)
	}
	if sessions == nil {
		sessions = make(map[string]string)
	}
	return sessions, nil
}

func loadSession(server string) (string, error) {
	sessions, err := readSessions()
	return sessions[strings.TrimRight(server, "/")], err
}

func saveSession(server, token string) error {
	sessions, err := readSessions()
	if err != nil {
		return err
	}
	server = strings.TrimRight(server, "/")
	if token == "" {
		if _, exists := sessions[server]; !exists {
			return nil
		}
		delete(sessions, server)
	} else {
		sessions[server] = token
	}
	path, err := sessionPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create session directory: %w", err)
	}
	data, err := json.Marshal(sessions)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".sessions-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
