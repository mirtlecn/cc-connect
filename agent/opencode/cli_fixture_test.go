package opencode

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

type opencodeCLIFixture struct {
	Command           string
	Lines             []string
	ExitCode          int
	CountPath         string
	GatePath          string
	RequireEnv        string
	ExpectedSessionID string
	Stderr            string
}

func writeOpencodeCLIFixture(t *testing.T, name string, fixture opencodeCLIFixture) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	bin := filepath.Join(t.TempDir(), name)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Link(self, bin); err != nil {
		data, err := os.ReadFile(self)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(bin, data, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	data, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin+".json", data, 0o600); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestMain(m *testing.M) {
	switch strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe") {
	case "fake-opencode", "my-ai-cli":
		os.Exit(runOpencodeCLIFixture(os.Args[0]+".json", os.Args[1:]))
	default:
		os.Exit(m.Run())
	}
}

func runOpencodeCLIFixture(configPath string, args []string) int {
	data, err := os.ReadFile(configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	var fixture opencodeCLIFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if len(args) == 0 || args[0] != fixture.Command {
		fmt.Fprintln(os.Stderr, "unexpected CLI arguments:", args)
		return 2
	}
	if fixture.Command == "session" {
		if len(args) != 3 || args[1] != "delete" || (fixture.ExpectedSessionID != "" && args[2] != fixture.ExpectedSessionID) {
			fmt.Fprintln(os.Stderr, "unexpected session delete arguments:", args)
			return 2
		}
	}
	if fixture.CountPath != "" {
		data, _ := os.ReadFile(fixture.CountPath)
		count, _ := strconv.Atoi(string(data))
		if err := os.WriteFile(fixture.CountPath, []byte(strconv.Itoa(count+1)), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
	}
	if fixture.GatePath != "" {
		for {
			if _, err := os.Stat(fixture.GatePath); err == nil {
				break
			} else if !os.IsNotExist(err) {
				fmt.Fprintln(os.Stderr, err)
				return 2
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	if fixture.RequireEnv != "" && os.Getenv(fixture.RequireEnv) == "" {
		return 0
	}
	for _, line := range fixture.Lines {
		fmt.Fprintln(os.Stdout, line)
	}
	if fixture.Stderr != "" {
		fmt.Fprintln(os.Stderr, fixture.Stderr)
	}
	return fixture.ExitCode
}
