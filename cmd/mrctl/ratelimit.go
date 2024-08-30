package main

import (
	"fmt"
	"io"
)

func renderRateLimited(stderr io.Writer, op string, he *httpError) int {
	retry := he.headers.Get("Retry-After")
	if retry != "" {
		fmt.Fprintf(stderr, "%s_rate_limited: try again in %ss\n", op, retry)
	} else {
		fmt.Fprintf(stderr, "%s_rate_limited\n", op)
	}
	return 1
}
