package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func WriteAuditLog(seriesDir string, started time.Time, results []Result) (string, error) {
	base := fmt.Sprintf("run-%s", started.UTC().Format("20060102T150405.000000000Z"))
	var filename string
	var file *os.File
	for sequence := 0; ; sequence++ {
		name := base + ".log"
		if sequence > 0 {
			name = fmt.Sprintf("%s-%03d.log", base, sequence)
		}
		filename = filepath.Join(seriesDir, name)
		var err error
		file, err = os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			break
		}
		if !os.IsExist(err) {
			return "", fmt.Errorf("create run log: %w", err)
		}
	}
	ok := false
	defer func() {
		file.Close()
		if !ok {
			os.Remove(filename)
		}
	}()
	counts := map[Status]int{Done: 0, Part: 0, Fail: 0, NoImg: 0}
	pagesOK, pagesFailed := 0, 0
	for _, result := range results {
		counts[result.Status]++
		pagesOK += result.Success
		pagesFailed += result.Total - result.Success
		if _, err := fmt.Fprintf(file, "chapter %s: %s pages=%d/%d\n", result.Chapter.Display, result.Label(), result.Success, result.Total); err != nil {
			return "", err
		}
		for _, message := range result.Errors {
			message = strings.ReplaceAll(message, "\n", " ")
			if _, err := fmt.Fprintf(file, "  %s\n", message); err != nil {
				return "", err
			}
		}
	}
	if _, err := fmt.Fprintf(file, "summary DONE=%d PART=%d FAIL=%d NOIMG=%d pages_ok=%d pages_failed=%d\n", counts[Done], counts[Part], counts[Fail], counts[NoImg], pagesOK, pagesFailed); err != nil {
		return "", err
	}
	if err := file.Sync(); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	ok = true
	return filename, nil
}
