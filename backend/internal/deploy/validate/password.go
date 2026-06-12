// Package validate holds deployment-management input validation rules shared
// by the global-config and component-override write paths.
package validate

import (
	"fmt"
	"sort"
	"strings"
)

// forbiddenPasswordChars are shell metacharacters that cannot appear in any
// password parameter, because passwords are embedded into single-quoted shell
// strings during render. Allowed: alphanumerics plus @#%.!_- and similar.
const forbiddenPasswordChars = "'\"\\$` "

// PasswordChars validates a single password-like value. It returns an error
// naming the first offending character (or all of them) if the value contains
// any forbidden shell metacharacter.
func PasswordChars(field, value string) error {
	var found []string
	seen := map[rune]bool{}
	for _, r := range value {
		if strings.ContainsRune(forbiddenPasswordChars, r) && !seen[r] {
			seen[r] = true
			found = append(found, describeChar(r))
		}
	}
	if len(found) == 0 {
		return nil
	}
	sort.Strings(found)
	return fmt.Errorf("%s 含有不允许的字符 %s（密码不能包含 shell 元字符：单引号 双引号 反斜杠 $ 反引号 空格）",
		field, strings.Join(found, " "))
}

// PasswordParams scans a parameter map and validates every key that looks like
// a password (key contains "password" or "passwd", case-insensitive).
func PasswordParams(params map[string]any) error {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		lk := strings.ToLower(k)
		if !strings.Contains(lk, "password") && !strings.Contains(lk, "passwd") {
			continue
		}
		if sv, ok := params[k].(string); ok {
			if err := PasswordChars(k, sv); err != nil {
				return err
			}
		}
	}
	return nil
}

func describeChar(r rune) string {
	switch r {
	case '\'':
		return "单引号(')"
	case '"':
		return "双引号(\")"
	case '\\':
		return "反斜杠(\\)"
	case '$':
		return "$"
	case '`':
		return "反引号(`)"
	case ' ':
		return "空格"
	default:
		return string(r)
	}
}
