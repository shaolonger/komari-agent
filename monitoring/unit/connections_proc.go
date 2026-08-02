package monitoring

import (
	"bufio"
	"io"
)

// countProcNetTable counts rows in a Linux /proc/net socket table without
// parsing or retaining connection objects. The first non-empty row is the
// kernel header.
func countProcNetTable(reader io.Reader) (int, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 1024), 64*1024)
	headerSeen := false
	count := 0
	for scanner.Scan() {
		if len(scanner.Bytes()) == 0 {
			continue
		}
		if !headerSeen {
			headerSeen = true
			continue
		}
		count++
	}
	return count, scanner.Err()
}
