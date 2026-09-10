package main

import (
	"fmt"
	"strconv"
	"strings"
)

func parseID(raw string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("identificador inválido")
	}
	return id, nil
}
