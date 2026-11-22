package core

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
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
		e.replyToMessageTrigger(p, msg.triggerReplyCtx, e.i18n.T(MsgPreviousProcessing), "")
		return
	}

	workDir := e.commandWorkDir(agent, msg)
	admitted := *msg
	msg.processingFeedback = nil
	go func() {
		defer func() { <-e.messageTriggers.shells }()
		if e.ctx.Err() != nil || e.triggerSourceRecalled(p, admitted.MessageID) {
			admitted.finishProcessingFeedback(MessageProcessingCancelled)
			return
		}
		if !e.isCurrentMessageTrigger(rule) {
			admitted.finishProcessingFeedback(MessageProcessingFailed)
			return
		}
		hasReceipt := admitted.OnAccepted != nil
		runMessageAccepted(&admitted)
		finish := admitted.processingFeedback
		if indicator, ok := p.(TypingIndicator); finish == nil && ok {
			if stop := indicator.StartTyping(e.ctx, admitted.ReplyCtx); stop != nil {
				defer stop()
			}
		}
		result, err := e.runMessageTriggerShell(rule.Command, workDir, &admitted)
		finishShellProcessingFeedback(finish, err, hasReceipt)
		if err != nil {
			slog.Warn("message trigger: command failed", "project", e.name, "rule", rule.Name, "error", err)
			result = strings.TrimSpace(e.i18n.Tf(MsgError, err) + "\n" + result)
		} else if result == "" {
			result = e.i18n.T(MsgEmptyResponse)
		}
		e.replyToMessageTrigger(p, admitted.triggerReplyCtx, result, workDir)
	}()
}

func finishShellProcessingFeedback(finish func(MessageProcessingOutcome, bool), err error, receiptOwned bool) {
	if finish == nil {
		return
	}
	if err != nil {
		finish(MessageProcessingFailed, receiptOwned)
		return
	}
	finish(MessageProcessingSucceeded, receiptOwned)
}

func (e *Engine) runMessageTriggerShell(command, workDir string, msg *Message) (string, error) {
	ctx, cancel := context.WithTimeout(e.ctx, messageTriggerShellTimeout)
	defer cancel()

	command, bindings, err := bindMessageTriggerShell(command, e.shell, e.shellFlag, msg.triggerCaptures, msg.Trigger.Body)
	if err != nil {
		return "", err
	}
	cmd := shellExecCommand(ctx, e.shell, e.shellFlag, e.shellProfile, command)
	cmd.Dir = workDir
	if runAsUser := e.runAsUser(); runAsUser != "" {
		// sudo -i passes the script through the target user's login shell.
		// That shell must not expand bindings before the configured shell runs.
		if len(bindings) != 0 {
			return "", fmt.Errorf("message trigger shell parameters are not supported with run_as_user")
		}
		if err := VerifyRunAsUserCheap(ctx, ExecSudoRunner{}, runAsUser); err != nil {
			return "", fmt.Errorf("verify command user's isolation: %w", err)
		}
		opts := SpawnOptions{RunAsUser: runAsUser, WorkDir: workDir}
		if a, ok := e.agent.(interface{ GetRunAsEnv() []string }); ok {
			opts.EnvAllowlist = a.GetRunAsEnv()
		}
		cmd = BuildSpawnCommand(ctx, opts, cmd.Args[0], cmd.Args[1:]...)
		cmd.Env = append(FilterEnvForSpawn(os.Environ(), opts), RunAsChdirEnv+"="+workDir)
	} else if len(bindings) != 0 {
		cmd.Env = append(cmd.Environ(), bindings...)
	}
	var output triggerShellOutput
	cmd.Stdout, cmd.Stderr = &output, &output
	cmd.WaitDelay = 2 * time.Second
	err = cmd.Run()
	result := strings.TrimSpace(strings.ToValidUTF8(output.buffer.String(), "�"))
	if output.truncated {
		result += "\n…"
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	return result, err
}

// bindMessageTriggerShell recognizes standalone parameters and leaves their
// values in the child environment. Only fixed environment references become
// part of the shell program.
func bindMessageTriggerShell(command, shell, flag string, captures []string, message string) (string, []string, error) {
	if !strings.Contains(command, "${message}") && (captures == nil || !strings.Contains(command, "$")) {
		return command, nil, nil
	}
	base := strings.TrimSuffix(strings.ToLower(filepath.Base(shell)), ".exe")
	powershell := strings.HasPrefix(base, "powershell") || strings.HasPrefix(base, "pwsh")
	escape := byte('\\')
	if powershell {
		escape = '`'
	}
	var script strings.Builder
	var bindings []string
	bound := map[string]bool{}
	complexSyntax := false
	commandSeen, redirectTarget := false, false
	previous := 0
	for i := 0; i < len(command); {
		c := command[i]
		if c == '#' {
			next := strings.IndexByte(command[i:], '\n')
			if next < 0 {
				break
			}
			i += next
			continue
		}
		if c == escape && i+1 < len(command) && command[i+1] == '\n' {
			i += 2
			continue
		}
		if strings.HasPrefix(command[i:], "&>") {
			i++ // Bash/Zsh redirect both output streams without ending the command.
			c = '>'
		}
		if c == '<' || c == '>' {
			complexSyntax = complexSyntax || redirectTarget || strings.HasPrefix(command[i:], "<<")
			redirectTarget = true
			i++
			if i < len(command) && strings.ContainsRune("<>&|", rune(command[i])) {
				i++ // The second character belongs to the redirect operator.
			}
			continue
		}
		if isTriggerShellSeparator(c) {
			switch c {
			case '(', ')':
				complexSyntax = true
			case '\n', ';', '|', '&':
				complexSyntax = complexSyntax || redirectTarget
				commandSeen, redirectTarget = false, false
			}
			i++
			continue
		}
		end, nested := triggerShellWordEnd(command, i, escape, base == "fish")
		complexSyntax = complexSyntax || nested
		rawWord := command[i:end]
		word := rawWord
		if len(word) >= 2 && word[0] == '"' && word[len(word)-1] == '"' {
			word = word[1 : len(word)-1]
		}
		name, value := "", ""
		switch {
		case word == "${message}":
			name, value = "CC_CONNECT_TRIGGER_MESSAGE", message
		case captures != nil && len(word) > 1 && word[0] == '$' && word[1] >= '1' && word[1] <= '9' &&
			strings.Trim(word[1:], "0123456789") == "":
			index, err := strconv.Atoi(word[1:])
			if err != nil || index > len(captures) {
				return "", nil, fmt.Errorf("message trigger shell capture %s is unavailable", word)
			}
			name, value = "CC_CONNECT_TRIGGER_CAPTURE_"+word[1:], captures[index-1]
		}
		if name != "" {
			if !commandSeen || redirectTarget {
				return "", nil, fmt.Errorf("message trigger shell parameter %s must be an argument to a fixed command", word)
			}
			if strings.ContainsRune(value, 0) {
				return "", nil, fmt.Errorf("message trigger shell parameter %s contains a NUL character", word)
			}
			script.WriteString(command[previous:i])
			switch {
			case base == "fish" && flag == "-c":
				script.WriteString(`"$` + name + `"`)
			case flag == "-c" && isTriggerPOSIXShell(base):
				script.WriteString(`"${` + name + `}"`)
			default:
				return "", nil, fmt.Errorf("message trigger shell parameters are unsupported with shell %q and flag %q", base, flag)
			}
			if !bound[name] {
				bindings = append(bindings, name+"="+value)
				bound[name] = true
			}
			previous = end
		}
		if redirectTarget {
			redirectTarget = false
		} else if !commandSeen && !isTriggerShellAssignment(rawWord) && !isTriggerShellRedirectFD(rawWord, command[end:]) {
			complexSyntax = complexSyntax || !isTriggerShellFixedCommand(rawWord)
			commandSeen = true
		}
		i = end
	}
	if len(bindings) == 0 {
		return command, nil, nil
	}
	if complexSyntax || redirectTarget {
		return "", nil, fmt.Errorf("message trigger shell parameters require simple commands with fixed command names")
	}
	script.WriteString(command[previous:])
	return script.String(), bindings, nil
}

func isTriggerShellSeparator(c byte) bool {
	switch c {
	case ' ', '\t', '\r', '\n', ';', '|', '&', '(', ')', '<', '>':
		return true
	default:
		return false
	}
}

func triggerShellWordEnd(command string, start int, escape byte, fish bool) (int, bool) {
	var quote byte
	nested := false
	for i := start; i < len(command); i++ {
		c := command[i]
		if quote == 0 && isTriggerShellSeparator(c) {
			return i, nested
		}
		if c == escape && i+1 < len(command) && (quote != '\'' || (fish && (command[i+1] == '\'' || command[i+1] == '\\'))) {
			i++
			continue
		}
		if quote != '\'' && (c == '`' || strings.HasPrefix(command[i:], "$(") || strings.HasPrefix(command[i:], "$[")) {
			nested = true
		}
		if quote == 0 && (strings.HasPrefix(command[i:], "$'") || strings.HasPrefix(command[i:], `$"`)) {
			nested = true
		}
		if c == quote {
			quote = 0
		} else if quote == 0 && (c == '\'' || c == '"') {
			quote = c
		}
	}
	return len(command), nested || quote != 0
}

func isTriggerShellAssignment(word string) bool {
	name, _, found := strings.Cut(word, "=")
	name = strings.TrimSuffix(name, "+")
	return found && name != "" && (name[0] < '0' || name[0] > '9') &&
		strings.Trim(name, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz_0123456789") == ""
}

func isTriggerShellRedirectFD(word, rest string) bool {
	return len(rest) > 0 && (rest[0] == '<' || rest[0] == '>') && strings.Trim(word, "0123456789") == ""
}

func isTriggerShellFixedCommand(word string) bool {
	if len(word) >= 2 && (word[0] == '"' || word[0] == '\'') && word[len(word)-1] == word[0] {
		word = word[1 : len(word)-1]
	}
	switch word {
	case "!", "if", "then", "elif", "else", "fi", "while", "until", "for", "do", "done", "case", "esac",
		"select", "function", "coproc", "time", "repeat", "begin", "end", "switch", "and", "or", "not",
		"eval", "exec", "command", "builtin", "let":
		return false
	case "[":
		return true // The simple test command takes ordinary arguments.
	}
	return word != "" && !strings.ContainsAny(word, "$`*?[]{}!~\\\"'")
}

func isTriggerPOSIXShell(base string) bool {
	switch base {
	case "sh", "bash", "zsh", "dash", "ash", "ksh", "mksh":
		return true
	default:
		return false
	}
}
