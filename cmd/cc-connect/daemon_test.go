package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/chenhg5/cc-connect/daemon"
)

func TestParseDaemonInstallArgs_ConfigSetsWorkDir(t *testing.T) {
	cfg, force, err := parseDaemonInstallArgs([]string{"--config", "/tmp/example/config.toml"})
	if err != nil {
		t.Fatalf("parseDaemonInstallArgs returned error: %v", err)
	}
	if force {
		t.Fatalf("force = true, want false")
	}

	want := filepath.Clean("/tmp/example")
	if cfg.WorkDir != want {
		t.Fatalf("cfg.WorkDir = %q, want %q", cfg.WorkDir, want)
	}
}

func TestParseDaemonInstallArgs_ConfigEqualsFormSetsWorkDir(t *testing.T) {
	cfg, _, err := parseDaemonInstallArgs([]string{"--config=/tmp/example/config.toml"})
	if err != nil {
		t.Fatalf("parseDaemonInstallArgs returned error: %v", err)
	}

	want := filepath.Clean("/tmp/example")
	if cfg.WorkDir != want {
		t.Fatalf("cfg.WorkDir = %q, want %q", cfg.WorkDir, want)
	}
}

func TestParseDaemonInstallArgs_NoCaptureSecretsFlag(t *testing.T) {
	os.Unsetenv("CC_DAEMON_NO_CAPTURE_SECRETS")

	cfg, _, err := parseDaemonInstallArgs([]string{"--no-capture-secrets"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !cfg.NoCaptureSecrets {
		t.Fatal("flag should set NoCaptureSecrets=true")
	}

	cfg2, _, err := parseDaemonInstallArgs(nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg2.NoCaptureSecrets {
		t.Fatal("default must be false when flag and env are unset")
	}
}

func TestParseDaemonInstallArgs_NoCaptureSecretsEnv(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE", "yes", "on"} {
		t.Run("truthy="+v, func(t *testing.T) {
			t.Setenv("CC_DAEMON_NO_CAPTURE_SECRETS", v)
			cfg, _, err := parseDaemonInstallArgs(nil)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if !cfg.NoCaptureSecrets {
				t.Fatalf("env=%q should opt out", v)
			}
		})
	}
	for _, v := range []string{"0", "false", "", "no", "off"} {
		t.Run("falsy="+v, func(t *testing.T) {
			t.Setenv("CC_DAEMON_NO_CAPTURE_SECRETS", v)
			cfg, _, err := parseDaemonInstallArgs(nil)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if cfg.NoCaptureSecrets {
				t.Fatalf("env=%q should NOT opt out", v)
			}
		})
	}
}

func TestParseDaemonInstallArgs_NoCaptureSecretsFlagAndEnvCombine(t *testing.T) {
	// OR semantics: env=truthy + flag=present → still true.
	t.Setenv("CC_DAEMON_NO_CAPTURE_SECRETS", "1")
	cfg, _, err := parseDaemonInstallArgs([]string{"--no-capture-secrets", "--force"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !cfg.NoCaptureSecrets {
		t.Fatal("flag+env both should leave NoCaptureSecrets=true")
	}
	// env=truthy without flag → still true.
	cfg2, _, err := parseDaemonInstallArgs([]string{"--force"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !cfg2.NoCaptureSecrets {
		t.Fatal("env=1 alone should opt out")
	}
}

func TestParseDaemonInstallArgs_WorkDirOverridesConfig(t *testing.T) {
	// Use native paths: a hardcoded POSIX path is not comparable to what
	// parseDaemonInstallArgs stores (it keeps --work-dir verbatim but routes
	// --config through filepath.Dir), so the assertion would only hold on unix.
	configPath := filepath.Join(t.TempDir(), "config.toml")
	overrideDir := t.TempDir()
	if filepath.Dir(configPath) == overrideDir {
		t.Fatal("test setup: override dir must differ from the config dir")
	}

	cfg, force, err := parseDaemonInstallArgs([]string{
		"--config", configPath,
		"--work-dir", overrideDir,
		"--force",
	})
	if err != nil {
		t.Fatalf("parseDaemonInstallArgs returned error: %v", err)
	}
	if !force {
		t.Fatalf("force = false, want true")
	}

	// Last flag wins: both write cfg.WorkDir, so a --work-dir that follows
	// --config overrides the directory implied by the config path.
	if cfg.WorkDir != overrideDir {
		t.Fatalf("cfg.WorkDir = %q, want %q", cfg.WorkDir, overrideDir)
	}
}

func TestWindowsDaemonInstallResolvesCustomConfig(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows daemon supports custom config names")
	}
	workDir := t.TempDir()
	t.Chdir(workDir)
	const filename = "custom config.toml"
	configPath := filepath.Join(workDir, filename)
	if err := os.WriteFile(configPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"--config", filename}, {"--config=" + filename}, {"-config=" + filename}} {
		cfg, _, err := parseDaemonInstallArgs(args)
		if err != nil {
			t.Fatal(err)
		}
		resolved, err := resolveDaemonInstallConfig(&cfg)
		if err != nil {
			t.Fatal(err)
		}
		if resolved != configPath || cfg.ConfigPath != configPath {
			t.Fatalf("resolved path = %q, task config path = %q, want %q", resolved, cfg.ConfigPath, configPath)
		}
		if cfg.WorkDir != workDir {
			t.Fatalf("task working directory = %q, want %q", cfg.WorkDir, workDir)
		}
	}
	if err := os.WriteFile("config.toml", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := parseDaemonInstallArgs([]string{"--config", "missing.toml"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolveDaemonInstallConfig(&cfg); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing explicit config error = %v, want file not found", err)
	}
}

func TestWindowsConfigDiscoveryPrefersLocalThenWindowsHome(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows config discovery")
	}
	t.Chdir(t.TempDir())
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	dataDir := filepath.Join(home, ".cc-connect")
	if err := os.Mkdir(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(dataDir, "config.toml")
	if err := os.WriteFile(legacyPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := resolveConfigPath(""); got != legacyPath {
		t.Fatalf("legacy fallback = %q, want %q", got, legacyPath)
	}
	windowsPath := filepath.Join(dataDir, "config.windows.toml")
	if err := os.WriteFile(windowsPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := resolveConfigPath(""); got != windowsPath {
		t.Fatalf("home config = %q, want %q", got, windowsPath)
	}
	var cfg daemon.Config
	if got, err := resolveDaemonInstallConfig(&cfg); err != nil || got != windowsPath || cfg.WorkDir != dataDir {
		t.Fatalf("daemon config = %q, work dir = %q, err = %v, want %q, %q", got, cfg.WorkDir, err, windowsPath, dataDir)
	}
	if err := os.WriteFile("config.toml", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := resolveConfigPath(""); got != "config.toml" {
		t.Fatalf("local config = %q, want config.toml", got)
	}
	if got := resolveConfigPath("explicit.toml"); got != "explicit.toml" {
		t.Fatalf("explicit config = %q, want explicit.toml", got)
	}
}
