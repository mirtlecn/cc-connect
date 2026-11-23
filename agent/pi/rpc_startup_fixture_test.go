package pi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"testing"
)

func newPiRPCStartupFixture(t *testing.T, ctx context.Context, mode, sessionID string) (*piSession, error) {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"-test.run=^TestPiRPCStartupFixtureProcess$", "--"}
	env := []string{"CC_CONNECT_TEST_PI_STARTUP_MODE=" + mode, "CC_CONNECT_TEST_PI_STARTUP_SESSION=" + sessionID}
	return newPiSession(ctx, bin, args, t.TempDir(), "", "", "", true, "", env)
}

func TestPiRPCStartupFixtureProcess(t *testing.T) {
	mode := os.Getenv("CC_CONNECT_TEST_PI_STARTUP_MODE")
	if mode == "" {
		return
	}
	input, output := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	emit := func(event map[string]any) {
		if err := output.Encode(event); err != nil {
			os.Exit(2)
		}
	}
	extension := func(id string) {
		emit(map[string]any{"type": "extension_ui_request", "id": id, "method": "setStatus", "statusKey": "plan-mode"})
	}
	extension("ext-init")
	for i := 1; ; i++ {
		var command struct {
			ID   string
			Type string
		}
		if err := input.Decode(&command); err != nil {
			if err == io.EOF {
				os.Exit(0)
			}
			os.Exit(2)
		}
		if mode == "extension-ui" {
			extension(fmt.Sprintf("ext-%d", i))
			continue
		}
		if command.Type != "get_state" {
			continue
		}
		event := map[string]any{"type": "response", "id": command.ID, "command": command.Type, "success": mode == "success"}
		if mode == "success" {
			event["data"] = map[string]string{"sessionId": os.Getenv("CC_CONNECT_TEST_PI_STARTUP_SESSION"), "sessionFile": "fake.jsonl"}
		} else {
			event["error"] = "session not available"
		}
		emit(event)
	}
}
