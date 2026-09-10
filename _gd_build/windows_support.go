//go:build windows

package main

import (
	"errors"
	"strings"
	"time"
)

func parseUserDate(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	for _, layout := range []string{"2006-01-02", "02/01/2006", "2/1/2006"} {
		if t, err := time.ParseInLocation(layout, raw, time.Local); err == nil {
			return t.Format("2006-01-02"), nil
		}
	}
	return "", errors.New("data inválida; use DD/MM/AAAA")
}

func parseUserAmount(raw string) (int64, error) {
	return parseAmountCell(rawCell{Text: raw})
}
