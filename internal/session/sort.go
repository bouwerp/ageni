package session

import (
	"sort"
	"strings"
)

// SortIDs sorts a slice of IDs (like s1, s100, sh2) numerically by their numeric suffix.
func SortIDs(ids []string) {
	sort.Slice(ids, func(i, j int) bool {
		return compareNatural(ids[i], ids[j])
	})
}

func compareNatural(a, b string) bool {
	idxA := strings.IndexAny(a, "0123456789")
	idxB := strings.IndexAny(b, "0123456789")
	if idxA == -1 || idxB == -1 {
		return a < b
	}
	prefixA := a[:idxA]
	prefixB := b[:idxB]
	if prefixA != prefixB {
		return prefixA < prefixB
	}
	numA := 0
	for i := idxA; i < len(a); i++ {
		if a[i] >= '0' && a[i] <= '9' {
			numA = numA*10 + int(a[i]-'0')
		} else {
			break
		}
	}
	numB := 0
	for i := idxB; i < len(b); i++ {
		if b[i] >= '0' && b[i] <= '9' {
			numB = numB*10 + int(b[i]-'0')
		} else {
			break
		}
	}
	if numA != numB {
		return numA < numB
	}
	return a < b
}
