package api

import (
	"fmt"
	"strconv"
	"strings"
)

func ParseMilliCPU(v any) (int64, error) {
	s := scalar(v)
	if s == "" {
		return 0, nil
	}
	if strings.HasSuffix(s, "m") {
		n, err := strconv.ParseFloat(strings.TrimSuffix(s, "m"), 64)
		if err != nil {
			return 0, fmt.Errorf("cpu %q: %w", s, err)
		}
		return int64(n), nil
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("cpu %q: %w", s, err)
	}
	if n < 50 {
		return int64(n * 1000), nil
	}
	return int64(n), nil
}

func ParseMemory(v any) (int64, error) {
	s := scalar(v)
	if s == "" {
		return 0, nil
	}
	mult := int64(1)
	for _, suf := range []struct {
		s string
		m int64
	}{
		{"Ki", 1 << 10},
		{"Mi", 1 << 20},
		{"Gi", 1 << 30},
		{"Ti", 1 << 40},
		{"K", 1000},
		{"M", 1000 * 1000},
		{"G", 1000 * 1000 * 1000},
		{"k", 1000},
	} {
		if strings.HasSuffix(s, suf.s) {
			s = strings.TrimSuffix(s, suf.s)
			mult = suf.m
			break
		}
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("memory %q: %w", scalar(v), err)
	}
	return int64(n * float64(mult)), nil
}

func scalar(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(t)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(t), 'f', -1, 64)
	default:
		return fmt.Sprint(t)
	}
}
