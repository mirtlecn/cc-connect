package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestMessageTriggerPowerShellNativeArguments(t *testing.T) {
	const helperEnv = "CC_CONNECT_TEST_TRIGGER_ARGUMENTS"
	if os.Getenv(helperEnv) == "1" {
		marker := slices.Index(os.Args, "--")
		if marker == -1 {
			os.Exit(2)
		}
		if err := json.NewEncoder(os.Stdout).Encode(os.Args[marker+1:]); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	shell := requireTestPowerShell7(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(helperEnv, "1")
	value := "原样传入 /path " + `"quotes" 'single' & | ; $env:PATH $(Write-Output bypass)` + "\n末尾\\"
	body := "第一行\n第二行"
	msg := &Message{Trigger: &MessageTriggerEvent{Body: body}, triggerCaptures: []string{value, ""}}
	command := fmt.Sprintf(`& '%s' '-test.run=^TestMessageTriggerPowerShellNativeArguments$' -- $1 "$2" ${message}`, strings.ReplaceAll(executable, "'", "''"))
	for _, flag := range []string{"-Command", "-c"} {
		t.Run(flag, func(t *testing.T) {
			e := &Engine{ctx: context.Background(), shell: shell, shellFlag: flag}
			output, err := e.runMessageTriggerShell(command, t.TempDir(), msg)
			if err != nil {
				t.Fatalf("message trigger shell failed: %v; output: %q", err, output)
			}
			var got []string
			if err := json.Unmarshal([]byte(output), &got); err != nil {
				t.Fatalf("decode native command arguments: %v; output: %q", err, output)
			}
			if want := []string{value, "", body}; !slices.Equal(got, want) {
				t.Fatalf("native command arguments = %q, want %q", got, want)
			}
		})
	}
}

func TestMessageTriggerPowerShellScriptNamedArgument(t *testing.T) {
	shell := requireTestPowerShell7(t)
	workDir := t.TempDir()
	script := filepath.Join(workDir, "record task.ps1")
	if err := os.WriteFile(script, []byte("param([string]$Text)\n[Console]::WriteLine($Text)\n"), 0600); err != nil {
		t.Fatal(err)
	}
	value := "待办“引号”" + ` "literal" 'one' & $env:PATH`
	msg := &Message{Trigger: &MessageTriggerEvent{}, triggerCaptures: []string{value}}
	command := fmt.Sprintf(`& '%s' -Text "$1"`, strings.ReplaceAll(filepath.ToSlash(script), "'", "''"))
	e := &Engine{ctx: context.Background(), shell: shell, shellFlag: "-Command"}
	output, err := e.runMessageTriggerShell(command, workDir, msg)
	if err != nil || output != value {
		t.Fatalf("named script argument = %q, %v; want %q", output, err, value)
	}
	output, err = e.runMessageTriggerShell(`Write-Output '命令已运行'`, workDir, msg)
	if err != nil || output != "命令已运行" {
		t.Fatalf("static UTF-8 output = %q, %v", output, err)
	}
}

func TestMessageTriggerWindowsPowerShellStaticCommandAndUnsupportedParameters(t *testing.T) {
	e := &Engine{ctx: context.Background(), shell: "powershell.exe", shellFlag: "-Command"}
	msg := &Message{Trigger: &MessageTriggerEvent{Body: "测试内容"}}
	workDir := t.TempDir()
	output, err := e.runMessageTriggerShell(`Write-Output 'command executed'`, workDir, msg)
	if err != nil || output != "command executed" {
		t.Fatalf("static shell result = %q, %v", output, err)
	}
	output, err = e.runMessageTriggerShell(`Write-Output ${message}`, workDir, msg)
	if output != "" || err == nil || !strings.Contains(err.Error(), "require PowerShell 7.3 or later") {
		t.Fatalf("parameterized shell result = %q, %v; want version requirement", output, err)
	}
}

func TestMessageTriggerPowerShellOutputFromGBKConsole(t *testing.T) {
	const helperEnv = "CC_CONNECT_TEST_TRIGGER_GBK_SHELL"
	const codePage = 936
	const want = "长江回声：中文命令已运行"
	if shell := os.Getenv(helperEnv); shell != "" {
		if err := windows.SetConsoleOutputCP(codePage); err != nil {
			t.Fatalf("set isolated console output code page: %v", err)
		}
		assertCodePage := func() {
			t.Helper()
			if page, err := windows.GetConsoleOutputCP(); err != nil || page != codePage {
				t.Fatalf("parent console output code page = %d, %v; want %d", page, err, codePage)
			}
		}
		assertCodePage()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		e := &Engine{ctx: ctx, shell: shell, shellFlag: "-Command"}
		msg := &Message{Trigger: &MessageTriggerEvent{}}
		workDir := t.TempDir()
		assertOutput := func(command string) {
			t.Helper()
			output, err := e.runMessageTriggerShell(command, workDir, msg)
			assertCodePage()
			if err != nil || output != want {
				t.Errorf("message trigger output = %q, %v; want %q", output, err, want)
			}
		}
		assertOutput(`Write-Output '` + want + `'`)
		e.shellProfile = "using namespace System\n$echoPrefix = '长江回声'"
		assertOutput(`Write-Output ($echoPrefix + '：中文命令已运行')`)
		e.shellProfile = ""

		var exitErr *exec.ExitError
		output, err := e.runMessageTriggerShell(`cmd.exe /d /c exit 7`, workDir, msg)
		assertCodePage()
		if output != "" || !errors.As(err, &exitErr) {
			t.Errorf("native command failure returned %q, %v; want empty output and a nonzero exit status", output, err)
		}
		output, err = e.runMessageTriggerShell(`exit 7`, workDir, msg)
		assertCodePage()
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 7 {
			t.Errorf("explicit exit returned %q, %v; want exit status 7", output, err)
		}
		return
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, shell := range []string{"powershell.exe", "pwsh.exe"} {
		t.Run(shell, func(t *testing.T) {
			shellPath, err := exec.LookPath(shell)
			if err != nil && shell == "pwsh.exe" {
				t.Skip("PowerShell Core is not installed")
			}
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestMessageTriggerPowerShellOutputFromGBKConsole$")
			cmd.Env = append(os.Environ(), helperEnv+"="+shellPath)
			cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_CONSOLE, HideWindow: true}
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("isolated GBK console test failed: %v\n%s", err, output)
			}
		})
	}
}

func requireTestPowerShell7(t *testing.T) string {
	t.Helper()
	shell, err := exec.LookPath("pwsh.exe")
	if err != nil {
		t.Skip("PowerShell 7.3 or later is not installed")
	}
	if err := exec.Command(shell, "-NoProfile", "-Command", `exit [int]($PSVersionTable.PSVersion -lt [version]'7.3')`).Run(); err != nil {
		t.Skip("PowerShell 7.3 or later is not installed")
	}
	return shell
}
