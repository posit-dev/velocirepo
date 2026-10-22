package cmd

import (
	"encoding/json"
	"os"

	"github.com/posit-dev/velocirepo/internal/check"
)

func writeReportJSON(path string, report check.Report) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}
