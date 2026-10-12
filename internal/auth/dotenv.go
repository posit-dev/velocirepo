package auth

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

func UpsertEnvFile(path string, updates map[string]string) error {
	lines, err := readLines(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", path, err)
	}

	remaining := make(map[string]bool, len(updates))
	for k := range updates {
		remaining[k] = true
	}

	var result []string
	for _, line := range lines {
		key := envKey(line)
		if key != "" && remaining[key] {
			result = append(result, key+"="+quoteEnvValue(updates[key]))
			delete(remaining, key)
		} else {
			result = append(result, line)
		}
	}

	for key, val := range updates {
		if remaining[key] {
			result = append(result, key+"="+quoteEnvValue(val))
		} else {
			_ = val
		}
	}

	content := strings.Join(result, "\n")
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}

	return os.WriteFile(path, []byte(content), 0600)
}

func ReadEnvValue(path, key string) string {
	lines, err := readLines(path)
	if err != nil {
		return ""
	}
	for _, line := range lines {
		k := envKey(line)
		if k == key {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				return UnquoteEnvValue(parts[1])
			}
		}
	}
	return ""
}

func readLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines, scanner.Err()
}

func envKey(line string) string {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return ""
	}
	parts := strings.SplitN(line, "=", 2)
	if len(parts) != 2 {
		return ""
	}
	return strings.TrimSpace(parts[0])
}

func quoteEnvValue(val string) string {
	// \n and \r are included so that values with embedded newlines or
	// carriage returns are quoted and escaped, not written as literal line
	// breaks that would corrupt the file.
	if strings.ContainsAny(val, " \t\n\r#=\"'\\$`!") {
		r := strings.NewReplacer(
			"\\", "\\\\",
			"\"", "\\\"",
			"$", "\\$",
			"`", "\\`",
			"!", "\\!",
			"\n", "\\n",
			"\r", "\\r",
		)
		return "\"" + r.Replace(val) + "\""
	}
	return val
}

// UnquoteEnvValue reverses quoteEnvValue: if val is wrapped in double quotes
// it removes them and unescapes the interior; otherwise it returns val
// unchanged. It is exported so that commands that read .env files can use
// the same logic as auth.UpsertEnvFile writes.
func UnquoteEnvValue(val string) string {
	if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
		val = val[1 : len(val)-1]
		r := strings.NewReplacer(
			"\\\"", "\"",
			"\\$", "$",
			"\\`", "`",
			"\\!", "!",
			"\\n", "\n",
			"\\r", "\r",
			"\\\\", "\\",
		)
		return r.Replace(val)
	}
	return val
}
