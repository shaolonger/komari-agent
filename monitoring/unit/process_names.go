package monitoring

func isDecimalPID(value string) bool {
	if value == "" {
		return false
	}
	for index := range len(value) {
		if value[index] < '0' || value[index] > '9' {
			return false
		}
	}
	return true
}

func countDecimalPIDs(names []string) int {
	count := 0
	for _, name := range names {
		if isDecimalPID(name) {
			count++
		}
	}
	return count
}
