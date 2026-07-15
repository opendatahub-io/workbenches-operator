package hardwareprofile

import "k8s.io/apimachinery/pkg/api/resource"

// ParseQuantityValue exports parseQuantityValue for testing.
func ParseQuantityValue(val any) (resource.Quantity, error) {
	return parseQuantityValue(val)
}
