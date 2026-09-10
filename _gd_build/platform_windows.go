//go:build windows

package main

import (
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const localPort = "17654"

func dataDirectory() (string, error) {
	if override := strings.TrimSpace(os.Getenv("GD_FISCAL_SAUDE_DATA_DIR")); override != "" {
		if err := os.MkdirAll(override, 0700); err != nil {
			return "", err
		}
		return override, nil
	}
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		return "", errors.New("LOCALAPPDATA não está disponível")
	}
	dir := filepath.Join(base, "GD Solucoes", "Fiscal Saude")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return dir, nil
}

func installTarget() (string, error) {
	if os.Getenv("GD_FISCAL_SAUDE_TEST_MODE") == "1" {
		return os.Executable()
	}
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		return "", errors.New("LOCALAPPDATA não está disponível")
	}
	dir := filepath.Join(base, "Programs", "GD Fiscal Saude")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	return filepath.Join(dir, "GD-Fiscal-Saude.exe"), nil
}

func samePath(a, b string) bool {
	aa, _ := filepath.Abs(a)
	bb, _ := filepath.Abs(b)
	return strings.EqualFold(filepath.Clean(aa), filepath.Clean(bb))
}

func serverAlive() bool {
	client := &http.Client{Timeout: 450 * time.Millisecond}
	resp, err := client.Get("http://127.0.0.1:" + localPort + "/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func copyExecutable(src, dst string) error {
	in, err := os.Open(src)
	if err != nil { return err }
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil { return err }
	_, copyErr := io.Copy(out, in)
	syncErr := out.Sync()
	closeErr := out.Close()
	if copyErr != nil { _ = os.Remove(tmp); return copyErr }
	if syncErr != nil { _ = os.Remove(tmp); return syncErr }
	if closeErr != nil { _ = os.Remove(tmp); return closeErr }
	_ = os.Remove(dst)
	if err := os.Rename(tmp, dst); err != nil { _ = os.Remove(tmp); return err }
	return nil
}

func preparePlatform() (bool, error) {
	if os.Getenv("GD_FISCAL_SAUDE_TEST_MODE") == "1" { return true, nil }
	if serverAlive() {
		openBrowser("http://127.0.0.1:" + localPort + "/")
		return false, nil
	}
	current, err := os.Executable(); if err != nil { return false, err }
	target, err := installTarget(); if err != nil { return false, err }
	if samePath(current, target) { return true, nil }
	if err := copyExecutable(current, target); err != nil { return false, err }
	cmd := exec.Command(target, "--installed")
	cmd.Dir = filepath.Dir(target)
	if err := cmd.Start(); err != nil { return false, err }
	return false, nil
}

func openBrowser(url string) {
	if capture := strings.TrimSpace(os.Getenv("GD_FISCAL_SAUDE_TEST_BROWSER_CAPTURE")); capture != "" {
		_ = os.WriteFile(capture, []byte(url), 0600)
		return
	}
	_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}
