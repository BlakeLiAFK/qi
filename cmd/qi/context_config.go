package main

import (
	"errors"
	"strconv"
	"strings"

	"gopi/internal/contextbudget"
)

func contextInputBudget(env func(string) string) (int, error) {
	raw := strings.TrimSpace(env("QI_MAX_CONTEXT_BYTES"))
	if raw == "" {
		return contextbudget.DefaultMaxInputBytes, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, errors.New("QI_MAX_CONTEXT_BYTES must be a positive integer byte budget (default 262144); this is not a token limit")
	}
	return n, nil
}
