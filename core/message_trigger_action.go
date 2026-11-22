package core

import (
	"fmt"
	"regexp"
	"strconv"
)

var messageTriggerActionReference = regexp.MustCompile(`\$\$|\$\{message\}|\$[1-9][0-9]*`)

func expandMessageTriggerPrompt(template, message string, captures []string) (string, error) {
	var missing string
	prompt := messageTriggerActionReference.ReplaceAllStringFunc(template, func(reference string) string {
		if reference == "${message}" {
			return message
		}
		if reference == "$$" || captures == nil {
			return reference
		}
		index, err := strconv.Atoi(reference[1:])
		if err != nil || index > len(captures) {
			if missing == "" {
				missing = reference
			}
			return reference
		}
		return captures[index-1]
	})
	if missing != "" {
		return "", fmt.Errorf("message trigger capture %s is unavailable", missing)
	}
	return prompt, nil
}
