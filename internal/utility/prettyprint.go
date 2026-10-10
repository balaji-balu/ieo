package utility
import (
	"encoding/json"
)
func Pretty(v interface{}) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}