package core

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
)

const messageTriggerShellTimeout = 60 * time.Second
const messageTriggerShellOutputLimit = 16 * 1024

type triggerShellOutput struct {
	buffer    bytes.Buffer
	truncated bool
}

func (o *triggerShellOutput) Write(data []byte) (int, error) {
	size := min(len(data), messageTriggerShellOutputLimit-o.buffer.Len())
	_, _ = o.buffer.Write(data[:size])
	o.truncated = o.truncated || size < len(data)
	return len(data), nil
}

func (e *Engine) startMessageTriggerShell(p Platform, msg *Message, agent Agent, rule *MessageTriggerRule) {
	select {
	case e.messageTriggers.shells <- struct{}{}:
	case <-e.ctx.Done():
		return
	default:
		slog.Warn("message trigger: shell concurrency limit", "project", e.name, "rule", rule.Name)
		e.replyToMessageTrigger(p, msg.triggerReplyCtx, e.i18n.T(MsgPreviousProcessing), "", "")
		return
	}

	workDir := e.commandWorkDir(agent, msg)
	runMessageAccepted(msg)
	go func() {
		defer func() { <-e.messageTriggers.shells }()
		if indicator, ok := p.(TypingIndicator); ok {
			if stop := indicator.StartTyping(e.ctx, msg.ReplyCtx); stop != nil {
				defer stop()
			}
		}
		result, err := e.runMessageTriggerShell(rule.Command, workDir)
		if err != nil {
			slog.Warn("message trigger: command failed", "project", e.name, "rule", rule.Name, "error", err)
			result = strings.TrimSpace(e.i18n.Tf(MsgError, err) + "\n" + result)
		} else if result == "" {
			result = e.i18n.T(MsgEmptyResponse)
		}
		e.replyToMessageTrigger(p, msg.triggerReplyCtx, result, "", workDir)
	}()
}

func (e *Engine) runMessageTriggerShell(command, workDir string) (string, error) {
	ctx, cancel := context.WithTimeout(e.ctx, messageTriggerShellTimeout)
	defer cancel()

	// Source message content is never used in argv, the environment or stdin.
	cmd := shellExecCommand(ctx, e.shell, e.shellFlag, e.shellProfile, command)
	cmd.Dir = workDir
	if runAsUser := e.runAsUser(); runAsUser != "" {
		if err := VerifyRunAsUserCheap(ctx, ExecSudoRunner{}, runAsUser); err != nil {
			return "", fmt.Errorf("verify command user's isolation: %w", err)
		}
		opts := SpawnOptions{RunAsUser: runAsUser, WorkDir: workDir}
		if a, ok := e.agent.(interface{ GetRunAsEnv() []string }); ok {
			opts.EnvAllowlist = a.GetRunAsEnv()
		}
		cmd = BuildSpawnCommand(ctx, opts, cmd.Args[0], cmd.Args[1:]...)
		cmd.Env = append(FilterEnvForSpawn(os.Environ(), opts), RunAsChdirEnv+"="+workDir)
	}
	var output triggerShellOutput
	cmd.Stdout, cmd.Stderr = &output, &output
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	result := strings.TrimSpace(strings.ToValidUTF8(output.buffer.String(), "�"))
	if output.truncated {
		result += "\n…"
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	return result, err
}
