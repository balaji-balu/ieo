package util
import (
    "strings"
    "fmt"
)
type AppSelector struct {
    Category string
    App      string
    Version  string
}

func ParseAppSelector(input string) (AppSelector, error) {
    if input == "" {
        return AppSelector{}, nil
    }

    // convert to lower-case before splitting
    input = strings.ToLower(strings.TrimSpace(input))

    parts := strings.Split(input, "/")
    sel := AppSelector{}

    switch len(parts) {
    case 1:
        sel.Category = parts[0]
    case 2:
        sel.Category = parts[0]
        sel.App = parts[1]
    case 3:
        sel.Category = parts[0]
        sel.App = parts[1]
        sel.Version = parts[2]
    default:
        return sel, fmt.Errorf("invalid name format '%s', expected category/app[/version]", input)
    }
    return sel, nil
}
