package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// writeFileAtomically writes data to path using a write-to-temp-then-rename
// pattern so the file is never partially written. The temp file is created in
// the same directory as path (required for an atomic rename across the same
// filesystem). mode is the file permission bits for the final file; if 0 the
// original file's mode is preserved when it exists. The temp file is removed
// on failure.
func writeFileAtomically(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)

	// If no mode provided, preserve the original file's mode.
	if mode == 0 {
		if info, err := os.Stat(path); err == nil {
			mode = info.Mode()
		} else {
			mode = 0644
		}
	}

	tmp, err := os.CreateTemp(dir, ".velocirepo-config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp config file: %w", err)
	}
	tmpPath := tmp.Name()

	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp config file: %w", err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp config file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp config file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename config file into place: %w", err)
	}

	ok = true
	return nil
}

func FindSection(lines []string, header string) (start, end int, found bool) {
	target := "[" + header + "]"
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == target {
			start = i
			for j := i + 1; j < len(lines); j++ {
				trimmed := strings.TrimSpace(lines[j])
				if strings.HasPrefix(trimmed, "[") {
					return start, j, true
				}
			}
			return start, len(lines), true
		}
	}
	return 0, 0, false
}

func AppendProject(path string, id string, project Project) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}

	content := string(data)
	if len(content) > 0 && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}

	section := formatProjectSection(id, project)
	content += "\n" + section

	return writeFileAtomically(path, []byte(content), 0)
}

func RemoveProject(path string, id string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}

	lines := strings.Split(string(data), "\n")
	header := "projects." + id
	start, end, found := FindSection(lines, header)
	if !found {
		return fmt.Errorf("project %q not found in config", id)
	}

	// Remove trailing blank lines before the next section
	for end > start && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	// Also remove one blank line before the section if present
	if start > 0 && strings.TrimSpace(lines[start-1]) == "" {
		start--
	}

	newLines := append(lines[:start], lines[end:]...)
	return writeFileAtomically(path, []byte(strings.Join(newLines, "\n")), 0)
}

func UpdateProject(path string, id string, updates map[string]string, unsets []string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}

	lines := strings.Split(string(data), "\n")
	header := "projects." + id
	start, end, found := FindSection(lines, header)
	if !found {
		return fmt.Errorf("project %q not found in config", id)
	}

	lines = applyProjectUpdates(lines, start, end, updates, unsets)

	return writeFileAtomically(path, []byte(strings.Join(collapseBlankLines(lines), "\n")), 0)
}

func applyProjectUpdates(lines []string, start, end int, updates map[string]string, unsets []string) []string {
	pendingUpdates := cloneStringMap(updates)
	unsetSet := stringSet(unsets)

	lines = updateExistingProjectKeys(lines, start, end, pendingUpdates, unsetSet)
	return insertMissingProjectKeys(lines, start, end, pendingUpdates)
}

func updateExistingProjectKeys(lines []string, start, end int, updates map[string]string, unsets map[string]bool) []string {
	for i := start + 1; i < end; i++ {
		key := extractKey(lines[i])
		if key == "" {
			continue
		}
		if unsets[key] {
			lines[i] = ""
			continue
		}
		if val, ok := updates[key]; ok {
			lines[i] = formatKeyValue(key, val)
			delete(updates, key)
		}
	}
	return lines
}

func insertMissingProjectKeys(lines []string, start, end int, updates map[string]string) []string {
	var newLines []string
	for key, val := range updates {
		newLines = append(newLines, formatKeyValue(key, val))
	}
	if len(newLines) == 0 {
		return lines
	}

	insertAt := end
	for insertAt > start+1 && strings.TrimSpace(lines[insertAt-1]) == "" {
		insertAt--
	}
	result := make([]string, 0, len(lines)+len(newLines))
	result = append(result, lines[:insertAt]...)
	result = append(result, newLines...)
	result = append(result, lines[insertAt:]...)
	return result
}

func cloneStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func stringSet(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
}

func collapseBlankLines(lines []string) []string {
	var cleaned []string
	prevBlank := false
	for _, line := range lines {
		blank := strings.TrimSpace(line) == ""
		if blank && prevBlank {
			continue
		}
		cleaned = append(cleaned, line)
		prevBlank = blank
	}
	return cleaned
}

func RenameSection(path string, oldID string, newID string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}

	oldHeader := "[projects." + oldID + "]"
	newHeader := "[projects." + newID + "]"

	lines := strings.Split(string(data), "\n")
	found := false
	for i, line := range lines {
		if strings.TrimSpace(line) == oldHeader {
			lines[i] = newHeader
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("project %q not found in config", oldID)
	}

	return writeFileAtomically(path, []byte(strings.Join(lines, "\n")), 0)
}

func formatProjectSection(id string, project Project) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[projects.%s]\n", id)
	if project.Name != "" {
		fmt.Fprintf(&b, "name = %q\n", project.Name)
	}
	for _, s := range project.Sources() {
		writeStringList(&b, s.Name, s.Values)
	}
	return b.String()
}

func writeStringList(b *strings.Builder, key string, values StringList) {
	if len(values) == 0 {
		return
	}
	if len(values) == 1 {
		fmt.Fprintf(b, "%s = %q\n", key, values[0])
		return
	}
	fmt.Fprintf(b, "%s = [", key)
	for i, v := range values {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(b, "%q", v)
	}
	b.WriteString("]\n")
}

func formatKeyValue(key, val string) string {
	if key == "name" || !strings.Contains(val, ",") {
		return fmt.Sprintf("%s = %q", key, val)
	}
	var trimmed []string
	for _, p := range strings.Split(val, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			trimmed = append(trimmed, p)
		}
	}
	if len(trimmed) <= 1 {
		v := ""
		if len(trimmed) == 1 {
			v = trimmed[0]
		}
		return fmt.Sprintf("%s = %q", key, v)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s = [", key)
	for i, v := range trimmed {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%q", v)
	}
	b.WriteString("]")
	return b.String()
}

func extractKey(line string) string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "[") {
		return ""
	}
	parts := strings.SplitN(trimmed, "=", 2)
	if len(parts) != 2 {
		return ""
	}
	return strings.TrimSpace(parts[0])
}

// SetSettingList sets a list in [settings], keeping the rest of the file as
// it is. A missing [settings] section is added before the projects.
func SetSettingList(path, key string, values []string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = fmt.Sprintf("%q", v)
	}
	line := fmt.Sprintf("%s = [%s]", key, strings.Join(quoted, ", "))

	lines := strings.Split(string(data), "\n")
	start, end, found := FindSection(lines, "settings")
	if !found {
		at := len(lines)
		for i, l := range lines {
			if strings.HasPrefix(strings.TrimSpace(l), "[projects") {
				at = i
				break
			}
		}
		section := []string{"[settings]", line, ""}
		lines = append(lines[:at], append(section, lines[at:]...)...)
		return writeFileAtomically(path, []byte(strings.Join(lines, "\n")), 0)
	}
	for i := start + 1; i < end; i++ {
		if extractKey(lines[i]) == key {
			lines[i] = line
			return writeFileAtomically(path, []byte(strings.Join(lines, "\n")), 0)
		}
	}
	insert := start + 1
	for insert < end && extractKey(lines[insert]) != "" {
		insert++
	}
	lines = append(lines[:insert], append([]string{line}, lines[insert:]...)...)
	return writeFileAtomically(path, []byte(strings.Join(lines, "\n")), 0)
}
